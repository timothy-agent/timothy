package settings

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SumonMSelim/timothy/internal/platform/pgpool"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// degradedStore returns a Store backed by a pool that never connects
// (empty DSN), exercising the same default-map path a database outage
// takes.
func degradedStore(t *testing.T) *Store {
	t.Helper()
	pool := pgpool.New(t.Context(), "", discardLog())
	return New(pool, discardLog())
}

// TestOutboundHostsDefaultsToNone pins the fail-closed default of
// issue #431's allowlist: absent row, no internal host is reachable.
func TestOutboundHostsDefaultsToNone(t *testing.T) {
	s := degradedStore(t)
	if got := s.OutboundHosts(context.Background()); len(got) != 0 {
		t.Fatalf("OutboundHosts() = %v, want none by default", got)
	}
	if !knownValueKeys[ValueOutboundHostAllowlist] {
		t.Fatal("ValueOutboundHostAllowlist missing from knownValueKeys")
	}
}

// TestKBImageCaptioningDefaultsOff confirms the one knownKeysOff
// switch defaults to false for an absent row / degraded database,
// unlike every other known switch which defaults to true: enabling
// real gateway spend must be an explicit opt-in.
func TestKBImageCaptioningDefaultsOff(t *testing.T) {
	s := degradedStore(t)
	if s.Enabled(context.Background(), KeyKBImageCaptioning) {
		t.Fatal("KeyKBImageCaptioning = true, want false by default")
	}
}

// TestOtherSwitchesDefaultOn pins the existing default-true behavior
// for a knownKeys member outside knownKeysOff, guarding against the
// new knownKeysOff map accidentally flipping every switch.
func TestOtherSwitchesDefaultOn(t *testing.T) {
	s := degradedStore(t)
	if !s.Enabled(context.Background(), KeyTools) {
		t.Fatal("KeyTools = false, want true by default")
	}
}

// TestKBLocalOCRDefaultsOn pins the local-OCR fallback as a known key
// that defaults ON despite sitting next to the default-off captioning
// switch: the ocr sidecar is local and free, so there is no spend to
// opt into.
func TestKBLocalOCRDefaultsOn(t *testing.T) {
	s := degradedStore(t)
	if !s.Enabled(context.Background(), KeyKBLocalOCR) {
		t.Fatal("KeyKBLocalOCR = false, want true by default")
	}
	if !s.All(context.Background())[KeyKBLocalOCR] {
		t.Fatal("All()[KeyKBLocalOCR] = false, want true by default")
	}
	if !knownKeys[KeyKBLocalOCR] {
		t.Fatal("KeyKBLocalOCR missing from knownKeys")
	}
	if knownKeysOff[KeyKBLocalOCR] {
		t.Fatal("KeyKBLocalOCR in knownKeysOff, want default-on")
	}
}

// TestUnknownKeyDefaultsOn confirms an unrecognized key still defaults
// to true, matching Enabled's documented behavior for unknown keys.
func TestUnknownKeyDefaultsOn(t *testing.T) {
	s := degradedStore(t)
	if !s.Enabled(context.Background(), "not_a_real_key") {
		t.Fatal("unknown key = false, want true by default")
	}
}

// TestAllAppliesKnownKeysOffDefault confirms All()'s map (not just
// Enabled's fallback) reflects the false default for the degraded/
// absent-row case, since callers like the settings API read All()
// directly.
func TestAllAppliesKnownKeysOffDefault(t *testing.T) {
	s := degradedStore(t)
	all := s.All(context.Background())
	if all[KeyKBImageCaptioning] {
		t.Fatal("All()[KeyKBImageCaptioning] = true, want false by default")
	}
}

// TestMCPToolIndexThresholdDefault pins the accessor's fallback: an
// absent row (or a degraded database) means the built-in threshold,
// not deferral-off, so a large MCP server is indexed out of the box.
func TestMCPToolIndexThresholdDefault(t *testing.T) {
	s := degradedStore(t)
	if got := s.MCPToolIndexThreshold(context.Background()); got != DefaultMCPToolIndexThreshold {
		t.Fatalf("MCPToolIndexThreshold = %d, want %d", got, DefaultMCPToolIndexThreshold)
	}
}

// TestWritingSettingsDefaultEmpty pins both writing settings as
// opt-in: an absent row (or a degraded database) means no writing-style
// block and no extra search_kb boost.
func TestWritingSettingsDefaultEmpty(t *testing.T) {
	s := degradedStore(t)
	if got := s.WritingStyle(context.Background()); got != "" {
		t.Fatalf("WritingStyle = %q, want empty", got)
	}
	if got := s.WritingSamplesCollection(context.Background()); got != "" {
		t.Fatalf("WritingSamplesCollection = %q, want empty", got)
	}
}

// TestExecutorKnobDefaults pins the issue #720 accessors: an absent row
// means the built-in review turn cap and no thinking budget at all.
func TestExecutorKnobDefaults(t *testing.T) {
	s := degradedStore(t)
	if got := s.ExecutorReviewMaxTurns(context.Background()); got != DefaultExecutorReviewMaxTurns {
		t.Fatalf("ExecutorReviewMaxTurns = %d, want %d", got, DefaultExecutorReviewMaxTurns)
	}
	if got := s.ExecutorThinkingTokens(context.Background()); got != 0 {
		t.Fatalf("ExecutorThinkingTokens = %d, want 0", got)
	}
}

// TestMissionCeilingDefaults pins the issue #718 accessors: with no
// row set, every mission retry ceiling reads its built-in default.
func TestMissionCeilingDefaults(t *testing.T) {
	s := degradedStore(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		got  int
		want int
	}{
		{ValueExecutorWorkerMaxTurns, s.ExecutorWorkerMaxTurns(ctx), DefaultExecutorWorkerMaxTurns},
		{ValueMissionDefaultMaxIterations, s.MissionDefaultMaxIterations(ctx), DefaultMissionMaxIterations},
		{ValueMissionBackoffFailures, s.MissionBackoffFailures(ctx), DefaultMissionBackoffFailures},
		{ValueMissionStallRounds, s.MissionStallRounds(ctx), DefaultMissionStallRounds},
		{ValueMissionHarnessRetryCap, s.MissionHarnessRetryCap(ctx), DefaultMissionHarnessRetryCap},
		{ValueMissionAutoResumeBackoffMax, s.MissionAutoResumeBackoffMax(ctx), DefaultMissionAutoResumeBackoffMax},
		{ValueMissionAutoResumeInfraMax, s.MissionAutoResumeInfraMax(ctx), DefaultMissionAutoResumeInfraMax},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
		}
		if !knownValueKeys[tc.name] {
			t.Errorf("%s is not in knownValueKeys, so it cannot be set", tc.name)
		}
		if !nonNegativeIntKeys[tc.name] {
			t.Errorf("%s is not validated as a non-negative integer", tc.name)
		}
	}
}

// TestGitHubPollIntervalDefault pins the poller's 60 s default for an
// absent row and the key's registration as a non-negative integer.
func TestGitHubPollIntervalDefault(t *testing.T) {
	s := degradedStore(t)
	if got := s.GitHubPollInterval(context.Background()); got != DefaultGitHubPollSeconds*time.Second {
		t.Fatalf("GitHubPollInterval() = %v, want %ds", got, DefaultGitHubPollSeconds)
	}
	if !knownValueKeys[ValueGitHubPollSeconds] || !nonNegativeIntKeys[ValueGitHubPollSeconds] {
		t.Fatal("ValueGitHubPollSeconds missing from knownValueKeys or nonNegativeIntKeys")
	}
}

// TestEmailPollIntervalFloor pins the email channel's 60 s default and
// 30 s floor, and the key's registration as a non-negative integer.
func TestEmailPollIntervalFloor(t *testing.T) {
	s := degradedStore(t)
	if got := s.EmailPollInterval(context.Background()); got != DefaultEmailPollSeconds*time.Second {
		t.Fatalf("EmailPollInterval() = %v, want %ds", got, DefaultEmailPollSeconds)
	}
	for v, want := range map[string]time.Duration{"0": 60 * time.Second, "5": 30 * time.Second, "30": 30 * time.Second, "90": 90 * time.Second, "junk": 60 * time.Second} {
		s.mu.Lock()
		s.flags, s.values, s.fetched = map[string]bool{}, map[string]string{ValueEmailPollSeconds: v}, time.Now()
		s.mu.Unlock()
		if got := s.EmailPollInterval(context.Background()); got != want {
			t.Errorf("EmailPollInterval(%q) = %v, want %v", v, got, want)
		}
	}
	if !knownValueKeys[ValueEmailPollSeconds] || !nonNegativeIntKeys[ValueEmailPollSeconds] {
		t.Fatal("ValueEmailPollSeconds missing from knownValueKeys or nonNegativeIntKeys")
	}
}

// TestJSONSettingsRejectUnknownKeys pins the JSON key class: only
// registered keys read or write, and a bool or string key is not one.
func TestJSONSettingsRejectUnknownKeys(t *testing.T) {
	s := degradedStore(t)
	ctx := context.Background()
	for _, key := range []string{"not_a_real_key", KeyTools, ValueTimezone} {
		var dst map[string]any
		if _, err := s.JSON(ctx, key, &dst); err == nil || !strings.Contains(err.Error(), "unknown setting") {
			t.Errorf("JSON(%q) err = %v, want unknown setting", key, err)
		}
		if err := s.SetJSON(ctx, key, map[string]any{}); err == nil || !strings.Contains(err.Error(), "unknown setting") {
			t.Errorf("SetJSON(%q) err = %v, want unknown setting", key, err)
		}
	}
}

// TestJSONSettingsDegradedReturnsError confirms a database outage is an
// error for JSON keys, not a silent default.
func TestJSONSettingsDegradedReturnsError(t *testing.T) {
	s := degradedStore(t)
	var dst map[string]any
	found, err := s.JSON(context.Background(), KeyOnboarding, &dst)
	if err == nil || found {
		t.Fatalf("JSON(degraded) = %v, %v; want error", found, err)
	}
	if err := s.SetJSON(context.Background(), KeyOnboarding, map[string]any{}); err == nil {
		t.Fatal("SetJSON(degraded) err = nil, want error")
	}
}

// TestMailCeilingValidation pins D-151's save-time validation: any
// integer >= 0 passes (and then fails only on the degraded write),
// with no upper bound; anything else is refused.
func TestMailCeilingValidation(t *testing.T) {
	cases := []struct {
		key, value, wantErr string
	}{
		{ValueMailMaxRecipientsPerSend, "5", "settings:"},
		{ValueMailMaxRecipientsPerSend, "0", "settings:"},
		{ValueMailMaxRecipientsPerSend, "100000", "settings:"},
		{ValueMailMaxRecipientsPerSend, "-1", "non-negative integer"},
		{ValueMailMaxRecipientsPerSend, "five", "non-negative integer"},
		{ValueMailMaxSendsPerDay, "50", "settings:"},
		{ValueMailMaxSendsPerDay, "0", "settings:"},
		{ValueMailMaxSendsPerDay, "1000000", "settings:"},
		{ValueMailMaxSendsPerDay, "-3", "non-negative integer"},
		{ValueMailMaxSendsPerDay, "1.5", "non-negative integer"},
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			err := degradedStore(t).SetValue(context.Background(), tc.key, tc.value)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("SetValue(%s, %q) err = %v, want containing %q", tc.key, tc.value, err, tc.wantErr)
			}
		})
	}
}

// TestMailCeilingReads pins the read side: unset is the default, 0
// stays 0 (ceiling off), large values pass through, junk falls back.
func TestMailCeilingReads(t *testing.T) {
	cases := []struct {
		name                      string
		recipients, daily         string
		wantRecipients, wantDaily int
	}{
		{"unset", "", "", 5, 50},
		{"lowered", "2", "10", 2, 10},
		{"off", "0", "0", 0, 0},
		{"large", "200", "5000", 200, 5000},
		{"junk falls back", "x", "-1", 5, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := degradedStore(t)
			s.mu.Lock()
			s.flags, s.fetched = map[string]bool{}, time.Now()
			s.values = map[string]string{ValueMailMaxRecipientsPerSend: tc.recipients, ValueMailMaxSendsPerDay: tc.daily}
			s.mu.Unlock()
			ctx := context.Background()
			if got := s.MailMaxRecipientsPerSend(ctx); got != tc.wantRecipients {
				t.Errorf("MailMaxRecipientsPerSend() = %d, want %d", got, tc.wantRecipients)
			}
			if got := s.MailMaxSendsPerDay(ctx); got != tc.wantDaily {
				t.Errorf("MailMaxSendsPerDay() = %d, want %d", got, tc.wantDaily)
			}
		})
	}
	if !knownValueKeys[ValueMailMaxRecipientsPerSend] || !nonNegativeIntKeys[ValueMailMaxSendsPerDay] {
		t.Fatal("mail ceiling keys missing from knownValueKeys or nonNegativeIntKeys")
	}
}
