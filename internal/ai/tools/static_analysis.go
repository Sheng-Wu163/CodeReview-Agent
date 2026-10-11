package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/tmc/langchaingo/tools"
)

// Default static analysis commands.
const (
	// DefaultLintCommand is the Go static analysis tool run by
	// RunStaticAnalysisTool when no command is configured.
	DefaultLintCommand = "golangci-lint"
	// defaultLintArgs are the default arguments for DefaultLintCommand.
	defaultLintArgs = "run ./... --output.text.timeout 5m"
)

// notAvailableNotice is returned (as output, not an error) when the static
// analysis binary is not installed, so agents degrade gracefully to LLM-only
// review (AC-23).
const notAvailableNotice = "static analysis tool not available, falling back to LLM-only review"

// RunStaticAnalysisTool runs a static analysis CLI (e.g. golangci-lint or
// semgrep) inside the review workspace and returns its combined output
// (FR-08). When the tool is not installed it reports a fallback notice
// instead of failing the agent run (AC-23).
type RunStaticAnalysisTool struct {
	// Root is the absolute path of the review workspace, used as the
	// working directory of the analysis command.
	Root string
	// Command is the analysis binary to run. Empty selects golangci-lint.
	Command string
	// Args are the arguments passed to Command. Ignored when Command is
	// empty (golangci-lint defaults are used).
	Args []string
}

// Compile-time check that RunStaticAnalysisTool implements tools.Tool
// (FR-11).
var _ tools.Tool = RunStaticAnalysisTool{}

// Name implements tools.Tool.
func (t RunStaticAnalysisTool) Name() string { return "run_static_analysis" }

// Description implements tools.Tool.
func (t RunStaticAnalysisTool) Description() string {
	return "Run the project's static analysis tool (golangci-lint) on the code under review " +
		"and return its findings. Input is ignored; pass an empty string."
}

// Call implements tools.Tool. The input parameter is ignored.
func (t RunStaticAnalysisTool) Call(ctx context.Context, _ string) (string, error) {
	command := t.Command
	args := t.Args
	if command == "" {
		command = DefaultLintCommand
		args = strings.Fields(defaultLintArgs)
	}

	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = t.Root
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if isNotFound(err) {
			return notAvailableNotice, nil
		}
		// Analysis tools exit non-zero when they find issues; their output
		// is still the result we want.
		out := strings.TrimSpace(stdout.String())
		errOut := strings.TrimSpace(stderr.String())
		if out != "" || errOut != "" {
			return out + errOut, nil
		}
		return "", fmt.Errorf("run_static_analysis %s: %w", command, err)
	}
	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())
	if out == "" && errOut == "" {
		return "no static analysis findings", nil
	}
	return out + errOut, nil
}

// isNotFound reports whether err indicates the executable does not exist.
func isNotFound(err error) bool {
	if err == exec.ErrNotFound {
		return true
	}
	if ee, ok := err.(*exec.Error); ok {
		return ee.Err == exec.ErrNotFound
	}
	return false
}
