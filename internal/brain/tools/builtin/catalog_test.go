package builtin

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestCatalogNamesUniqueAndDescribed(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, tool := range Catalog() {
		if tool.Name == "" {
			t.Fatal("catalog tool with empty name")
		}
		if seen[tool.Name] {
			t.Errorf("duplicate catalog tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s has an empty description", tool.Name)
		}
		if len(tool.InputSchema) == 0 {
			t.Errorf("%s has no input schema", tool.Name)
		}
	}
}

// TestCatalogCoversEveryConstructor fails when an exported function in
// this package returns *tools.Tool but Catalog never calls it.
func TestCatalogCoversEveryConstructor(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var constructors, called []string
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.IsExported() && returnsToolPtr(fn) {
				constructors = append(constructors, fn.Name.Name)
			}
		}
		if name != "catalog.go" {
			continue
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if id, ok := call.Fun.(*ast.Ident); ok {
					called = append(called, id.Name)
				}
			}
			return true
		})
	}
	if len(constructors) == 0 {
		t.Fatal("found no tool constructors")
	}
	for _, c := range constructors {
		if !slices.Contains(called, c) {
			t.Errorf("Catalog does not include %s()", c)
		}
	}
}

func returnsToolPtr(fn *ast.FuncDecl) bool {
	res := fn.Type.Results
	if res == nil || len(res.List) != 1 {
		return false
	}
	star, ok := res.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "tools" && sel.Sel.Name == "Tool"
}
