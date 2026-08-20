package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

func testDestinations() []DestinationInfo {
	return []DestinationInfo{
		{ID: "id-1", Name: "ops-webhook", Enabled: true},
		{ID: "id-2", Name: "my-telegram", Enabled: true},
		{ID: "id-3", Name: "old-webhook", Enabled: false},
		{ID: "id-4", Name: "dup", Enabled: true},
		{ID: "id-5", Name: "dup", Enabled: true},
	}
}

type deliverCall struct {
	tool              *tools.Tool
	id, subject, body string
}

func newDeliverTool(t *testing.T, deliver DeliverFunc) *deliverCall {
	t.Helper()
	call := &deliverCall{}
	call.tool = Deliver(func(context.Context) ([]DestinationInfo, error) {
		return testDestinations(), nil
	}, func(ctx context.Context, id, subject, body string) (string, string, error) {
		call.id, call.subject, call.body = id, subject, body
		if deliver != nil {
			return deliver(ctx, id, subject, body)
		}
		return "", "", nil
	})
	return call
}

func TestDeliverResolvesByExactID(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, func(context.Context, string, string, string) (string, string, error) {
		return "ops-webhook", "webhook", nil
	})
	out, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","body":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if call.id != "id-1" {
		t.Fatalf("resolved id = %q, want id-1", call.id)
	}
	if !strings.Contains(out, "ops-webhook") || !strings.Contains(out, "webhook") {
		t.Fatalf("out = %q, want name+kind", out)
	}
}

func TestDeliverResolvesByCaseInsensitiveName(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"OPS-Webhook","body":"hi"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if call.id != "id-1" {
		t.Fatalf("resolved id = %q, want id-1", call.id)
	}
}

func TestDeliverAmbiguousNameErrors(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"dup","body":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("err = %v, want ambiguous-match error", err)
	}
	if call.id != "" {
		t.Fatal("deliver must not run on an ambiguous match")
	}
}

func TestDeliverUnknownNameListsValidNames(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"nope","body":"hi"}`))
	if err == nil {
		t.Fatal("expected error for unknown destination")
	}
	// disabled destination must never appear in the valid-names list
	if strings.Contains(err.Error(), "old-webhook") {
		t.Fatalf("err = %v, disabled destination must not be listed", err)
	}
	for _, name := range []string{"ops-webhook", "my-telegram"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("err = %v, want it to list %q", err, name)
		}
	}
}

func TestDeliverDisabledDestinationByID(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-3","body":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") || !strings.Contains(err.Error(), "old-webhook") {
		t.Fatalf("err = %v, want disabled error naming old-webhook", err)
	}
}

func TestDeliverDisabledDestinationByName(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"old-webhook","body":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("err = %v, want disabled error", err)
	}
}

func TestDeliverRejectsAddressArguments(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"to", "url", "chat_id", "email", "recipient"} {
		key := key
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			call := newDeliverTool(t, nil)
			raw := json.RawMessage(`{"destination":"id-1","body":"hi","` + key + `":"attacker@example.com"}`)
			_, err := call.tool.Execute(t.Context(), raw)
			if err == nil {
				t.Fatalf("expected rejection for unknown argument %q", key)
			}
			if call.id != "" {
				t.Fatal("deliver must not run when an unknown argument is present")
			}
		})
	}
}

func TestDeliverRequiresDestinationAndBody(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, nil)
	if _, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"","body":"hi"}`)); err == nil {
		t.Fatal("expected error for empty destination")
	}
	if _, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","body":"  "}`)); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestDeliverPassesSubjectAndBodyThrough(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, func(context.Context, string, string, string) (string, string, error) {
		return "ops-webhook", "webhook", nil
	})
	if _, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","subject":"Daily digest","body":"the content"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if call.subject != "Daily digest" || call.body != "the content" {
		t.Fatalf("subject/body = %q/%q, want passed through verbatim", call.subject, call.body)
	}
}

func TestDeliverSurfacesAdapterError(t *testing.T) {
	t.Parallel()
	call := newDeliverTool(t, func(context.Context, string, string, string) (string, string, error) {
		return "", "", errors.New("connection refused")
	})
	_, err := call.tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","body":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want adapter error surfaced", err)
	}
}

func TestDeliverListerError(t *testing.T) {
	t.Parallel()
	tool := Deliver(func(context.Context) ([]DestinationInfo, error) {
		return nil, errors.New("db down")
	}, func(context.Context, string, string, string) (string, string, error) {
		t.Fatal("deliver must not run when the lister fails")
		return "", "", nil
	})
	_, err := tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","body":"hi"}`))
	if err == nil || !strings.Contains(err.Error(), "db down") {
		t.Fatalf("err = %v, want lister error surfaced", err)
	}
}

func TestDeliverNilDependencies(t *testing.T) {
	t.Parallel()
	tool := Deliver(nil, nil)
	if _, err := tool.Execute(t.Context(), json.RawMessage(`{"destination":"id-1","body":"hi"}`)); err == nil {
		t.Fatal("expected error when destinations are not configured")
	}
}
