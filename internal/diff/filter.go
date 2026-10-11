package diff

import (
	"path"
	"strings"
)

// FilterRules holds the configurable file exclusion rules (FR-04).
type FilterRules struct {
	// ExcludeDirs lists directory names whose contents are excluded, e.g.
	// "vendor". Any path containing a directory with one of these names is
	// filtered out, at any depth.
	ExcludeDirs []string
	// ExcludePatterns lists path.Match glob patterns. Each pattern is
	// matched against both the full path and the base file name, e.g.
	// "*.md", "*_generated.go", "go.sum".
	ExcludePatterns []string
}

// DefaultFilterRules returns the default exclusion rules required by FR-04:
// generated files, vendor/third-party directories, documentation and lock
// files.
func DefaultFilterRules() FilterRules {
	return FilterRules{
		ExcludeDirs: []string{"vendor", "third_party", "node_modules"},
		ExcludePatterns: []string{
			"*_generated.go",
			"*.pb.go",
			"*.pb.gw.go",
			"*.md",
			"go.sum",
			"package-lock.json",
			"yarn.lock",
		},
	}
}

// Filter decides which files should enter the review queue (FR-04).
type Filter struct {
	rules FilterRules
}

// NewFilter creates a Filter with the given rules. Empty rule lists fall
// back to the defaults from DefaultFilterRules.
func NewFilter(rules FilterRules) *Filter {
	normalized := FilterRules{
		ExcludeDirs:     make([]string, 0, len(rules.ExcludeDirs)),
		ExcludePatterns: make([]string, 0, len(rules.ExcludePatterns)),
	}
	for _, d := range rules.ExcludeDirs {
		if d = strings.Trim(d, "/\\"); d != "" {
			normalized.ExcludeDirs = append(normalized.ExcludeDirs, d)
		}
	}
	normalized.ExcludePatterns = append(normalized.ExcludePatterns, rules.ExcludePatterns...)
	if len(normalized.ExcludeDirs) == 0 && len(normalized.ExcludePatterns) == 0 {
		normalized = DefaultFilterRules()
	}
	return &Filter{rules: normalized}
}

// ShouldInclude reports whether the file at the given path passes the
// filter and should be reviewed.
func (f *Filter) ShouldInclude(p string) bool {
	if p == "" {
		return false
	}
	for _, dir := range f.rules.ExcludeDirs {
		for _, segment := range strings.Split(p, "/") {
			if segment == dir {
				return false
			}
		}
	}
	for _, pattern := range f.rules.ExcludePatterns {
		if ok, err := path.Match(pattern, p); err == nil && ok {
			return false
		}
		if ok, err := path.Match(pattern, path.Base(p)); err == nil && ok {
			return false
		}
	}
	return true
}

// Apply returns the subset of files that pass the filter, preserving input
// order.
func (f *Filter) Apply(files []FileDiff) []FileDiff {
	filtered := make([]FileDiff, 0, len(files))
	for _, file := range files {
		// Deleted files are identified by their old path.
		p := file.Path
		if p == "" {
			p = file.OldPath
		}
		if f.ShouldInclude(p) {
			filtered = append(filtered, file)
		}
	}
	return filtered
}
