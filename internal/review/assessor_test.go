package review

import (
	"fmt"
	"testing"

	"codereviewagent/internal/diff"
)

// fileWithLines builds a FileDiff with n added lines at the given path.
func fileWithLines(path string, n int) diff.FileDiff {
	h := diff.Hunk{NewStartLine: 1}
	for i := 0; i < n; i++ {
		h.Lines = append(h.Lines, diff.Line{
			Type:      diff.LineAdded,
			Content:   fmt.Sprintf("line %d", i+1),
			NewNumber: i + 1,
		})
	}
	return diff.FileDiff{Path: path, ChangeType: diff.ChangeModified, Hunks: []diff.Hunk{h}}
}

// fileWithChurn builds a FileDiff with a added and d deleted lines.
func fileWithChurn(path string, added, deleted int) diff.FileDiff {
	h := diff.Hunk{NewStartLine: 1}
	for i := 0; i < added; i++ {
		h.Lines = append(h.Lines, diff.Line{Type: diff.LineAdded, Content: "a", NewNumber: i + 1})
	}
	for i := 0; i < deleted; i++ {
		h.Lines = append(h.Lines, diff.Line{Type: diff.LineDeleted, Content: "d", OldNumber: i + 1})
	}
	return diff.FileDiff{Path: path, ChangeType: diff.ChangeModified, Hunks: []diff.Hunk{h}}
}

func TestAssessorLevels(t *testing.T) {
	tests := []struct {
		name  string
		diffs []diff.FileDiff
		want  ComplexityLevel
	}{
		{
			name:  "AC-70: 10 lines, single file, non-sensitive => Trivial",
			diffs: []diff.FileDiff{fileWithLines("pkg/util.go", 10)},
			want:  ComplexityTrivial,
		},
		{
			name:  "5 lines, single file => Trivial",
			diffs: []diff.FileDiff{fileWithLines("pkg/util.go", 5)},
			want:  ComplexityTrivial,
		},
		{
			name:  "AC-71: 250 lines => Complex",
			diffs: []diff.FileDiff{fileWithLines("pkg/big.go", 250)},
			want:  ComplexityComplex,
		},
		{
			name: "churn counts deletions too: 150 added + 150 deleted => Complex",
			diffs: []diff.FileDiff{
				fileWithChurn("pkg/big.go", 150, 150),
			},
			want: ComplexityComplex,
		},
		{
			name: "more than 5 files => Complex",
			diffs: []diff.FileDiff{
				fileWithLines("a.go", 1), fileWithLines("b.go", 1),
				fileWithLines("c.go", 1), fileWithLines("d.go", 1),
				fileWithLines("e.go", 1), fileWithLines("f.go", 1),
			},
			want: ComplexityComplex,
		},
		{
			name:  "AC-72: auth/login.go, single line => Complex (sensitive)",
			diffs: []diff.FileDiff{fileWithLines("auth/login.go", 1)},
			want:  ComplexityComplex,
		},
		{
			name:  "internal/auth/login.go => Complex (sensitive segment)",
			diffs: []diff.FileDiff{fileWithLines("internal/auth/login.go", 1)},
			want:  ComplexityComplex,
		},
		{
			name:  "db/migrations/0001.sql => Complex (default sensitive)",
			diffs: []diff.FileDiff{fileWithLines("db/migrations/0001.sql", 1)},
			want:  ComplexityComplex,
		},
		{
			name:  "oauth/login.go is NOT sensitive => Trivial",
			diffs: []diff.FileDiff{fileWithLines("oauth/login.go", 3)},
			want:  ComplexityTrivial,
		},
		{
			name:  "20 lines (not < 20) => Standard",
			diffs: []diff.FileDiff{fileWithLines("pkg/util.go", 20)},
			want:  ComplexityStandard,
		},
		{
			name:  "10 lines but 2 files => Standard",
			diffs: []diff.FileDiff{fileWithLines("a.go", 5), fileWithLines("b.go", 5)},
			want:  ComplexityStandard,
		},
		{
			name:  "between thresholds => Standard",
			diffs: []diff.FileDiff{fileWithLines("pkg/mid.go", 50)},
			want:  ComplexityStandard,
		},
		{
			name:  "spec 14.4: empty diff => Trivial",
			diffs: nil,
			want:  ComplexityTrivial,
		},
		{
			name:  "spec 14.4: fully filtered diff => Trivial",
			diffs: []diff.FileDiff{},
			want:  ComplexityTrivial,
		},
	}

	a := NewAssessor(AssessorOptions{})
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := a.Assess(tt.diffs); got != tt.want {
				t.Errorf("Assess() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestAssessorCustomThresholds covers FR-38: thresholds are configurable
// and a configured sensitive list replaces the defaults.
func TestAssessorCustomThresholds(t *testing.T) {
	a := NewAssessor(AssessorOptions{
		TrivialMaxLines: 5,
		ComplexMinLines: 50,
		ComplexMinFiles: 2,
		SensitivePaths:  []string{"internal/billing"},
	})

	if got := a.Assess([]diff.FileDiff{fileWithLines("a.go", 4)}); got != ComplexityTrivial {
		t.Errorf("4 lines with trivial_max_lines=5 = %q, want trivial", got)
	}
	if got := a.Assess([]diff.FileDiff{fileWithLines("a.go", 60)}); got != ComplexityComplex {
		t.Errorf("60 lines with complex_min_lines=50 = %q, want complex", got)
	}
	if got := a.Assess([]diff.FileDiff{fileWithLines("a.go", 3), fileWithLines("b.go", 3), fileWithLines("c.go", 3)}); got != ComplexityComplex {
		t.Errorf("3 files with complex_min_files=2 = %q, want complex", got)
	}
	// auth is NOT sensitive anymore because the config replaced the defaults.
	if got := a.Assess([]diff.FileDiff{fileWithLines("auth/login.go", 2)}); got != ComplexityTrivial {
		t.Errorf("auth/* with custom sensitive list = %q, want trivial", got)
	}
	if got := a.Assess([]diff.FileDiff{fileWithLines("internal/billing/charge.go", 2)}); got != ComplexityComplex {
		t.Errorf("internal/billing/* with custom sensitive list = %q, want complex", got)
	}
}

// TestAssessorDeterministic covers AC-73: 100 runs on the same input agree.
func TestAssessorDeterministic(t *testing.T) {
	diffs := []diff.FileDiff{fileWithLines("pkg/util.go", 12)}
	a := NewAssessor(AssessorOptions{})

	first := a.Assess(diffs)
	for i := 0; i < 100; i++ {
		if got := a.Assess(diffs); got != first {
			t.Fatalf("run %d = %q, want %q (must be deterministic)", i, got, first)
		}
	}
}
