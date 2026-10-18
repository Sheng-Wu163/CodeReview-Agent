// Package review implements the v1.2 adaptive review strategy: a fully
// deterministic complexity assessor (FR-34/FR-36) and a strategy selector
// that only decides which agents run (FR-35). It never tunes agent
// parameters and never calls an LLM.
package review

import (
	"path/filepath"
	"strings"

	"codereviewagent/internal/diff"
)

// ComplexityLevel classifies a pull request (v1.2 spec 14.3, FROZEN).
type ComplexityLevel string

// Complexity levels.
const (
	// ComplexityTrivial is a tiny, low-risk change.
	ComplexityTrivial ComplexityLevel = "trivial"
	// ComplexityStandard is the default, full-review level.
	ComplexityStandard ComplexityLevel = "standard"
	// ComplexityComplex is a large or security-sensitive change.
	ComplexityComplex ComplexityLevel = "complex"
)

// Default thresholds (FR-34); overridable via config (FR-38).
const (
	// DefaultTrivialMaxLines is the exclusive upper bound of a trivial
	// change.
	DefaultTrivialMaxLines = 20
	// DefaultComplexMinLines is the exclusive lower bound of a complex
	// change.
	DefaultComplexMinLines = 200
	// DefaultComplexMinFiles is the exclusive file-count lower bound of a
	// complex change.
	DefaultComplexMinFiles = 5
)

// defaultSensitivePaths are used when the configuration leaves the list
// empty (spec 14.4).
var defaultSensitivePaths = []string{"auth", "migration", "migrations"}

// ReviewStrategy defines the review plan for one PR (v1.2 spec 14.3,
// FROZEN). It only toggles which agents run; it never adjusts agent
// parameters.
type ReviewStrategy struct {
	// Level is the assessed complexity.
	Level ComplexityLevel
	// EnabledAgents lists the agent names to run, e.g. ["security", "lint"].
	EnabledAgents []string
	// EnableVerifier toggles the adversarial verification stage.
	EnableVerifier bool
}

// ComplexityAssessor assesses a pull request's complexity (v1.2 spec 14.3,
// FROZEN). Implementations must be deterministic: the same diffs always
// yield the same level (FR-36).
type ComplexityAssessor interface {
	// Assess returns the complexity level of the given diffs.
	Assess(diffs []diff.FileDiff) ComplexityLevel
}

// AssessorOptions are the deterministic thresholds of an Assessor. Zero
// values fall back to the package defaults; an empty SensitivePaths falls
// back to the built-in defaults (spec 14.4).
type AssessorOptions struct {
	// TrivialMaxLines is the exclusive upper line bound for Trivial.
	TrivialMaxLines int
	// ComplexMinLines is the exclusive lower line bound for Complex.
	ComplexMinLines int
	// ComplexMinFiles is the exclusive file-count bound for Complex.
	ComplexMinFiles int
	// SensitivePaths are path segments/prefixes that force Complex.
	SensitivePaths []string
}

// Assessor is the default ComplexityAssessor. It performs pure, in-memory
// statistics over the diffs: no I/O, no LLM, no randomness (CLAUDE.v.1.2.md).
type Assessor struct {
	trivialMaxLines int
	complexMinLines int
	complexMinFiles int
	sensitivePaths  []string
}

// Compile-time check that Assessor implements ComplexityAssessor.
var _ ComplexityAssessor = (*Assessor)(nil)

// NewAssessor creates an assessor, filling zero/empty options with the
// defaults.
func NewAssessor(opts AssessorOptions) *Assessor {
	a := &Assessor{
		trivialMaxLines: opts.TrivialMaxLines,
		complexMinLines: opts.ComplexMinLines,
		complexMinFiles: opts.ComplexMinFiles,
		sensitivePaths:  opts.SensitivePaths,
	}
	if a.trivialMaxLines <= 0 {
		a.trivialMaxLines = DefaultTrivialMaxLines
	}
	if a.complexMinLines <= 0 {
		a.complexMinLines = DefaultComplexMinLines
	}
	if a.complexMinFiles <= 0 {
		a.complexMinFiles = DefaultComplexMinFiles
	}
	if len(a.sensitivePaths) == 0 {
		a.sensitivePaths = defaultSensitivePaths
	}
	return a
}

// Assess implements ComplexityAssessor. Complex wins over Trivial: a
// sensitive path forces Complex even for a one-line change (spec 14.4).
func (a *Assessor) Assess(diffs []diff.FileDiff) ComplexityLevel {
	lines, files := countChanges(diffs)
	if a.touchesSensitivePath(diffs) ||
		lines > a.complexMinLines ||
		files > a.complexMinFiles {
		return ComplexityComplex
	}
	// An empty (or fully filtered) diff is Trivial (spec 14.4).
	if lines < a.trivialMaxLines && files <= 1 {
		return ComplexityTrivial
	}
	return ComplexityStandard
}

// countChanges returns the total added+deleted line count and the number of
// files in the diff.
func countChanges(diffs []diff.FileDiff) (lines, files int) {
	for _, f := range diffs {
		for _, h := range f.Hunks {
			for _, l := range h.Lines {
				if l.Type == diff.LineAdded || l.Type == diff.LineDeleted {
					lines++
				}
			}
		}
	}
	return lines, len(diffs)
}

// touchesSensitivePath reports whether any changed file path matches one of
// the configured sensitive paths.
func (a *Assessor) touchesSensitivePath(diffs []diff.FileDiff) bool {
	for _, f := range diffs {
		if a.pathIsSensitive(f.Path) || a.pathIsSensitive(f.OldPath) {
			return true
		}
	}
	return false
}

// pathIsSensitive matches one path against the sensitive list. A sensitive
// entry matches a whole path segment, a path prefix, or a trailing segment,
// so "auth" matches "internal/auth/login.go" but not "oauth/login.go".
func (a *Assessor) pathIsSensitive(path string) bool {
	if path == "" {
		return false
	}
	p := filepath.ToSlash(path)
	p = strings.TrimPrefix(p, "./")
	for _, sp := range a.sensitivePaths {
		sp = strings.Trim(filepath.ToSlash(sp), "/")
		if sp == "" {
			continue
		}
		if p == sp ||
			strings.HasPrefix(p, sp+"/") ||
			strings.Contains(p, "/"+sp+"/") ||
			strings.HasSuffix(p, "/"+sp) {
			return true
		}
	}
	return false
}
