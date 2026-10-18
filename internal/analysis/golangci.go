package analysis

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codereviewagent/internal/diff"
)

// golangciRunMu serializes in-process golangci-lint invocations: the CLI
// enforces a single global instance, so concurrent same-package jobs (e.g.
// the pipeline worker pool linting several files of one package) would
// fail-fast against each other. Cross-process contention is handled by the
// retry loop below.
var golangciRunMu sync.Mutex

// DefaultGolangciBinary is the default golangci-lint binary name.
const DefaultGolangciBinary = "golangci-lint"

// styleLinters report conventions rather than bugs; their findings map to
// "suggestion" (FR-13). Everything else maps to "warning".
var styleLinters = map[string]bool{
	"gofmt": true, "gofumpt": true, "goimports": true, "gci": true,
	"revive": true, "golint": true, "misspell": true, "godot": true,
	"whitespace": true, "lll": true, "wsl": true, "decorder": true,
	"unconvert": true, "prealloc": true,
}

// golangciIssue mirrors the subset of golangci-lint --output.json.path
// stdout v2 JSON that we consume.
type golangciIssue struct {
	FromLinter  string   `json:"FromLinter"`
	Text        string   `json:"Text"`
	Severity    string   `json:"Severity"`
	SourceLines []string `json:"SourceLines"`
	Pos         struct {
		Filename string `json:"Filename"`
		Line     int    `json:"Line"`
		Column   int    `json:"Column"`
	} `json:"Pos"`
	LineRange struct {
		From int `json:"From"`
		To   int `json:"To"`
	} `json:"LineRange"`
}

// GolangciLintAnalyzer runs golangci-lint (v2 CLI) on the package of a
// changed file and maps its JSON output to PreAnalysisFinding entries.
// If the binary is not installed or the CLI fails, Analyze returns a
// descriptive error - never a panic.
type GolangciLintAnalyzer struct {
	// Root is the directory the CLI runs in (the review workspace, which
	// must contain the Go module).
	Root string
	// ConfigPath optionally points to a golangci-lint config file passed
	// via --config (FR-24). Empty uses the workspace default discovery.
	ConfigPath string
	// Binary is the golangci-lint executable. Empty selects
	// DefaultGolangciBinary (overridable in tests).
	Binary string
}

// Compile-time check that GolangciLintAnalyzer implements the Analyzer
// interface.
var _ Analyzer = GolangciLintAnalyzer{}

// NewGolangciLintAnalyzer creates a golangci-lint analyzer. root is the
// review workspace; configPath may be empty.
func NewGolangciLintAnalyzer(root, configPath string) GolangciLintAnalyzer {
	return GolangciLintAnalyzer{Root: root, ConfigPath: configPath}
}

// Name implements Analyzer.
func (a GolangciLintAnalyzer) Name() string { return ToolGolangciLint }

// Analyze implements Analyzer: it lints the package containing the changed
// file and returns the issues that point into that file.
func (a GolangciLintAnalyzer) Analyze(ctx context.Context, file diff.FileDiff) ([]PreAnalysisFinding, error) {
	path := file.Path
	if path == "" {
		// Deleted file: nothing left to lint.
		return nil, nil
	}

	pkgDir := filepath.ToSlash(filepath.Dir(path))
	args := []string{"run", pkgDir, "--output.json.path", "stdout"}
	if a.ConfigPath != "" {
		args = append(args, "--config", a.ConfigPath)
	}

	binary := a.Binary
	if binary == "" {
		binary = DefaultGolangciBinary
	}

	// golangci-lint enforces a single global instance ("parallel
	// golangci-lint is running"), which also triggers when two PR reviews
	// (or two test packages) run concurrently. Retry with backoff while the
	// lock is held, staying within the caller's context budget: a competing
	// run can hold the lock for up to a minute.
	const (
		maxAttempts   = 8
		lockRetryWait = 10 * time.Second
	)
	for attempt := 1; ; attempt++ {
		golangciRunMu.Lock()
		var stderr bytes.Buffer
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = a.Root
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		golangciRunMu.Unlock()

		if attempt < maxAttempts && isParallelLockError(err, out, &stderr) {
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("golangci-lint: %w", ctx.Err())
			case <-time.After(lockRetryWait):
				continue
			}
		}
		if err != nil {
			if _, ok := err.(*exec.Error); ok {
				return nil, fmt.Errorf("golangci-lint: not installed or not in PATH: %w", err)
			}
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				return nil, fmt.Errorf("golangci-lint: run: %w", err)
			}
		}

		issues, err := parseGolangciJSON(out)
		if err != nil {
			return nil, fmt.Errorf("golangci-lint: parse JSON: %w (stderr: %.300s)",
				err, strings.TrimSpace(stderr.String()))
		}

		findings := make([]PreAnalysisFinding, 0, len(issues))
		for _, iss := range issues {
			issueFile := filepath.ToSlash(iss.Pos.Filename)
			if issueFile != path {
				continue
			}
			line := iss.Pos.Line
			if line <= 0 {
				continue
			}
			findings = append(findings, PreAnalysisFinding{
				ID:       Fingerprint(ToolGolangciLint, iss.FromLinter, path, line),
				Tool:     ToolGolangciLint,
				RuleID:   iss.FromLinter,
				FilePath: path,
				Line:     line,
				EndLine:  endLineOf(iss),
				Severity: severityFor(iss),
				Message:  iss.Text,
				Snippet:  snippetOf(firstSourceLine(iss), "", ""),
			})
		}
		return findings, nil
	}
}

// isParallelLockError reports whether the golangci-lint run failed because
// another golangci-lint instance holds the global lock.
func isParallelLockError(err error, out []byte, stderr *bytes.Buffer) bool {
	const marker = "parallel golangci-lint is running"
	return err != nil && len(out) == 0 &&
		strings.Contains(stderr.String(), marker)
}

// parseGolangciJSON decodes the golangci-lint v2 JSON output. The CLI may
// append a human-readable summary (e.g. "3 issues:") after the JSON, so only
// the first JSON value is decoded.
func parseGolangciJSON(data []byte) ([]golangciIssue, error) {
	start := bytes.IndexByte(data, '{')
	if start < 0 {
		return nil, fmt.Errorf("no JSON in output")
	}
	var parsed struct {
		Issues []golangciIssue `json:"Issues"`
	}
	if err := json.NewDecoder(bytes.NewReader(data[start:])).Decode(&parsed); err != nil {
		return nil, err
	}
	return parsed.Issues, nil
}

// severityFor maps a linter name to the FR-13 severity scale.
func severityFor(iss golangciIssue) string {
	if styleLinters[iss.FromLinter] {
		return "suggestion"
	}
	return "warning"
}

// endLineOf returns the multi-line end of an issue when available.
func endLineOf(iss golangciIssue) int {
	if iss.LineRange.To > iss.Pos.Line {
		return iss.LineRange.To
	}
	return iss.Pos.Line
}

// firstSourceLine returns the first reported source line.
func firstSourceLine(iss golangciIssue) string {
	if len(iss.SourceLines) == 0 {
		return ""
	}
	return iss.SourceLines[0]
}
