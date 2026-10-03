package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRememberStoresFact(t *testing.T) {
	t.Parallel()
	var gotContent, gotType string
	tool := Remember(func(_ context.Context, content, typ string) (string, string, error) {
		gotContent, gotType = content, typ
		return "mem-1", "active", nil
	})
	out, err := tool.Execute(context.Background(),
		json.RawMessage(`{"content":"User's birthday is 3 March.","type":"semantic"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotContent != "User's birthday is 3 March." || gotType != "semantic" {
		t.Fatalf("save got (%q, %q)", gotContent, gotType)
	}
	if !strings.Contains(out, "mem-1") {
		t.Fatalf("out = %q, want id echoed", out)
	}
}

func TestRememberReportsReviewAndRejectedStatuses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		id       string
		status   string
		wantText string
		wantErr  bool
	}{
		{name: "pending", id: "mem-2", status: "pending", wantText: "Awaiting memory review (id mem-2): a fact"},
		{name: "dropped", status: "dropped", wantText: "Not stored because this fact matches a previously rejected memory."},
		{name: "unknown", id: "mem-3", status: "archived", wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tool := Remember(func(_ context.Context, content, memoryType string) (string, string, error) {
				if content != "a fact" || memoryType != "semantic" {
					t.Fatalf("save args = (%q, %q)", content, memoryType)
				}
				return tc.id, tc.status, nil
			})
			out, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"a fact"}`))
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), `unknown status "archived"`) {
					t.Fatalf("Execute error = %v, want unknown status", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if out != tc.wantText {
				t.Fatalf("Execute = %q, want %q", out, tc.wantText)
			}
		})
	}
}

func TestRememberDefaultsToSemantic(t *testing.T) {
	t.Parallel()
	var gotType string
	tool := Remember(func(_ context.Context, _, typ string) (string, string, error) {
		gotType = typ
		return "id", "active", nil
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"a fact"}`)); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotType != "semantic" {
		t.Fatalf("type = %q, want semantic default", gotType)
	}
}

func TestRememberValidates(t *testing.T) {
	t.Parallel()
	tool := Remember(func(context.Context, string, string) (string, string, error) {
		t.Fatal("save must not run on invalid args")
		return "", "", nil
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"  "}`)); err == nil {
		t.Fatal("empty content accepted")
	}
}

func TestRememberSurfacesSaveError(t *testing.T) {
	t.Parallel()
	tool := Remember(func(context.Context, string, string) (string, string, error) {
		return "", "", errors.New("memoryd down")
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"x"}`)); err == nil ||
		!strings.Contains(err.Error(), "memoryd down") {
		t.Fatalf("err = %v", err)
	}
}
