package handler

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A static gate: a 500 means the server failed, and whatever err.Error()
// says at that point is internal detail — SQLite messages, absolute host
// paths, wrapped syscall errors — not something a client can act on. The
// detail belongs in the server log (respondInternalError); the body carries a
// generic or hand-written safe message. It is an AST walk for the same reason
// layering_test.go is: what's forbidden is a call pattern, which no linter
// here can express.
//
// A body is flagged when a JSON response built with a literal 500 status
// contains an x.Error() call or a fmt.Sprintf/Errorf (the other way err text
// gets formatted in).

// internalErrorGuardWaivers lists files the gate skips, with the reason.
// Keep it empty unless a file genuinely must echo error text.
var internalErrorGuardWaivers = map[string]string{}

func TestNoRawErrorTextInInternalErrorResponses(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var violations []string
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		if _, waived := internalErrorGuardWaivers[path]; waived {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || !isJSONResponseCall(call) || len(call.Args) < 2 || !isStatus500(call.Args[0]) {
				return true
			}
			for _, arg := range call.Args[1:] {
				if leaksErrorText(arg) {
					violations = append(violations, fmt.Sprintf("%s: 500 body built from error text", fset.Position(call.Pos())))
				}
			}
			return true
		})
	}
	sort.Strings(violations)
	for _, v := range violations {
		t.Error(v + " — use respondInternalError, or a fixed safe message")
	}
}

func isJSONResponseCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "JSON", "AbortWithStatusJSON", "IndentedJSON", "PureJSON":
		return true
	}
	return false
}

func isStatus500(expr ast.Expr) bool {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return e.Sel.Name == "StatusInternalServerError"
	case *ast.BasicLit:
		return e.Value == "500"
	}
	return false
}

func leaksErrorText(expr ast.Expr) bool {
	leak := false
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name == "Error" && len(call.Args) == 0 {
			leak = true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "fmt" && (sel.Sel.Name == "Sprintf" || sel.Sel.Name == "Errorf") {
			leak = true
		}
		return !leak
	})
	return leak
}
