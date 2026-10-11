package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tmc/langchaingo/tools"
)

// funcDefRe matches a Go function or method declaration line, capturing the
// function name, e.g. "func Run(" or "func (c *Client) Run(".
var funcDefRe = regexp.MustCompile(`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\s*\(`)

// GetFunctionDefTool locates a Go function or method definition in the
// review workspace and returns its full source (FR-11).
type GetFunctionDefTool struct {
	// Root is the absolute path of the review workspace.
	Root string
}

// Compile-time check that GetFunctionDefTool implements tools.Tool (FR-11).
var _ tools.Tool = GetFunctionDefTool{}

// Name implements tools.Tool.
func (t GetFunctionDefTool) Name() string { return "get_function_definition" }

// Description implements tools.Tool.
func (t GetFunctionDefTool) Description() string {
	return "Get the full source of a Go function or method by name, to understand " +
		"its implementation. Input: the function name, e.g. \"HandleWebhook\". " +
		"Methods are matched by their method name, e.g. \"Review\" finds \"func (a *Agent) Review(...)\"."
}

// Call implements tools.Tool. Input is the function or method name.
func (t GetFunctionDefTool) Call(_ context.Context, input string) (string, error) {
	name := strings.TrimSpace(input)
	if name == "" {
		return "", fmt.Errorf("get_function_definition: empty function name")
	}
	if _, err := regexp.Compile(`^` + regexp.QuoteMeta(name) + `$`); err != nil {
		return "", fmt.Errorf("get_function_definition: invalid function name %q", name)
	}

	def, err := t.find(name)
	if err != nil {
		return "", fmt.Errorf("get_function_definition: %w", err)
	}
	return def, nil
}

// find scans Go files in the workspace for a declaration of name and
// extracts the function source using brace balancing.
func (t GetFunctionDefTool) find(name string) (string, error) {
	var found string
	err := walkWorkspace(t.Root, func(p string, info fs.FileInfo) {
		if found != "" || !strings.HasSuffix(p, ".go") {
			return
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return
		}
		def, ok := extractFunction(string(data), name)
		if !ok {
			return
		}
		rel, relErr := filepath.Rel(t.Root, p)
		if relErr != nil {
			rel = p
		}
		found = fmt.Sprintf("%s:\n%s", filepath.ToSlash(rel), def)
	})
	if err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("function %q not found in workspace", name)
	}
	return found, nil
}

// extractFunction finds the declaration of name in src and returns the
// complete function source, using brace counting from the opening brace.
func extractFunction(src, name string) (string, bool) {
	lines := strings.Split(src, "\n")
	for i, line := range lines {
		m := funcDefRe.FindStringSubmatch(strings.TrimPrefix(line, "//"))
		if m == nil || m[1] != name {
			continue
		}
		// Find the opening brace of the body, possibly on a later line.
		depth := 0
		started := false
		var body []string
		for j := i; j < len(lines); j++ {
			body = append(body, lines[j])
			for _, c := range lines[j] {
				switch c {
				case '{':
					depth++
					started = true
				case '}':
					depth--
				}
			}
			if started && depth <= 0 {
				return strings.Join(body, "\n"), true
			}
		}
		// Unbalanced braces (should not happen in valid Go); return what we
		// have so the caller still gets useful context.
		return strings.Join(body, "\n"), true
	}
	return "", false
}
