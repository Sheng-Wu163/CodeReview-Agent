// Package diff provides parsing and filtering of PR diffs.
package diff

import (
	gogitdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
)

// LineType classifies a line inside a hunk. It aliases go-git's diff
// Operation so the two representations stay interchangeable.
type LineType = gogitdiff.Operation

// Line type values (mirrors go-git's diff operations).
const (
	LineContext LineType = gogitdiff.Equal  // unchanged context line
	LineAdded   LineType = gogitdiff.Add    // line added in the PR
	LineDeleted LineType = gogitdiff.Delete // line removed in the PR
)

// ChangeType describes how a file changed in the pull request.
type ChangeType int

// File change types. ChangeModified is the zero value: a plain file edit is
// the default assumption when no mode/rename markers appear in the diff.
const (
	// ChangeModified means the file already existed and was edited.
	ChangeModified ChangeType = iota
	// ChangeAdded means the file was newly created in the PR.
	ChangeAdded
	// ChangeDeleted means the file was removed in the PR.
	ChangeDeleted
	// ChangeRenamed means the file was renamed (possibly with edits).
	ChangeRenamed
)

// String implements fmt.Stringer for ChangeType.
func (c ChangeType) String() string {
	switch c {
	case ChangeAdded:
		return "added"
	case ChangeModified:
		return "modified"
	case ChangeDeleted:
		return "deleted"
	case ChangeRenamed:
		return "renamed"
	default:
		return "unknown"
	}
}

// Line is a single line inside a hunk: either a changed line or a context
// line (FR-03).
type Line struct {
	// Type classifies the line as context, added or deleted.
	Type LineType
	// Content is the line text without the diff prefix character.
	Content string
	// OldNumber is the line number in the old file (context and deleted
	// lines). Zero when not applicable.
	OldNumber int
	// NewNumber is the line number in the new file (context and added
	// lines). Zero when not applicable.
	NewNumber int
}

// Hunk is a contiguous block of changes (FR-03): starting line numbers and
// counts for both file versions plus the changed and context lines.
type Hunk struct {
	// OldStartLine is the 1-based start line of the hunk in the old file.
	OldStartLine int
	// OldLines is the number of old-file lines the hunk covers.
	OldLines int
	// NewStartLine is the 1-based start line of the hunk in the new file.
	NewStartLine int
	// NewLines is the number of new-file lines the hunk covers.
	NewLines int
	// Lines holds the changed and context lines of the hunk in order.
	Lines []Line
}

// FileDiff captures all changes to a single file in the PR (FR-03).
type FileDiff struct {
	// Path is the file path in the new version (empty for deleted files).
	Path string
	// OldPath is the file path in the old version (empty for added files,
	// differs from Path for renames).
	OldPath string
	// ChangeType describes how the file changed.
	ChangeType ChangeType
	// IsBinary marks binary files, which have no parsable hunks.
	IsBinary bool
	// Hunks lists the change blocks of the file in diff order.
	Hunks []Hunk
}

// HasChanges reports whether the file diff contains at least one added or
// deleted line.
func (f FileDiff) HasChanges() bool {
	for _, h := range f.Hunks {
		for _, l := range h.Lines {
			if l.Type == LineAdded || l.Type == LineDeleted {
				return true
			}
		}
	}
	return false
}
