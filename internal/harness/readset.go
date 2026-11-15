package harness

import (
	"path"
	"strings"
	"sync"
)

// pathReadSet is the default ReadSet implementation.
type pathReadSet struct {
	mu    sync.RWMutex
	paths map[string]bool
}

// newPathReadSet creates an empty read set.
func newPathReadSet() *pathReadSet {
	return &pathReadSet{paths: make(map[string]bool)}
}

// Add implements ReadSet.
func (r *pathReadSet) Add(p string) {
	normalized := normalizeReadPath(p)
	if normalized == "" {
		return
	}
	r.mu.Lock()
	r.paths[normalized] = true
	r.mu.Unlock()
}

// Has implements ReadSet.
func (r *pathReadSet) Has(p string) bool {
	normalized := normalizeReadPath(p)
	if normalized == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.paths[normalized]
}

// normalizeReadPath converts both "./auth.go" and "auth.go" (and Windows
// separators) to the same key (v1.12 spec 23.4).
func normalizeReadPath(p string) string {
	p = strings.TrimSpace(strings.Trim(p, `"`))
	// Tool inputs may carry a ":start:end" line range.
	if idx := strings.Index(p, ":"); idx >= 0 {
		p = p[:idx]
	}
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	if p == "." || p == "/" {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}
