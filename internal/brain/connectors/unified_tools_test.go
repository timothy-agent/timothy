package connectors

import (
	"testing"
)

// TestGoogleMicrosoftSharedToolSchemasMatch pins the unified mail
// surface's precondition: google and microsoft serve the SAME input
// schema (property names, types, required) AND the SAME Description
// for every tool name they both expose, so Manager's aggregation can
// offer one schema and one base description per capability regardless
// of which account answers it. Provider-specific query syntax belongs
// in the aggregate's per-kind description blocks (see
// mailSearchGuidanceByKind), never in a diverging base schema or
// description.
func TestGoogleMicrosoftSharedToolSchemasMatch(t *testing.T) {
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

	type toolShape struct {
		schema string
		desc   string
	}
	gByName := map[string]toolShape{}
	for _, tl := range gSrc.Tools() {
		gByName[tl.Name] = toolShape{schema: string(tl.InputSchema), desc: tl.Description}
	}
	mByName := map[string]toolShape{}
	for _, tl := range mSrc.Tools() {
		mByName[tl.Name] = toolShape{schema: string(tl.InputSchema), desc: tl.Description}
	}

	var shared int
	for name, gShape := range gByName {
		mShape, ok := mByName[name]
		if !ok {
			continue
		}
		shared++
		if gShape.schema != mShape.schema {
			t.Errorf("tool %s: schemas differ\n google: %s\n msft:   %s", name, gShape.schema, mShape.schema)
		}
		if gShape.desc != mShape.desc {
			t.Errorf("tool %s: descriptions differ\n google: %s\n msft:   %s", name, gShape.desc, mShape.desc)
		}
	}
	if shared == 0 {
		t.Fatal("no shared tool names found between google and microsoft: test setup is broken")
	}
}
