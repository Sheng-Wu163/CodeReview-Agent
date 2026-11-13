// Package prompts holds the externalized system prompts for the review
// agents. Prompts live in .txt files so they can be iterated without code
// changes (see CLAUDE.md).
package prompts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
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

// Hash returns the hex-encoded SHA-256 of the named prompt file. It is used
// as the v1.10 checkpoint prompt_hash cache key.
func Hash(name string) (string, error) {
	data, err := promptsFS.ReadFile(name)
	if err != nil {
		return "", fmt.Errorf("read prompt %q: %w", name, err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
