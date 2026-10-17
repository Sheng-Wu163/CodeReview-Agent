package context

import (
	"testing"

	"codereviewagent/internal/diff"
)

// hunk builds a single-hunk FileDiff from raw lines whose first character
// encodes the line type: '+' added, '-' deleted, ' ' context.
func goFileDiff(path string, raw ...string) diff.FileDiff {
	h := diff.Hunk{OldStartLine: 1, NewStartLine: 1}
	for _, r := range raw {
		var lt diff.LineType
		content := r
		switch {
		case len(r) > 0 && r[0] == '+':
			lt, content = diff.LineAdded, r[1:]
		case len(r) > 0 && r[0] == '-':
			lt, content = diff.LineDeleted, r[1:]
		default:
			lt = diff.LineContext
			if len(r) > 0 && r[0] == ' ' {
				content = r[1:]
			}
		}
		h.Lines = append(h.Lines, diff.Line{Type: lt, Content: content})
	}
	return diff.FileDiff{Path: path, ChangeType: diff.ChangeModified, Hunks: []diff.Hunk{h}}
}

func TestDetectStructuralChanges(t *testing.T) {
	tests := []struct {
		name  string
		diffs []diff.FileDiff
		want  []StructuralChange
	}{
		{
			name: "exported function param change (AC-50)",
			diffs: []diff.FileDiff{goFileDiff("calc.go",
				" package calc",
				"",
				"-func CalculateTotal(items []Item) float64 {",
				"+func CalculateTotal(items []Item, tax float64) float64 {",
				" \ttotal := 0.0",
			)},
			want: []StructuralChange{
				{FilePath: "calc.go", FunctionName: "CalculateTotal", ChangeType: SignatureChanged},
			},
		},
		{
			name: "private function logic change (AC-51)",
			diffs: []diff.FileDiff{goFileDiff("calc.go",
				" package calc",
				"",
				"-func helper(a int) int {",
				"+func helper(a int, b int) int {",
				" \treturn a",
			)},
			want: nil,
		},
		{
			name: "interface method added",
			diffs: []diff.FileDiff{goFileDiff("io.go",
				" package io",
				"",
				" type Reader interface {",
				" \tRead(p []byte) (n int, err error)",
				"+\tClose() error",
				" }",
			)},
			want: []StructuralChange{
				{FilePath: "io.go", FunctionName: "Reader", ChangeType: InterfaceChanged},
			},
		},
		{
			name: "exported function removed",
			diffs: []diff.FileDiff{goFileDiff("calc.go",
				" package calc",
				"-func Old(items []Item) float64 {",
				" \treturn 0",
				" }",
			)},
			want: []StructuralChange{
				{FilePath: "calc.go", FunctionName: "Old", ChangeType: SignatureChanged},
			},
		},
		{
			name: "non-go file ignored",
			diffs: []diff.FileDiff{goFileDiff("config.yaml",
				"-key: 1",
				"+key: 2",
			)},
			want: nil,
		},
		{
			name: "deleted go file ignored",
			diffs: []diff.FileDiff{{
				Path: "", OldPath: "gone.go", ChangeType: diff.ChangeDeleted,
				Hunks: []diff.Hunk{{Lines: []diff.Line{
					{Type: diff.LineDeleted, Content: "func Gone() {}"},
				}}},
			}},
			want: nil,
		},
		{
			name: "binary go file ignored",
			diffs: []diff.FileDiff{{
				Path: "blob.go", IsBinary: true,
				Hunks: []diff.Hunk{{Lines: []diff.Line{
					{Type: diff.LineAdded, Content: "func Added(a, b int) {}"},
				}}},
			}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectStructuralChanges(tt.diffs)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d changes (%+v), want %d (%+v)", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("change[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}
