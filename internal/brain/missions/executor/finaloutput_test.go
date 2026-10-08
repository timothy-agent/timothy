package executor

import "testing"

// TestParseResultFinalOutput (D-134): every adapter decodes the
// optional final_output, and a result without it still parses.
func TestParseResultFinalOutput(t *testing.T) {
	adapters := map[string]interface{ ParseResult(Event) (Result, bool) }{
		"claude": claudeAdapter{}, "codex": codexAdapter{}, "cursor": cursorAdapter{},
		"opencode": opencodeAdapter{}, "pi": piAdapter{},
	}
	for name, a := range adapters {
		t.Run(name, func(t *testing.T) {
			res, ok := a.ParseResult(Event{Kind: KindResult, Result: []byte(`{"status":"DONE","note":"n","final_output":"## Report\nbody"}`)})
			if !ok || res.Status != "DONE" || res.Note != "n" || res.FinalOutput != "## Report\nbody" {
				t.Fatalf("ParseResult = %+v, %v; want DONE with the report", res, ok)
			}
			res, ok = a.ParseResult(Event{Kind: KindResult, Result: []byte(`{"status":"RETRY","note":"n"}`)})
			if !ok || res.Status != "RETRY" || res.FinalOutput != "" {
				t.Fatalf("ParseResult without final_output = %+v, %v", res, ok)
			}
		})
	}
}
