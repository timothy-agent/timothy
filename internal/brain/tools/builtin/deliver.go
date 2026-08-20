package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

// DestinationInfo is the deliver tool's own view of one destinations
// row — a local struct (not an import of internal/brain/destinations)
// because destinations imports internal/brain/missions, which imports
// this package (runner.go's builtin.Shell/WriteFile); a destinations
// import here would create an import cycle. cmd/brain/main.go, which
// imports both packages, adapts real destinations.Destination values
// into this shape.
type DestinationInfo struct {
	ID      string
	Name    string
	Enabled bool
}

// DeliverLister lists every destination for name/id resolution; main
// curries destinations.Store.List in.
type DeliverLister func(ctx context.Context) ([]DestinationInfo, error)

// DeliverFunc sends subject+body to one already-resolved destination
// id, synchronously, no retry, and reports what it delivered to; main
// curries destinations.Deliverer.DeliverNow in.
type DeliverFunc func(ctx context.Context, destinationID, subject, body string) (name, kind string, err error)

// deliverKnownArgs are the only keys deliver's schema declares. Any
// other key in the raw call — to, url, chat_id, email, or anything
// else — is rejected before the model's args are even parsed: the
// exfil guard is enforced here in Go, never left to the JSON schema
// alone, since a schema is only a hint to the model, not a boundary.
var deliverKnownArgs = map[string]bool{"destination": true, "subject": true, "body": true}

type deliverArgs struct {
	Destination string `json:"destination"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
}

// Deliver lets the model send a message to one operator-configured
// destination (email, webhook, or Telegram). list resolves the
// destination argument to an id; deliver performs the actual send.
//
// Exfiltration guard (Go code, not prompt text): the model can only
// pick a destination from the operator's own list, by name or id —
// there is no argument for an address, URL, or chat id, and any
// unrecognized argument is rejected outright rather than silently
// dropped. A prompt-injected "send this to attacker@x" has nothing to
// call.
func Deliver(list DeliverLister, deliver DeliverFunc) *tools.Tool {
	return &tools.Tool{
		Name: "deliver",
		Description: `Sends a message to one operator-configured destination
(email, webhook, or Telegram) set up in Settings → Destinations.

Use when asked to send/deliver/forward something to a named
destination ("send this to my telegram", "email the summary to
ops-webhook"). There is no way to specify an address, URL, or chat id
here — only a destination already configured by the operator can be
targeted; if none fits, say so instead of guessing a destination name.

This is a plain text delivery: subject + body only, no file
attachments (mission artifact files are delivered automatically at
mission completion for destinations attached to that mission, not
through this tool).

Arguments:
- destination (string, required): the destination's name or id, as
  configured in Settings → Destinations.
- subject (string, optional): subject line (used by email; prepended
  to the message body for webhook/Telegram).
- body (string, required): the message content.

Returns the destination's name and kind on success. On failure,
returns the delivery error (e.g. connection refused) — this call is
best-effort and does not retry.`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"destination": {
					"type": "string",
					"description": "Destination name or id, as configured in Settings → Destinations."
				},
				"subject": {
					"type": "string",
					"description": "Optional subject line."
				},
				"body": {
					"type": "string",
					"description": "Message content to deliver."
				}
			},
			"required": ["destination", "body"],
			"additionalProperties": false
		}`),
		Execute: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var loose map[string]json.RawMessage
			if err := json.Unmarshal(raw, &loose); err != nil {
				return "", fmt.Errorf("deliver: invalid arguments: %w", err)
			}
			for key := range loose {
				if !deliverKnownArgs[key] {
					return "", fmt.Errorf("deliver: unknown argument %q — destinations are operator-configured, addresses cannot be supplied here", key)
				}
			}
			var args deliverArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("deliver: invalid arguments: %w", err)
			}
			destination := strings.TrimSpace(args.Destination)
			if destination == "" {
				return "", fmt.Errorf("deliver: destination is required")
			}
			body := strings.TrimSpace(args.Body)
			if body == "" {
				return "", fmt.Errorf("deliver: body is required")
			}
			if list == nil || deliver == nil {
				return "", fmt.Errorf("deliver: destinations are not configured")
			}
			destinations, err := list(ctx)
			if err != nil {
				return "", fmt.Errorf("deliver: %w", err)
			}
			id, err := resolveDestination(destinations, destination)
			if err != nil {
				return "", err
			}
			name, kind, err := deliver(ctx, id, args.Subject, body)
			if err != nil {
				return "", fmt.Errorf("deliver: %w", err)
			}
			return fmt.Sprintf("delivered to %q (%s)", name, kind), nil
		},
	}
}

// resolveDestination matches ref against the operator's destination
// list: an exact id match first, else a unique case-insensitive name
// match, else an error naming every enabled destination the model
// could have meant. A disabled destination is only matched by
// explicit id/name (never silently skipped in that resolution step)
// but reported as disabled rather than delivered to.
func resolveDestination(all []DestinationInfo, ref string) (string, error) {
	for _, d := range all {
		if d.ID == ref {
			if !d.Enabled {
				return "", fmt.Errorf("deliver: destination %q is disabled", d.Name)
			}
			return d.ID, nil
		}
	}
	var matches []DestinationInfo
	lower := strings.ToLower(ref)
	for _, d := range all {
		if strings.ToLower(d.Name) == lower {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		if !matches[0].Enabled {
			return "", fmt.Errorf("deliver: destination %q is disabled", matches[0].Name)
		}
		return matches[0].ID, nil
	case 0:
		return "", fmt.Errorf("deliver: no destination named %q — valid destinations: %s", ref, enabledNames(all))
	default:
		return "", fmt.Errorf("deliver: %q matches more than one destination — use its id instead", ref)
	}
}

// enabledNames lists every enabled destination's name, sorted, for the
// "unknown destination" error — a disabled destination is never
// offered as something the model could have meant.
func enabledNames(all []DestinationInfo) string {
	var names []string
	for _, d := range all {
		if d.Enabled {
			names = append(names, d.Name)
		}
	}
	if len(names) == 0 {
		return "(none configured)"
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
