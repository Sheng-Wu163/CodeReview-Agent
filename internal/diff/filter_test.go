package diff

import (
	"reflect"
	"testing"
)

func TestFilterShouldInclude(t *testing.T) {
	f := NewFilter(DefaultFilterRules())

	tests := []struct {
		path string
		want bool
	}{
		// Required by T5 acceptance: vendor/, *_generated.go, *.md, go.sum,
		// *.pb.go.
		{"vendor/github.com/foo/bar.go", false},
		{"internal/user_generated.go", false},
		{"README.md", false},
		{"docs/architecture.md", false},
		{"go.sum", false},
		{"internal/api/service.pb.go", false},

		// Additional defaults (FR-04).
		{"third_party/lib/x.go", false},
		{"node_modules/pkg/index.js", false},
		{"web/package-lock.json", false},
		{"web/yarn.lock", false},
		{"internal/api/service.pb.gw.go", false},

		// Must stay included.
		{"main.go", true},
		{"go.mod", true},
		{"internal/config/config.go", true},
		{"internal/config/config_test.go", true},
		{"assets/logo.png", true},
		{"myvendor/x.go", true}, // segment must match exactly
		{"internal/vendored/x.go", true},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := f.ShouldInclude(tt.path); got != tt.want {
				t.Errorf("ShouldInclude(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFilterApply(t *testing.T) {
	f := NewFilter(DefaultFilterRules())

	files := []FileDiff{
		{Path: "vendor/github.com/foo/bar.go", ChangeType: ChangeAdded},
		{Path: "main.go", ChangeType: ChangeModified},
		{Path: "README.md", ChangeType: ChangeModified},
		{Path: "internal/diff/parser.go", ChangeType: ChangeModified},
		{Path: "internal/api/gen_generated.go", ChangeType: ChangeAdded},
	}
	got := f.Apply(files)
	want := []FileDiff{
		{Path: "main.go", ChangeType: ChangeModified},
		{Path: "internal/diff/parser.go", ChangeType: ChangeModified},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Apply mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestFilterApplyDeletedFileUsesOldPath(t *testing.T) {
	f := NewFilter(DefaultFilterRules())

	files := []FileDiff{
		{OldPath: "vendor/github.com/foo/bar.go", ChangeType: ChangeDeleted},
		{Path: "cmd/server/main.go", ChangeType: ChangeDeleted},
	}
	got := f.Apply(files)
	if len(got) != 1 || got[0].Path != "cmd/server/main.go" {
		t.Errorf("Apply = %+v, want only cmd/server/main.go", got)
	}
}

func TestFilterCustomRules(t *testing.T) {
	f := NewFilter(FilterRules{
		ExcludeDirs:     []string{"migrations/"},
		ExcludePatterns: []string{"*.sql", "Dockerfile"},
	})

	tests := []struct {
		path string
		want bool
	}{
		{"db/migrations/001_init.go", false},
		{"schema.sql", false},
		{"db/schema.sql", false},
		{"Dockerfile", false},
		{"vendor/lib.go", true}, // vendor not in custom rules
		{"main.go", true},
		{"README.md", true}, // *.md not in custom rules
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := f.ShouldInclude(tt.path); got != tt.want {
				t.Errorf("ShouldInclude(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestFilterEmptyRulesFallBackToDefaults(t *testing.T) {
	f := NewFilter(FilterRules{})
	if !f.shouldExclude("vendor/x.go") {
		t.Error("empty rules should fall back to defaults: vendor/x.go must be excluded")
	}
	if f.shouldExclude("main.go") {
		t.Error("main.go must be included under default rules")
	}
}

// shouldExclude is a test helper returning the negation of ShouldInclude.
func (f *Filter) shouldExclude(p string) bool { return !f.ShouldInclude(p) }
