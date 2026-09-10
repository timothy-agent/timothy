package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestSearchMemory(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		hits    []SearchMemoryHit
		recErr  error
		want    string
		wantErr string
	}{
		{
			name: "returns numbered memories with tier",
			args: `{"query":"preferred language"}`,
			hits: []SearchMemoryHit{
				{Type: "semantic", Content: "Prefers Go for every new project.", Score: 0.9},
				{Type: "procedural", Content: "Runs the stack with docker compose.", Score: 0.4},
			},
			want: "1. [semantic] Prefers Go for every new project.\n2. [procedural] Runs the stack with docker compose.",
		},
		{
			name: "zero matches is a normal answer",
			args: `{"query":"favourite colour"}`,
			hits: nil,
			want: "nothing recorded for that query",
		},
		{
			name: "memory without a tier still renders",
			args: `{"query":"anything"}`,
			hits: []SearchMemoryHit{{Content: "Bare fact."}},
			want: "1. Bare fact.",
		},
		{
			name: "limit truncates the returned set",
			args: `{"query":"anything","limit":1}`,
			hits: []SearchMemoryHit{{Content: "First."}, {Content: "Second."}},
			want: "1. First.",
		},
		{
			name:    "limit above the maximum rejected",
			args:    `{"query":"anything","limit":21}`,
			wantErr: "limit must be between 1 and 20",
		},
		{
			name:    "limit below one rejected",
			args:    `{"query":"anything","limit":0}`,
			wantErr: "limit must be between 1 and 20",
		},
		{
			name:    "empty query rejected",
			args:    `{"query":"   "}`,
			wantErr: "query must not be empty",
		},
		{
			name:    "invalid json rejected",
			args:    `{"query":`,
			wantErr: "invalid arguments",
		},
		{
			name:    "backend error surfaces",
			args:    `{"query":"stack"}`,
			recErr:  errors.New("memoryd unreachable"),
			wantErr: "memoryd unreachable",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotQuery string
			var gotLimit int
			tool := SearchMemory(func(_ context.Context, query string, limit int) ([]SearchMemoryHit, error) {
				gotQuery = query
				gotLimit = limit
				return tc.hits, tc.recErr
			})
			if tool.Name != "search_memory" {
				t.Fatalf("tool name = %q, want search_memory", tool.Name)
			}
			out, err := tool.Execute(context.Background(), json.RawMessage(tc.args))
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if out != tc.want {
				t.Fatalf("output =\n%q\nwant\n%q", out, tc.want)
			}
			if gotQuery == "" {
				t.Fatal("query was not passed through to the backend")
			}
			if gotLimit < 1 {
				t.Fatalf("limit passed to the backend = %d, want at least 1", gotLimit)
			}
		})
	}
}

func TestSearchMemorySchemaValid(t *testing.T) {
	tool := SearchMemory(func(context.Context, string, int) ([]SearchMemoryHit, error) { return nil, nil })
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("input schema is not valid json: %v", err)
	}
	req, ok := schema["required"].([]any)
	if !ok || len(req) != 1 || req[0] != "query" {
		t.Fatalf("required = %v, want [query]", schema["required"])
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties = %v, want an object", schema["properties"])
	}
	limit, ok := props["limit"].(map[string]any)
	if !ok {
		t.Fatal("schema has no limit property")
	}
	if limit["maximum"] != float64(searchMemoryMaxLimit) {
		t.Fatalf("limit maximum = %v, want %d", limit["maximum"], searchMemoryMaxLimit)
	}
}
