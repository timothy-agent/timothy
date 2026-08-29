// Package builtin holds the compiled-in tools the agent loop ships
// with. Each constructor returns a *tools.Tool; brain registers them
// at startup.
package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
	// Embed the IANA timezone database so LoadLocation works in
	// containers without a zoneinfo directory (distroless, alpine).
	_ "time/tzdata"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// Clock returns the current time; injectable for tests.
type Clock func() time.Time

// LocationFunc returns the operator's configured timezone (UTC when
// unset); injectable for tests. Matches settings.Store.Location.
type LocationFunc func(context.Context) *time.Location

type currentTimeArgs struct {
	Timezone string `json:"timezone"`
}

func CurrentTime(now Clock, defaultLoc LocationFunc) *tools.Tool {
	return &tools.Tool{
		Name: "get_current_time",
		Description: `Returns the current date and time.

Use when the answer depends on "now": today's date, the current time
somewhere, day of week, or anything relative to the present.

Arguments:
- timezone (string, optional): IANA timezone name, e.g. "Africa/Nairobi",
  "America/New_York", "UTC". Defaults to the configured operator
  timezone, UTC when unset. Abbreviations like "EST" or offsets like
  "+03:00" are NOT accepted, use the IANA name.

Returns the time in RFC 3339 format plus a human-readable line with
the weekday.

Example: {"timezone": "Asia/Dhaka"} →
"2026-07-11T09:15:00+06:00 (Saturday, 11 July 2026, 09:15 +06, Asia/Dhaka)"`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"timezone": {
					"type": "string",
					"description": "IANA timezone name (e.g. Africa/Nairobi). Defaults to the configured operator timezone, UTC when unset."
				}
			},
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args currentTimeArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			loc := time.UTC
			if defaultLoc != nil {
				if l := defaultLoc(ctx); l != nil {
					loc = l
				}
			}
			if args.Timezone != "" {
				l, err := time.LoadLocation(args.Timezone)
				if err != nil {
					return "", fmt.Errorf("unknown timezone %q: use an IANA name like Europe/Paris", args.Timezone)
				}
				loc = l
			}
			t := now().In(loc)
			return fmt.Sprintf("%s (%s, %s)",
				t.Format(time.RFC3339),
				t.Format("Monday, 2 January 2006, 15:04 -07"),
				loc.String(),
			), nil
		},
	}
}

type convertTimeArgs struct {
	Time       string `json:"time"`
	ToTimezone string `json:"to_timezone"`
}

func ConvertTime() *tools.Tool {
	return &tools.Tool{
		Name: "convert_time",
		Description: `Converts a timestamp to another timezone.

Use when the user gives a time in one zone and needs it in another
("3pm New York time in Dhaka?"). Resolve the source time to RFC 3339
with its offset first (use get_current_time if you need today's date).

Arguments:
- time (string, required): RFC 3339 timestamp including offset,
  e.g. "2026-07-11T15:00:00-04:00". Bare local times without an offset
  are rejected — the conversion would be ambiguous.
- to_timezone (string, required): IANA timezone name to convert into.

Returns the same instant expressed in the target zone, RFC 3339 plus a
human-readable line.

Example: {"time": "2026-07-11T15:00:00-04:00", "to_timezone": "Asia/Dhaka"} →
"2026-07-12T01:00:00+06:00 (Sunday, 12 July 2026, 01:00 +06, Asia/Dhaka)"`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"time": {
					"type": "string",
					"description": "RFC 3339 timestamp with offset, e.g. 2026-07-11T15:00:00-04:00"
				},
				"to_timezone": {
					"type": "string",
					"description": "Target IANA timezone name, e.g. Asia/Dhaka"
				}
			},
			"required": ["time", "to_timezone"],
			"additionalProperties": false
		}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args convertTimeArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			t, err := time.Parse(time.RFC3339, args.Time)
			if err != nil {
				return "", fmt.Errorf("time must be RFC 3339 with offset (e.g. 2026-07-11T15:00:00-04:00): %w", err)
			}
			loc, err := time.LoadLocation(args.ToTimezone)
			if err != nil {
				return "", fmt.Errorf("unknown timezone %q: use an IANA name like Europe/Paris", args.ToTimezone)
			}
			out := t.In(loc)
			return fmt.Sprintf("%s (%s, %s)",
				out.Format(time.RFC3339),
				out.Format("Monday, 2 January 2006, 15:04 -07"),
				loc.String(),
			), nil
		},
	}
}
