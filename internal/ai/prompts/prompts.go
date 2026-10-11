// Package prompts holds the externalized system prompts for the review
// agents. Prompts live in .txt files so they can be iterated without code
// changes (see CLAUDE.md).
package prompts

import (
	"embed"
	"fmt"
)

//go:embed *.txt
var promptsFS embed.FS

// Get returns the content of the named prompt file (e.g. "security.txt").
func Get(name string) (string, error) {
	data, err := promptsFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read prompt %q: %w", name, err)
	}
	return string(data), nil
}
