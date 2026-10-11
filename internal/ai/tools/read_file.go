// Package tools implements the LangChainGo review tools used by the review
// agents to collect code context (FR-11).
package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tmc/langchaingo/tools"
)

// ReviewTool is the interface all review tools implement. It is an alias of
// LangChainGo's tools.Tool contract, so implementations satisfy both.
type ReviewTool = tools.Tool

// resolvePath safely joins a tool-supplied relative path with the workspace
// root, rejecting absolute paths and any escape outside the root.
func resolvePath(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty file path")
	}
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return "", fmt.Errorf("absolute paths are not allowed: %q", rel)
	}
	clean := filepath.Clean(filepath.Join(root, rel))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	abs, err := filepath.Abs(clean)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) && abs != rootAbs {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	return abs, nil
}

// ReadFileTool reads files from the review workspace, optionally restricted
// to a line range.
type ReadFileTool struct {
	// Root is the absolute path of the review workspace.
	Root string
}

// Compile-time check that ReadFileTool implements tools.Tool (FR-11).
var _ tools.Tool = ReadFileTool{}

// Name implements tools.Tool.
func (t ReadFileTool) Name() string { return "read_file" }

// Description implements tools.Tool.
func (t ReadFileTool) Description() string {
	return "Read a source file from the code under review. " +
		"Input: \"<path>\" to read the whole file, or \"<path>:<start_line>:<end_line>\" " +
		"for a 1-based inclusive line range. Example: \"internal/config/config.go:10:40\""
}

// Call implements tools.Tool. See Description for the input format.
func (t ReadFileTool) Call(_ context.Context, input string) (string, error) {
	path, start, end, err := parseRangeInput(input)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	full, err := resolvePath(t.Root, path)
	if err != nil {
		return "", fmt.Errorf("read_file: %w", err)
	}
	data, err := os.ReadFile(full)
	if err != nil {
		return "", fmt.Errorf("read_file %q: %w", path, err)
	}

	lines := strings.Split(string(data), "\n")
	selectAll := start == 0 && end == 0
	if selectAll {
		start, end = 1, len(lines)
	}
	start = max(start, 1)
	end = min(end, len(lines))
	if start > end {
		return "", fmt.Errorf("read_file: invalid line range %d:%d", start, end)
	}

	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%d\t%s\n", i, strings.TrimSuffix(lines[i-1], "\r"))
	}
	return b.String(), nil
}

// parseRangeInput splits "<path>[:start[:end]]" into its parts. Line numbers
// are 1-based; 0 means unset (whole file).
func parseRangeInput(input string) (path string, start, end int, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", 0, 0, fmt.Errorf("empty input")
	}
	parts := strings.Split(input, ":")
	path = parts[0]
	switch len(parts) {
	case 1:
		return path, 0, 0, nil
	case 3:
		start, err = strconv.Atoi(parts[1])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid start line %q: %w", parts[1], err)
		}
		end, err = strconv.Atoi(parts[2])
		if err != nil {
			return "", 0, 0, fmt.Errorf("invalid end line %q: %w", parts[2], err)
		}
		if start < 1 || end < 1 {
			return "", 0, 0, fmt.Errorf("line numbers must be >= 1")
		}
		return path, start, end, nil
	default:
		return "", 0, 0, fmt.Errorf("invalid input %q, want \"<path>\" or \"<path>:<start>:<end>\"", input)
	}
}
