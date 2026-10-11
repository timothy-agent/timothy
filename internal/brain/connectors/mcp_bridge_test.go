package connectors

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SumonMSelim/timothy/internal/mcpbridge"
)

// serveStdio runs f as a stdio MCP server: each newline-delimited
// request goes through f's HTTP handler and any response body is
// written back as one line.
func serveStdio(f *fakeMCP, in io.Reader, out io.Writer) {
	sc := bufio.NewScanner(in)
	for sc.Scan() {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(sc.Bytes()))
		req.Header.Set("Mcp-Session-Id", f.sessionID)
		rec := httptest.NewRecorder()
		f.handler()(rec, req)
		if rec.Body.Len() > 0 {
			_, _ = out.Write(append(rec.Body.Bytes(), '\n'))
		}
	}
}

// buildMCPViaBridge builds an mcpSource against f served over stdio
// behind mcpbridge (D-161).
func buildMCPViaBridge(t *testing.T, f *fakeMCP) Source {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go serveStdio(f, inR, outW)
	b := mcpbridge.New("bridge-token", inW, outR, io.Discard)
	srv := httptest.NewServer(b)
	t.Cleanup(func() {
		srv.Close()
		_ = inW.Close()
		_ = outW.Close()
	})
	//nolint:gosec // G101: CredentialRef is a ref NAME, not a credential value.
	src, err := MCPBuilder(srv.Client(), MCPDeferral{})(t.Context(), Connector{
		Name: "toy", Kind: "mcp",
		Config:        json.RawMessage(`{"endpoint":"` + srv.URL + `/mcp"}`),
		CredentialRef: "TOY_BRIDGE_TOKEN",
	}, func(context.Context, string) (string, error) { return "bridge-token", nil })
	if err != nil {
		t.Fatalf("build via bridge: %v", err)
	}
	return src
}

// TestMCPThroughStdioBridge pins #1207's acceptance: the real client's
// initialize, tools/list and tools/call through the bridge give the
// same results as against the server directly.
func TestMCPThroughStdioBridge(t *testing.T) {
	t.Parallel()
	direct := buildMCP(t, &fakeMCP{}, "")
	bridged := buildMCPViaBridge(t, &fakeMCP{})

	want, got := direct.Tools(), bridged.Tools()
	if len(got) != len(want) {
		t.Fatalf("tools via bridge = %d, direct = %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Description != want[i].Description || string(got[i].InputSchema) != string(want[i].InputSchema) {
			t.Fatalf("tool %d via bridge = %+v, direct = %+v", i, got[i], want[i])
		}
	}
	if d, b := direct.(*mcpSource).fingerprint, bridged.(*mcpSource).fingerprint; d != b {
		t.Fatalf("fingerprint via bridge = %s, direct = %s", b, d)
	}

	args := json.RawMessage(`{"title":"bug"}`)
	wantOut, err := want[0].Execute(t.Context(), args)
	if err != nil {
		t.Fatal(err)
	}
	gotOut, err := got[0].Execute(t.Context(), args)
	if err != nil || gotOut != wantOut {
		t.Fatalf("call via bridge = %q, %v; direct = %q", gotOut, err, wantOut)
	}
	if err := bridged.Test(t.Context()); err != nil {
		t.Fatalf("Test via bridge: %v", err)
	}
}

func TestMCPThroughStdioBridgeToolError(t *testing.T) {
	t.Parallel()
	src := buildMCPViaBridge(t, &fakeMCP{callErr: true})
	if _, err := src.Tools()[0].Execute(t.Context(), nil); err == nil || !bytes.Contains([]byte(err.Error()), []byte("boom: rate limited")) {
		t.Fatalf("Execute err = %v", err)
	}
}
