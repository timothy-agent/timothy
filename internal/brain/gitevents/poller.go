// Package gitevents polls GitHub repos watched by connector_event
// triggers and writes new activity to the events inbox (issue #826).
// No inbound endpoint: conditional requests on the repo event feed plus
// a workflow runs query per watch.
package gitevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/SumonMSelim/timothy/internal/brain/automations"
	"github.com/SumonMSelim/timothy/internal/brain/connectors"
	"github.com/SumonMSelim/timothy/internal/brain/events"
)

const (
	// lockKey is the poller's advisory lock key ("GHEV").
	lockKey = 0x47484556
	// minInterval is the poll interval floor.
	minInterval = 30 * time.Second
	// defaultInterval applies when no interval func is wired.
	defaultInterval = 60 * time.Second
	// rateFloor backs a connector off once fewer requests remain.
	rateFloor = 50
	// rateSlack is added past GitHub's rate reset.
	rateSlack = 5 * time.Second
	// identityTTL is how long a connector's login stays cached.
	identityTTL = time.Hour
	// runsLookback keeps the workflow runs window this wide, so a run
	// that completes after a newer one was created is still seen.
	runsLookback = 2 * time.Hour
)

// Poller turns watched repos' GitHub activity into connector events.
type Poller struct {
	reader   func(ctx context.Context, connectorID string) (*connectors.GitHubEventReader, error)
	watches  func(ctx context.Context) ([]automations.Watch, error)
	cursors  *Store
	inbox    *events.Store
	enabled  func(ctx context.Context) bool
	interval func(ctx context.Context) time.Duration
	kick     func()
	polled   *prometheus.CounterVec // label kind; nil skips
	log      *slog.Logger

	identities map[string]identity
}

type identity struct {
	login     string
	fetchedAt time.Time
}

// NewPoller wires the poller. enabled is the automations switch (nil
// runs always), interval the configured poll interval (nil is 60 s),
// kick the drainer's Kick (nil skips) and polled a counter by kind
// (nil skips).
func NewPoller(reader func(ctx context.Context, connectorID string) (*connectors.GitHubEventReader, error), watches func(ctx context.Context) ([]automations.Watch, error),
	cursors *Store, inbox *events.Store, enabled func(ctx context.Context) bool, interval func(ctx context.Context) time.Duration, kick func(), polled *prometheus.CounterVec, log *slog.Logger) *Poller {
	return &Poller{reader: reader, watches: watches, cursors: cursors, inbox: inbox, enabled: enabled, interval: interval, kick: kick, polled: polled, log: log, identities: map[string]identity{}}
}

// Run polls once per interval until ctx is done.
func (p *Poller) Run(ctx context.Context) {
	timer := time.NewTimer(p.pollInterval(ctx))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := p.Tick(ctx, time.Now()); err != nil && ctx.Err() == nil {
				p.log.Error("gitevents: tick failed", "error", err)
			}
			timer.Reset(p.pollInterval(ctx))
		}
	}
}

func (p *Poller) pollInterval(ctx context.Context) time.Duration {
	d := defaultInterval
	if p.interval != nil {
		d = p.interval(ctx)
	}
	return clampInterval(d)
}

// clampInterval applies the poll interval floor.
func clampInterval(d time.Duration) time.Duration {
	return max(d, minInterval)
}

// pass is one Tick's per-connector state.
type pass struct {
	now       time.Time
	interval  time.Duration
	readers   map[string]readerResult
	backedOff map[string]bool
	inserted  int
}

type readerResult struct {
	reader *connectors.GitHubEventReader
	login  string
	err    error
}

// Tick polls every due watch at now under the poller's advisory lock,
// then deletes cursors no watch names. A per-watch failure is logged
// and never aborts the pass.
func (p *Poller) Tick(ctx context.Context, now time.Time) error {
	if p.enabled != nil && !p.enabled(ctx) {
		return nil
	}
	watches, err := p.watches(ctx)
	if err != nil {
		return fmt.Errorf("gitevents tick: %w", err)
	}
	db, err := p.cursors.db.Get()
	if err != nil {
		return fmt.Errorf("gitevents tick: %w", err)
	}
	// A session lock on one held connection: the pass makes network
	// calls, which an open transaction would sit idle across.
	conn, err := db.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("gitevents tick: acquire: %w", err)
	}
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, int64(lockKey)).Scan(&acquired); err != nil {
		conn.Release()
		return fmt.Errorf("gitevents tick: advisory lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil
	}
	defer func() {
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(uctx, `SELECT pg_advisory_unlock($1)`, int64(lockKey)); err != nil {
			// Closing the session is the only other way to drop the lock.
			_ = conn.Hijack().Close(uctx)
			return
		}
		conn.Release()
	}()

	ps := &pass{now: now, interval: p.pollInterval(ctx), readers: map[string]readerResult{}, backedOff: map[string]bool{}}
	for _, w := range watches {
		p.pollWatch(ctx, ps, w)
	}
	if n, err := p.cursors.Sweep(ctx, watches); err != nil {
		p.log.Warn("gitevents: cursor sweep failed", "error", err)
	} else if n > 0 {
		p.log.Info("gitevents: swept cursors without a watch", "deleted", n)
	}
	if ps.inserted > 0 && p.kick != nil {
		p.kick()
	}
	return nil
}

// pollWatch polls one watch when due and saves its cursor.
func (p *Poller) pollWatch(ctx context.Context, ps *pass, w automations.Watch) {
	owner, name, ok := strings.Cut(w.Repo, "/")
	if !ok {
		p.log.Warn("gitevents: watch repo is not owner/name, skipping", "connector_id", w.ConnectorID, "repo", w.Repo)
		return
	}
	if ps.backedOff[w.ConnectorID] {
		return
	}
	c, err := p.cursors.Load(ctx, w.ConnectorID, w.Repo, ps.now)
	if err != nil {
		p.log.Warn("gitevents: load cursor failed", "connector_id", w.ConnectorID, "repo", w.Repo, "error", err)
		return
	}
	if c.NextPollAt.After(ps.now) {
		return
	}
	rr := p.connector(ctx, ps, w.ConnectorID)
	if rr.err != nil {
		p.log.Warn("gitevents: connector unavailable", "connector_id", w.ConnectorID, "repo", w.Repo, "error", rr.err)
		c.NextPollAt = ps.now.Add(ps.interval)
		p.save(ctx, c)
		return
	}

	feed, etag, pollInterval, rate, err := rr.reader.RepoEvents(ctx, owner, name, c.ETag)
	if err != nil {
		c.NextPollAt = ps.now.Add(ps.interval)
		p.save(ctx, c)
		p.failed(ctx, ps, w, rate, err)
		return
	}
	var notBefore time.Time
	if c.ETag == "" && c.LastEventID == "" {
		notBefore = c.RunsSince // first sight: no backfill
	}
	payloads, newest := normalizeFeed(w.ConnectorID, w.Repo, rr.login, feed, c.LastEventID, notBefore)
	if p.insert(ctx, ps, payloads) {
		applyFeed(&c, etag, newest)
	}
	c.NextPollAt = nextPollAt(ps.now, ps.interval, pollInterval)

	runs, runsRate, err := rr.reader.WorkflowRuns(ctx, owner, name, c.RunsSince)
	if err != nil {
		p.save(ctx, c)
		p.failed(ctx, ps, w, runsRate, err)
		return
	}
	var runPayloads []events.ConnectorEventPayload
	for _, r := range runs {
		if pl, ok := normalizeRun(w.ConnectorID, w.Repo, rr.login, r); ok {
			runPayloads = append(runPayloads, pl)
		}
	}
	if p.insert(ctx, ps, runPayloads) {
		c.RunsSince = advanceRunsSince(c.RunsSince, ps.now)
	}
	p.save(ctx, c)
	p.log.Info("gitevents: polled", "connector_id", w.ConnectorID, "repo", w.Repo, "not_modified", feed == nil,
		"events", len(payloads), "runs", len(runPayloads), "rate_remaining", min(rate.Remaining, runsRate.Remaining))
	if lowRate(rate) || lowRate(runsRate) {
		worst := rate
		if lowRate(runsRate) {
			worst = runsRate
		}
		p.backoff(ctx, ps, w.ConnectorID, worst, "rate limit nearly exhausted")
	}
}

// connector returns the pass's reader and identity login for
// connectorID, refreshing the login after identityTTL.
func (p *Poller) connector(ctx context.Context, ps *pass, connectorID string) readerResult {
	if rr, ok := ps.readers[connectorID]; ok {
		return rr
	}
	var rr readerResult
	rr.reader, rr.err = p.reader(ctx, connectorID)
	if rr.err == nil {
		id, cached := p.identities[connectorID]
		if !cached || ps.now.Sub(id.fetchedAt) >= identityTTL {
			ident, err := rr.reader.Identity(ctx)
			switch {
			case err != nil:
				// Without a login the self guard cannot hold: skip.
				rr.err = fmt.Errorf("identity: %w", err)
			case ident.Login == "":
				rr.err = errors.New("identity: empty login")
			default:
				id = identity{login: ident.Login, fetchedAt: ps.now}
				p.identities[connectorID] = id
			}
		}
		rr.login = id.login
	}
	ps.readers[connectorID] = rr
	return rr
}

// insert adds each payload's event; ok is false when any insert failed,
// so the caller keeps its cursor and the next poll retries.
func (p *Poller) insert(ctx context.Context, ps *pass, payloads []events.ConnectorEventPayload) (ok bool) {
	ok = true
	for _, pl := range payloads {
		ev, err := events.ConnectorEvent(pl)
		if err != nil {
			p.log.Warn("gitevents: build event failed", "kind", pl.Kind, "provider_event_id", pl.ProviderEventID, "error", err)
			continue
		}
		_, inserted, err := p.inbox.AddIfNew(ctx, ev)
		if err != nil {
			p.log.Warn("gitevents: insert event failed", "kind", pl.Kind, "provider_event_id", pl.ProviderEventID, "error", err)
			ok = false
			continue
		}
		if inserted {
			ps.inserted++
			if p.polled != nil {
				p.polled.WithLabelValues(pl.Kind).Inc()
			}
		}
	}
	return ok
}

func (p *Poller) save(ctx context.Context, c Cursor) {
	if err := p.cursors.Save(ctx, c); err != nil {
		p.log.Warn("gitevents: save cursor failed", "connector_id", c.ConnectorID, "repo", c.Repo, "error", err)
	}
}

// failed logs a request error, backing the connector off when GitHub
// rate limited it.
func (p *Poller) failed(ctx context.Context, ps *pass, w automations.Watch, rate connectors.GitHubRate, err error) {
	if errors.Is(err, connectors.ErrGitHubRateLimited) {
		p.backoff(ctx, ps, w.ConnectorID, rate, err.Error())
		return
	}
	p.log.Warn("gitevents: poll failed", "connector_id", w.ConnectorID, "repo", w.Repo, "error", err)
}

// backoff pushes every watch of connectorID past the rate reset and
// skips it for the rest of the pass, logging once.
func (p *Poller) backoff(ctx context.Context, ps *pass, connectorID string, rate connectors.GitHubRate, reason string) {
	until := backoffUntil(ps.now, ps.interval, rate)
	if err := p.cursors.Backoff(ctx, connectorID, until); err != nil {
		p.log.Warn("gitevents: backoff failed", "connector_id", connectorID, "error", err)
	}
	if !ps.backedOff[connectorID] {
		p.log.Warn("gitevents: backing off connector", "connector_id", connectorID, "until", until, "rate_remaining", rate.Remaining, "reason", reason)
	}
	ps.backedOff[connectorID] = true
}

// applyFeed records a successful feed read: the new etag (a 304 keeps
// the old one) and the newest event id seen.
func applyFeed(c *Cursor, etag, newest string) {
	if etag != "" {
		c.ETag = etag
	}
	if newest != "" {
		c.LastEventID = newest
	}
}

// nextPollAt is now plus the interval, never sooner than GitHub's
// X-Poll-Interval.
func nextPollAt(now time.Time, interval, pollInterval time.Duration) time.Time {
	return now.Add(max(interval, pollInterval))
}

// lowRate reports a known remaining count under rateFloor.
func lowRate(r connectors.GitHubRate) bool {
	return r.Remaining >= 0 && r.Remaining < rateFloor
}

// backoffUntil is rateSlack past the rate reset, or one interval from
// now when the reset is unknown or past.
func backoffUntil(now time.Time, interval time.Duration, r connectors.GitHubRate) time.Time {
	if r.Reset.After(now) {
		return r.Reset.Add(rateSlack)
	}
	return now.Add(interval)
}

// advanceRunsSince slides the runs window forward, keeping runsLookback
// of history and never moving before its current start.
func advanceRunsSince(since, now time.Time) time.Time {
	if floor := now.Add(-runsLookback); floor.After(since) {
		return floor
	}
	return since
}
