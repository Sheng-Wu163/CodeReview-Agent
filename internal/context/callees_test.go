package context

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// parseTestFile parses src into an *ast.File for the pure-function tests.
func parseTestFile(t *testing.T, src string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return file
}

func TestExtractCallees(t *testing.T) {
	src := `package calc

// helperAdd adds two ints.
func helperAdd(a, b int) int { return a + b }

// helperMul multiplies two ints.
func helperMul(a, b int) int { return a * b }

func unused() {}

// CalculateTotal computes the total.
func CalculateTotal(items []int) int {
	total := helperAdd(1, 2)
	return helperMul(total, 3)
}
`
	file := parseTestFile(t, src)
	got := ExtractCallees("calc", []*ast.File{file}, map[string]bool{"CalculateTotal": true})

	if len(got) != 2 {
		t.Fatalf("got %d callees, want 2: %+v", len(got), got)
	}
	// Sorted by name: helperAdd, helperMul.
	if got[0].Name != "helperAdd" || got[1].Name != "helperMul" {
		t.Fatalf("callee names = %q,%q, want helperAdd,helperMul", got[0].Name, got[1].Name)
	}
	if got[0].Package != "calc" {
		t.Errorf("package = %q, want calc", got[0].Package)
	}
	if got[0].Signature != "(int, int) (int)" {
		t.Errorf("signature = %q, want (int, int) (int)", got[0].Signature)
	}
	if got[0].Doc != "helperAdd adds two ints." {
		t.Errorf("doc = %q, want first doc line", got[0].Doc)
	}
}

func TestExtractCalleesIgnoresNonTargetsAndSelectors(t *testing.T) {
	src := `package calc

func helper() int { return 1 }

func Other() int { return helper() }
`
	file := parseTestFile(t, src)
	// Target a function that does not exist: no callees.
	got := ExtractCallees("calc", []*ast.File{file}, map[string]bool{"Missing": true})
	if len(got) != 0 {
		t.Fatalf("non-target should yield no callees, got %+v", got)
	}

	// Selector-only calls (method calls) must be ignored.
	src2 := `package calc

func Target() { var x struct{ Helper func() }; x.Helper() }

func helper() {}
`
	file2 := parseTestFile(t, src2)
	got2 := ExtractCallees("calc", []*ast.File{file2}, map[string]bool{"Target": true})
	if len(got2) != 0 {
		t.Fatalf("selector call should be ignored, got %+v", got2)
	}
}

func TestExtractCalleesEmpty(t *testing.T) {
	if got := ExtractCallees("calc", nil, map[string]bool{"X": true}); got != nil {
		t.Fatalf("no files should yield nil, got %+v", got)
	}
}
