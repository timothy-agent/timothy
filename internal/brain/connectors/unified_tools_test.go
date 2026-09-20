package connectors

import (
	"context"
	"strings"
	"testing"
)

// TestSharedToolSchemasMatchAcrossKinds pins the unified mail surface's
// precondition: every kind serving a raw tool name (search_mail,
// read_mail, ...) serves the SAME input schema (property names, types,
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

	// github and bitbucket build with no network call and share the four
	// pull request tool names (issue #656); their schemas and
	// descriptions are duplicated literals, so this is the drift guard.
	tokenResolve := func(_ context.Context, _ string) (string, error) { return "tok", nil }
	ghSrc, err := GitHubBuilder(nil)(t.Context(), Connector{Name: "gh", Kind: "github", CredentialRef: "GH_PAT"}, tokenResolve)
	if err != nil {
		t.Fatalf("github build: %v", err)
	}
	bbSrc, err := BitbucketBuilder(nil)(t.Context(), Connector{Name: "bb", Kind: "bitbucket", CredentialRef: "BB_TOKEN"}, tokenResolve)
	if err != nil {
		t.Fatalf("bitbucket build: %v", err)
	}

	type toolShape struct {
		schema string
		desc   string
	}
	// Driven by the kinds registry, not a literal list: a new kind must
	// either be built here or be named below, so one reusing a shared tool
	// name cannot slip past this guard unnoticed.
	glSrc, err := GitLabBuilder(nil)(t.Context(), Connector{Name: "gl", Kind: "gitlab", CredentialRef: "GL_TOKEN"}, tokenResolve)
	if err != nil {
		t.Fatalf("gitlab build: %v", err)
	}
	sources := map[string]Source{"google": gSrc, "microsoft": mSrc, "imap": iSrc, "caldav": cSrc, "github": ghSrc, "bitbucket": bbSrc, "gitlab": glSrc}
	// mcp wraps an external server whose schemas are its own; aws is an mcp
	// bridge; gcp serves only its own storage/bigquery tools.
	noSharedTools := map[string]bool{"mcp": true, "aws": true, "gcp": true}
	byKind := map[string]map[string]toolShape{}
	for kind := range kinds {
		src, ok := sources[kind]
		if !ok {
			if !noSharedTools[kind] {
				t.Errorf("kind %q is in the kinds registry but not built here: add a source or name it in noSharedTools", kind)
			}
			continue
		}
		byKind[kind] = map[string]toolShape{}
		for _, tl := range src.Tools() {
			byKind[kind][tl.Name] = toolShape{schema: string(tl.InputSchema), desc: tl.Description}
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

	// Issue #645: the read-vs-search and list-vs-search contrasts must
	// stay in these descriptions so the model routes between them
	// without guessing.
	contrasts := map[string]string{
		"search_mail":          "read_mail",
		"read_mail":            "search_mail",
		"list_calendar_events": "search_mail",
	}
	for name, want := range contrasts {
		var checked int
		for kind, tls := range byKind {
			shape, ok := tls[name]
			if !ok {
				continue
			}
			checked++
			if !strings.Contains(strings.ToLower(shape.desc), "do not use this") {
				t.Errorf("tool %s (%s): description states no negative space", name, kind)
			}
			if !strings.Contains(shape.desc, want) {
				t.Errorf("tool %s (%s): description does not name %s", name, kind, want)
			}
		}
		if checked == 0 {
			t.Errorf("tool %s was served by no kind: test setup is broken", name)
		}
	}
}
