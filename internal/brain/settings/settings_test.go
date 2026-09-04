package settings

import (
	"context"
	"io"
	"log/slog"
	"testing"

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
