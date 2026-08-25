package connectors

import (
	"testing"
)

// TestSharedToolSchemasMatchAcrossKinds pins the unified mail surface's
// precondition: every kind serving a raw tool name (mail_search,
// mail_read, ...) serves the SAME input schema (property names, types,
// required) AND the SAME Description as every other kind serving that
// same name, so Manager's aggregation can offer one schema and one
// base description per capability regardless of which account answers
// it. Provider-specific query syntax belongs in the aggregate's
// per-kind description blocks (see mailSearchGuidanceByKind), never in
// a diverging base schema or description. imap and caldav are built
// against local httptest/fake servers so this touches no real network.
func TestSharedToolSchemasMatchAcrossKinds(t *testing.T) {
	t.Parallel()
	fg := &fakeGoogle{}
	gRow := googleRow(bothScopes)
	g, _ := testGoogle(t, fg, gRow)
	gSrc, err := g.Builder()(t.Context(), gRow, nil)
	if err != nil {
		t.Fatalf("google build: %v", err)
	}

	fm := &fakeMicrosoft{}
	mRow := microsoftRow(microsoftAllScopes)
	m, _ := testMicrosoft(t, fm, mRow)
	mSrc, err := m.Builder()(t.Context(), mRow, nil)
	if err != nil {
		t.Fatalf("microsoft build: %v", err)
	}

	iSess := &fakeIMAPSession{}
	iSrc, _ := testIMAPSource(t, imapRow("smtp.example.com"), iSess)

	srv := caldavTestServer(t, nil)
	cSrc := testCalDAVSource(t, srv.URL)

	type toolShape struct {
		schema string
		desc   string
	}
	byKind := map[string]map[string]toolShape{
		"google":    {},
		"microsoft": {},
		"imap":      {},
		"caldav":    {},
	}
	for name, src := range map[string]Source{"google": gSrc, "microsoft": mSrc, "imap": iSrc, "caldav": cSrc} {
		for _, tl := range src.Tools() {
			byKind[name][tl.Name] = toolShape{schema: string(tl.InputSchema), desc: tl.Description}
		}
	}

	// Bucket by tool name: for every tool name served by 2+ kinds,
	// every contributor must match the first contributor exactly.
	byToolName := map[string][]string{} // tool name -> kinds serving it
	for kind, tls := range byKind {
		for name := range tls {
			byToolName[name] = append(byToolName[name], kind)
		}
	}

	var shared int
	for name, kinds := range byToolName {
		if len(kinds) < 2 {
			continue
		}
		shared++
		first := kinds[0]
		firstShape := byKind[first][name]
		for _, kind := range kinds[1:] {
			shape := byKind[kind][name]
			if shape.schema != firstShape.schema {
				t.Errorf("tool %s: %s schema differs from %s\n %s: %s\n %s: %s",
					name, kind, first, kind, shape.schema, first, firstShape.schema)
			}
			if shape.desc != firstShape.desc {
				t.Errorf("tool %s: %s description differs from %s\n %s: %s\n %s: %s",
					name, kind, first, kind, shape.desc, first, firstShape.desc)
			}
		}
	}
	if shared == 0 {
		t.Fatal("no shared tool names found across kinds: test setup is broken")
	}
}
