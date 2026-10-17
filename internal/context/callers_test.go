package context

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// writeModule writes a temp Go module with the given files and returns its
// root.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files["go.mod"] = "module fixture\n\ngo 1.26\n"
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func TestCallerRetrieverFindsCallSites(t *testing.T) {
	root := writeModule(t, map[string]string{
		"def.go":      "package fixture\n\ntype Item struct{}\n\nfunc CalculateTotal(items []Item) float64 {\n\treturn 0\n}\n",
		"caller_a.go": "package fixture\n\nfunc A() float64 {\n\treturn CalculateTotal(nil)\n}\n",
		"caller_b.go": "package fixture\n\nfunc B() float64 {\n\treturn CalculateTotal(nil)\n}\n\nfunc C() float64 {\n\treturn CalculateTotal(nil)\n}\n",
	})

	r := NewCallerRetriever(root, nil)
	got := r.FindCallers(context.Background(), []StructuralChange{
		{FilePath: "def.go", FunctionName: "CalculateTotal", ChangeType: SignatureChanged},
	})

	if len(got) != 3 {
		t.Fatalf("got %d call sites, want 3: %+v", len(got), got)
	}
	callers := map[string]bool{}
	for _, c := range got {
		if c.Line <= 0 {
			t.Errorf("call site missing line number: %+v", c)
		}
		if !strings.Contains(c.Snippet, "CalculateTotal") {
			t.Errorf("snippet does not contain the call: %q", c.Snippet)
		}
		if c.CallerFunc == "" {
			t.Errorf("call site missing caller function: %+v", c)
		}
		callers[c.CallerFunc] = true
	}
	for _, want := range []string{"A", "B", "C"} {
		if !callers[want] {
			t.Errorf("missing caller %q in %+v", want, got)
		}
	}
}

func TestCallerRetrieverTruncatesAt10(t *testing.T) {
	var b strings.Builder
	b.WriteString("package fixture\n\ntype Item struct{}\n\nfunc CalculateTotal(items []Item) float64 { return 0 }\n\n")
	for i := 0; i < 15; i++ {
		b.WriteString("func Caller" + strconv.Itoa(i) + "() float64 { return CalculateTotal(nil) }\n\n")
	}
	root := writeModule(t, map[string]string{"many.go": b.String()})

	r := NewCallerRetriever(root, nil)
	got := r.FindCallers(context.Background(), []StructuralChange{
		{FilePath: "many.go", FunctionName: "CalculateTotal", ChangeType: SignatureChanged},
	})

	if len(got) != 10 {
		t.Fatalf("got %d call sites, want truncation to 10", len(got))
	}
	// Deterministic order: the first 10 declarations.
	if got[0].CallerFunc != "Caller0" {
		t.Errorf("first call site = %q, want Caller0", got[0].CallerFunc)
	}
}

func TestCallerRetrieverDegradesOnBrokenPackage(t *testing.T) {
	root := writeModule(t, map[string]string{
		"broken.go": "package fixture\n\nfunc broken( {\n",
	})

	r := NewCallerRetriever(root, nil)
	got := r.FindCallers(context.Background(), []StructuralChange{
		{FilePath: "broken.go", FunctionName: "Broken", ChangeType: SignatureChanged},
	})

	if len(got) != 0 {
		t.Fatalf("broken package should degrade to empty, got %+v", got)
	}
}

func TestCallerRetrieverEmptyTargets(t *testing.T) {
	r := NewCallerRetriever(t.TempDir(), nil)
	if got := r.FindCallers(context.Background(), nil); got != nil {
		t.Fatalf("no targets should return nil, got %+v", got)
	}
}
