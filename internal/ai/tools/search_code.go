package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tmc/langchaingo/tools"
)

// maxSearchMatches caps the number of reported matches per search.
const maxSearchMatches = 100

// maxSearchFileBytes caps how much of each file is searched, to avoid
// scanning huge generated or binary files.
const maxSearchFileBytes = 512 * 1024

// SearchCodeTool searches the review workspace for a regular expression,
// optionally restricted to files matching a glob (FR-11).
type SearchCodeTool struct {
	// Root is the absolute path of the review workspace.
	Root string
}

// Compile-time check that SearchCodeTool implements tools.Tool (FR-11).
var _ tools.Tool = SearchCodeTool{}

// Name implements tools.Tool.
func (t SearchCodeTool) Name() string { return "search_code" }

// Description implements tools.Tool.
func (t SearchCodeTool) Description() string {
	return "Search the code under review with a regular expression. " +
		"Input: \"<regex>\" to search all files, or \"<regex> | <glob>\" to restrict " +
		"search to files matching the glob. Example: \"Query\\(.*\\+ | *.go\""
}

// Call implements tools.Tool. See Description for the input format.
func (t SearchCodeTool) Call(_ context.Context, input string) (string, error) {
	pattern, glob, err := parseSearchInput(input)
	if err != nil {
		return "", fmt.Errorf("search_code: %w", err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("search_code: invalid regex %q: %w", pattern, err)
	}

	matches, err := t.search(re, glob)
	if err != nil {
		return "", fmt.Errorf("search_code: %w", err)
	}
	if len(matches) == 0 {
		return "no matches found", nil
	}
	return strings.Join(matches, "\n"), nil
}

// parseSearchInput splits "<regex>[ | <glob>]" into its parts.
func parseSearchInput(input string) (pattern, glob string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", fmt.Errorf("empty input")
	}
	if idx := strings.Index(input, "|"); idx >= 0 {
		pattern = strings.TrimSpace(input[:idx])
		glob = strings.TrimSpace(input[idx+1:])
		if glob == "" {
			return "", "", fmt.Errorf("empty glob after \"|\"")
		}
		return pattern, glob, nil
	}
	return input, "", nil
}

// search walks the workspace and collects "path:line: text" results.
func (t SearchCodeTool) search(re *regexp.Regexp, glob string) ([]string, error) {
	var matches []string
	err := walkWorkspace(t.Root, func(path string, info fs.FileInfo) {
		if len(matches) >= maxSearchMatches {
			return
		}
		rel, relErr := filepath.Rel(t.Root, path)
		if relErr != nil {
			rel = path
		}
		relSlash := filepath.ToSlash(rel)
		if glob != "" {
			base := pathpkg.Base(relSlash)
			if ok, err := filepath.Match(glob, relSlash); err != nil || !ok {
				if ok2, err2 := filepath.Match(glob, base); err2 != nil || !ok2 {
					return
				}
			}
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if len(data) > maxSearchFileBytes {
			data = data[:maxSearchFileBytes]
		}
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d: %s",
					relSlash, i+1, strings.TrimSuffix(line, "\r")))
				if len(matches) >= maxSearchMatches {
					return
				}
			}
		}
	})
	return matches, err
}

// walkWorkspace walks the workspace rooted at root, skipping directories
// that never contain reviewable code (hidden dirs, vendor, node_modules...)
// and invoking fn for each regular file. It never fails on unreadable
// entries; only root resolution errors are propagated.
func walkWorkspace(root string, fn func(path string, info fs.FileInfo)) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve workspace root: %w", err)
	}
	skipDirs := map[string]bool{
		".git": true, "vendor": true, "node_modules": true, "third_party": true,
	}
	return filepath.Walk(rootAbs, func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			if info != nil && info.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if skipDirs[info.Name()] || strings.HasPrefix(info.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !info.Mode().IsRegular() || info.Size() == 0 {
			return nil
		}
		fn(p, info)
		return nil
	})
}
