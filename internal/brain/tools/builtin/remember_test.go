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
	tool := Remember(func(_ context.Context, content, typ string) (string, error) {
		gotContent, gotType = content, typ
		return "mem-1", nil
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

func TestRememberDefaultsToSemantic(t *testing.T) {
	t.Parallel()
	var gotType string
	tool := Remember(func(_ context.Context, _, typ string) (string, error) {
		gotType = typ
		return "id", nil
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
	tool := Remember(func(context.Context, string, string) (string, error) {
		t.Fatal("save must not run on invalid args")
		return "", nil
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"  "}`)); err == nil {
		t.Fatal("empty content accepted")
	}
}

func TestRememberSurfacesSaveError(t *testing.T) {
	t.Parallel()
	tool := Remember(func(context.Context, string, string) (string, error) {
		return "", errors.New("memoryd down")
	})
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{"content":"x"}`)); err == nil ||
		!strings.Contains(err.Error(), "memoryd down") {
		t.Fatalf("err = %v", err)
	}
}
