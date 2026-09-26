package missions

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// allowedMissionLiteralFuncs are the only funcs allowed to build a
// Mission{} directly (issue #841): ResolveDefaults is the single
// funnel every create path (API, automations, workflows, follow-ups)
// resolves a mission through, so it must never be bypassed by a
// hand-built literal that skips the D-100 route gate and agent
// defaults. contextBlocks and sweepAskTimeouts build a throwaway
// Mission for a rendering/title helper and never pass it to
// Driver.Create.
var allowedMissionLiteralFuncs = map[string]bool{
	"ResolveDefaults":  true,
	"contextBlocks":    true,
	"sweepAskTimeouts": true,
}

// TestNoMissionLiteralOutsideResolveDefaults walks internal/ and cmd/
// and fails on any non-empty Mission{} (or missions.Mission{}) literal
// outside the allowlisted funcs, proving no create path can diverge
// from ResolveDefaults again the way CreateFollowUp once did.
func TestNoMissionLiteralOutsideResolveDefaults(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		full := filepath.Join(root, dir)
		err := filepath.WalkDir(full, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			scanFileForMissionLiterals(t, fset, file)
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", full, err)
		}
	}
}

// scanFileForMissionLiterals reports every non-empty Mission{} literal
// in file that sits outside an allowlisted func, including one at
// package scope (never allowed).
func scanFileForMissionLiterals(t *testing.T, fset *token.FileSet, file *ast.File) {
	t.Helper()
	report := func(pos token.Pos, funcName string) {
		p := fset.Position(pos)
		if funcName == "" {
			t.Errorf("%s:%d: Mission{} literal at package scope", p.Filename, p.Line)
			return
		}
		t.Errorf("%s:%d: Mission{} literal outside ResolveDefaults (in func %s)", p.Filename, p.Line, funcName)
	}
	inspect := func(node ast.Node, funcName string) {
		ast.Inspect(node, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || len(lit.Elts) == 0 {
				return true
			}
			if isMissionType(lit.Type, file.Name.Name) && !allowedMissionLiteralFuncs[funcName] {
				report(lit.Pos(), funcName)
			}
			return true
		})
	}
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			inspect(decl, "")
			continue
		}
		if fd.Body != nil {
			inspect(fd.Body, fd.Name.Name)
		}
	}
}

// isMissionType reports whether expr names the Mission type: a bare
// "Mission" ident inside package missions itself, or a "missions.Mission"
// selector everywhere else.
func isMissionType(expr ast.Expr, pkgName string) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "Mission" && pkgName == "missions"
	case *ast.SelectorExpr:
		x, ok := t.X.(*ast.Ident)
		return ok && x.Name == "missions" && t.Sel.Name == "Mission"
	}
	return false
}

// repoRoot walks up from the test's working directory to find the
// module root (the directory holding go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from the test's working directory")
		}
		dir = parent
	}
}
