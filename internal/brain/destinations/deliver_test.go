package destinations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/brain/missions"
)

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

type fakeDestStore struct {
	rows map[string]Destination
}

func (f *fakeDestStore) Get(_ context.Context, id string) (Destination, error) {
	d, ok := f.rows[id]
	if !ok {
		return Destination{}, ErrNotFound
	}
	return d, nil
}

type fakeEventStore struct {
	mu     sync.Mutex
	events []missions.Event
}

func (f *fakeEventStore) AppendEvent(_ context.Context, _, kind string, payload map[string]any) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, _ := json.Marshal(payload)
	f.events = append(f.events, missions.Event{Kind: kind, Payload: data})
	return nil
}

func (f *fakeEventStore) Events(_ context.Context, _ string) ([]missions.Event, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]missions.Event, len(f.events))
	copy(out, f.events)
	return out, nil
}

// fakeAdapter fails the first failCount calls then succeeds (or always
// fails when failCount is negative).
type fakeAdapter struct {
	mu        sync.Mutex
	calls     int
	failCount int
}

func (f *fakeAdapter) Deliver(_ context.Context, _ json.RawMessage, _ string, _ Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.failCount < 0 || f.calls <= f.failCount {
		return errors.New("simulated delivery failure")
	}
	return nil
}

func (f *fakeAdapter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// maybeDeliveredAdapter always fails with errMaybeDelivered: the
// "the request may have reached the provider" case deliverOne must
// never retry.
type maybeDeliveredAdapter struct {
	mu    sync.Mutex
	calls int
}

func (f *maybeDeliveredAdapter) Deliver(_ context.Context, _ json.RawMessage, _ string, _ Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return fmt.Errorf("timeout: %w", errMaybeDelivered)
}

func (f *maybeDeliveredAdapter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func withFastBackoff(t *testing.T) {
	t.Helper()
	orig := deliverBackoff
	deliverBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { deliverBackoff = orig })
}

func entries(ids ...string) []missions.DestinationEntry {
	out := make([]missions.DestinationEntry, len(ids))
	for i, id := range ids {
		out[i] = missions.DestinationEntry{DestinationID: id}
	}
	return out
}

func TestDeliverZeroDestinationsNoop(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{}}
	eventStore := &fakeEventStore{}
	d := NewDeliverer(destStore, eventStore, nil, &WebhookAdapter{}, nil, nil, nil, nil, discardLog())

	if _, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, nil); err != nil {
		t.Fatalf("Deliver with zero destinations: %v", err)
	}

	if len(eventStore.events) != 0 {
		t.Fatalf("expected no events for zero destinations, got %d", len(eventStore.events))
	}
}

func TestDeliverRetryThenSucceed(t *testing.T) {
	withFastBackoff(t)
	adapter := &fakeAdapter{failCount: 2} // fails twice, succeeds on 3rd (last) attempt
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	updated, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if adapter.callCount() != 3 {
		t.Fatalf("expected 3 attempts, got %d", adapter.callCount())
	}
	if len(eventStore.events) != 1 || eventStore.events[0].Kind != eventDelivered {
		t.Fatalf("expected one mission.delivered event, got %+v", eventStore.events)
	}
	if len(updated) != 1 || updated[0].DeliveredAt == "" {
		t.Fatalf("expected the returned entry to carry delivered_at, got %+v", updated)
	}
}

func TestDeliverRetryExhaustedThenFail(t *testing.T) {
	withFastBackoff(t)
	adapter := &fakeAdapter{failCount: -1} // always fails
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	updated, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1"))
	if err == nil {
		t.Fatal("Deliver with an always-failing adapter: want an error, got nil")
	}

	if adapter.callCount() != 3 {
		t.Fatalf("expected 3 attempts (1 + 2 retries), got %d", adapter.callCount())
	}
	if len(eventStore.events) != 1 || eventStore.events[0].Kind != eventDeliveryFailed {
		t.Fatalf("expected one mission.delivery_failed event, got %+v", eventStore.events)
	}
	if len(updated) != 1 || updated[0].Error == "" || updated[0].DeliveredAt != "" {
		t.Fatalf("expected the returned entry to carry error, no delivered_at, got %+v", updated)
	}
}

// TestDeliverStopsRetryingOnMaybeDelivered covers the fix: an error
// wrapping errMaybeDelivered (the request may have already reached the
// provider) must stop the retry loop after the first attempt, unlike a
// plain failure which retries the full schedule.
func TestDeliverStopsRetryingOnMaybeDelivered(t *testing.T) {
	withFastBackoff(t)
	adapter := &maybeDeliveredAdapter{}
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "telegram-1", Kind: "telegram", Enabled: true, Config: json.RawMessage(`{"chat_id":"1"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"telegram": adapter}, log: discardLog()}

	if _, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1")); err == nil {
		t.Fatal("Deliver with a maybe-delivered error: want an error, got nil")
	}

	if adapter.callCount() != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry on errMaybeDelivered), got %d", adapter.callCount())
	}
	if len(eventStore.events) != 1 || eventStore.events[0].Kind != eventDeliveryFailed {
		t.Fatalf("expected one mission.delivery_failed event, got %+v", eventStore.events)
	}
}

func TestDeliverOncePerMissionGuard(t *testing.T) {
	withFastBackoff(t)
	adapter := &fakeAdapter{failCount: 0}
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	first, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	// Re-drive with the entries Deliver just returned (delivered_at set):
	// the driver always feeds the previous round's entries back in.
	if _, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, first); err != nil {
		t.Fatalf("Deliver (re-drive): %v", err)
	}

	if adapter.callCount() != 1 {
		t.Fatalf("expected exactly 1 delivery attempt across both calls, got %d", adapter.callCount())
	}
	if len(eventStore.events) != 1 {
		t.Fatalf("expected exactly 1 outcome event, got %d", len(eventStore.events))
	}
}

// TestDeliverRedriveRetriesOnlyFailedDestination covers D-086's
// result-phase retry contract: an entry that recorded an error (not
// delivered_at) is retried on a re-drive, unlike
// TestDeliverOncePerMissionGuard's already-delivered case above.
func TestDeliverRedriveRetriesOnlyFailedDestination(t *testing.T) {
	withFastBackoff(t)
	adapter := &fakeAdapter{failCount: -1} // always fails
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	first, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1"))
	if err == nil {
		t.Fatal("Deliver with an always-failing adapter: want an error, got nil")
	}
	firstAttempts := adapter.callCount()

	adapter.failCount = 0 // the retry succeeds this time
	updated, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, first)
	if err != nil {
		t.Fatalf("Deliver (retry): %v", err)
	}

	if got := adapter.callCount(); got <= firstAttempts {
		t.Fatalf("adapter attempts after retry = %d, want more than the first round's %d (failed destination must be retried)", got, firstAttempts)
	}
	if len(updated) != 1 || updated[0].DeliveredAt == "" {
		t.Fatalf("expected the retried entry to carry delivered_at, got %+v", updated)
	}
	var delivered, failed int
	for _, ev := range eventStore.events {
		switch ev.Kind {
		case eventDelivered:
			delivered++
		case eventDeliveryFailed:
			failed++
		}
	}
	if delivered != 1 {
		t.Fatalf("mission.delivered events = %d, want 1 (the retry's eventual success)", delivered)
	}
	if failed == 0 {
		t.Fatal("mission.delivery_failed events = 0, want at least 1 (the first round's failure)")
	}
}

func TestDeliverUnknownDestinationRecordsFailure(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": &WebhookAdapter{}}, log: discardLog()}

	if _, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("missing")); err == nil {
		t.Fatal("Deliver against an unknown destination: want an error, got nil")
	}

	if len(eventStore.events) != 1 || eventStore.events[0].Kind != eventDeliveryFailed {
		t.Fatalf("expected one mission.delivery_failed event, got %+v", eventStore.events)
	}
}

func TestDeliverDisabledDestinationRecordsFailure(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "off", Kind: "webhook", Enabled: false},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"webhook": &WebhookAdapter{}}, log: discardLog()}

	if _, err := d.Deliver(t.Context(), missions.Mission{ID: "m1"}, entries("d1")); err == nil {
		t.Fatal("Deliver against a disabled destination: want an error, got nil")
	}

	if len(eventStore.events) != 1 || eventStore.events[0].Kind != eventDeliveryFailed {
		t.Fatalf("expected one mission.delivery_failed event, got %+v", eventStore.events)
	}
}

func TestDeliverNowSuccess(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	adapter := &fakeAdapter{failCount: 0}
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	name, kind, err := d.DeliverNow(t.Context(), "d1", "subject", "body")
	if err != nil {
		t.Fatalf("DeliverNow: %v", err)
	}
	if name != "webhook-1" || kind != "webhook" {
		t.Fatalf("name/kind = %q/%q, want webhook-1/webhook", name, kind)
	}
	if adapter.callCount() != 1 {
		t.Fatalf("expected exactly 1 call, no retry, got %d", adapter.callCount())
	}
}

func TestDeliverNowUnknownDestination(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{}}
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{}, log: discardLog()}
	if _, _, err := d.DeliverNow(t.Context(), "missing", "s", "b"); err == nil {
		t.Fatal("expected error for unknown destination")
	}
}

func TestDeliverNowDisabledDestination(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "off", Kind: "webhook", Enabled: false},
	}}
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{"webhook": &WebhookAdapter{}}, log: discardLog()}
	_, _, err := d.DeliverNow(t.Context(), "d1", "s", "b")
	if err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "off") {
		t.Fatalf("err = %v, want disabled error naming the destination", err)
	}
}

func TestDeliverNowNeverRetries(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	adapter := &fakeAdapter{failCount: -1} // always fails
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{"webhook": adapter}, log: discardLog()}

	_, _, err := d.DeliverNow(t.Context(), "d1", "s", "b")
	if err == nil {
		t.Fatal("expected the adapter error surfaced")
	}
	if adapter.callCount() != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry), got %d", adapter.callCount())
	}
}

// TestDeliverGitHubRoutesToAdapter proves a "github" kind destination
// row routes to the wired GitHubAdapter (not the Adapter map) and
// records the delivered pr_url/pr_number/branch/remote_host on the
// entry, same as any other successful delivery.
func TestDeliverGitHubRoutesToAdapter(t *testing.T) {
	m := pushableMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"conn1","mode":"push_pr"}`)},
	}}
	eventStore := &fakeEventStore{}
	p := &fakePusher{host: "github.com"}
	pr := &fakePRSource{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/1", prNumber: 1}
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
	github := &GitHubAdapter{Pusher: p, Events: eventStore, ResolveToken: resolveToken, PR: pr}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{}, github: github, log: discardLog()}

	updated, err := d.Deliver(t.Context(), m, entries("d1"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(updated) != 1 || updated[0].DeliveredAt == "" {
		t.Fatalf("expected the entry to carry delivered_at, got %+v", updated)
	}
	if updated[0].PRURL != "https://github.com/octo/repo/pull/1" || updated[0].PRNumber != 1 {
		t.Fatalf("entry after delivery = %+v, want pr_url/pr_number recorded", updated[0])
	}
	if p.pushCalls != 1 {
		t.Fatalf("Push called %d times, want exactly 1", p.pushCalls)
	}
}

// TestDeliverGitHubNoAdapterFails proves a "github" row with no
// GitHubAdapter wired fails cleanly (connectors disabled), same as any
// other kind's nil-adapter case.
func TestDeliverGitHubNoAdapterFails(t *testing.T) {
	m := pushableMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"conn1","mode":"push"}`)},
	}}
	eventStore := &fakeEventStore{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{}, log: discardLog()}

	if _, err := d.Deliver(t.Context(), m, entries("d1")); err == nil {
		t.Fatal("Deliver against a github destination with no adapter wired: want an error, got nil")
	}
}

// capturingAdapter records the last Payload it was asked to deliver,
// TestDeliverGitHubDeliversBeforeMessageKinds' way of proving the
// message payload was rendered AFTER the github delivery ran.
type capturingAdapter struct {
	last Payload
}

func (a *capturingAdapter) Deliver(_ context.Context, _ json.RawMessage, _ string, payload Payload) error {
	a.last = payload
	return nil
}

// TestDeliverGitHubDeliversBeforeMessageKinds proves a mission with a
// telegram entry listed BEFORE a github entry still gets the fresh PR
// URL in the message payload's links (issue #561): github delivers
// first, Render runs against the resulting mission.pr_opened event,
// then the telegram entry delivers.
func TestDeliverGitHubDeliversBeforeMessageKinds(t *testing.T) {
	m := pushableMission(t)
	destStore := &fakeDestStore{rows: map[string]Destination{
		"tg1": {ID: "tg1", Name: "telegram-1", Kind: "telegram", Enabled: true, Config: json.RawMessage(`{"chat_id":"123"}`), CredentialRef: "tok"},
		"gh1": {ID: "gh1", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"conn1","mode":"push_pr"}`)},
	}}
	eventStore := &fakeEventStore{}
	p := &fakePusher{host: "github.com"}
	pr := &fakePRSource{repoExists: true, defaultBranch: "main", prURL: "https://github.com/octo/repo/pull/7", prNumber: 7}
	resolveToken := func(context.Context, string) (string, error) { return "tok", nil }
	github := &GitHubAdapter{Pusher: p, Events: eventStore, ResolveToken: resolveToken, PR: pr}
	telegram := &capturingAdapter{}
	d := &Deliverer{store: destStore, events: eventStore, adapters: map[string]Adapter{"telegram": telegram}, github: github, log: discardLog()}

	// telegram entry listed BEFORE the github entry.
	updated, err := d.Deliver(t.Context(), m, entries("tg1", "gh1"))
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	for _, e := range updated {
		if e.DeliveredAt == "" {
			t.Fatalf("entry %+v not delivered", e)
		}
	}
	found := false
	for _, link := range telegram.last.Links {
		if link == "https://github.com/octo/repo/pull/7" {
			found = true
		}
	}
	if !found {
		t.Fatalf("telegram payload links = %v, want the PR url from the github delivery that ran first", telegram.last.Links)
	}
}

func TestDeliverTest(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "webhook-1", Kind: "webhook", Enabled: true, Config: json.RawMessage(`{"url":"https://example.com","format":"json"}`)},
	}}
	okAdapter := &fakeAdapter{failCount: 0}
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{"webhook": okAdapter}, log: discardLog()}

	if err := d.Test(t.Context(), "d1"); err != nil {
		t.Fatalf("Test() error = %v, want nil", err)
	}
	if okAdapter.callCount() != 1 {
		t.Fatalf("expected exactly 1 call, got %d", okAdapter.callCount())
	}

	if err := d.Test(t.Context(), "unknown"); err == nil {
		t.Fatal("expected error for unknown destination")
	}
}

func TestDeliverTestGitHubUnsupported(t *testing.T) {
	destStore := &fakeDestStore{rows: map[string]Destination{
		"d1": {ID: "d1", Name: "gh-1", Kind: "github", Enabled: true, Config: json.RawMessage(`{"connector_id":"conn1","mode":"push"}`)},
	}}
	d := &Deliverer{store: destStore, adapters: map[string]Adapter{}, log: discardLog()}

	if err := d.Test(t.Context(), "d1"); err == nil {
		t.Fatal("expected github destinations to reject Test")
	}
	if _, _, err := d.DeliverNow(t.Context(), "d1", "s", "b"); err == nil {
		t.Fatal("expected github destinations to reject DeliverNow")
	}
}
