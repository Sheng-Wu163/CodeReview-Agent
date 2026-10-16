package analysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"codereviewagent/internal/diff"
)

// DefaultSemgrepBinary is the default semgrep binary name.
const DefaultSemgrepBinary = "semgrep"

// semgrepOutput mirrors the subset of `semgrep scan --json` output that we
// consume.
type semgrepOutput struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
			Lines    string `json:"lines"`
		} `json:"extra"`
	} `json:"results"`
	Errors []struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"errors"`
}

// SemgrepAnalyzer runs the Semgrep CLI with the project's custom rule
// directory over a changed file and maps its JSON output to
// PreAnalysisFinding entries. If the binary is missing or the CLI fails,
// Analyze returns a descriptive error - never a panic.
type SemgrepAnalyzer struct {
	// Root is the directory the CLI runs in (the review workspace). Target
	// paths are interpreted relative to it.
	Root string
	// RulesDir is the directory holding custom semgrep rule files
	// (FR-18), e.g. "rules/semgrep". Relative paths resolve against Root.
	RulesDir string
	// Binary is the semgrep executable. Empty selects DefaultSemgrepBinary
	// (overridable in tests).
	Binary string
}

// Compile-time check that SemgrepAnalyzer implements the Analyzer interface.
var _ Analyzer = SemgrepAnalyzer{}

// NewSemgrepAnalyzer creates a semgrep analyzer. root is the review
// workspace; rulesDir holds the custom rules (e.g. "rules/semgrep").
func NewSemgrepAnalyzer(root, rulesDir string) SemgrepAnalyzer {
	return SemgrepAnalyzer{Root: root, RulesDir: rulesDir}
}

// Name implements Analyzer.
func (a SemgrepAnalyzer) Name() string { return ToolSemgrep }

// Analyze implements Analyzer: it scans the changed file with the custom
// rule set.
func (a SemgrepAnalyzer) Analyze(ctx context.Context, file diff.FileDiff) ([]PreAnalysisFinding, error) {
	path := file.Path
	if path == "" {
		// Deleted file: nothing left to scan.
		return nil, nil
	}
	if a.RulesDir == "" {
		return nil, fmt.Errorf("semgrep: rules directory not configured")
	}

	binary := a.Binary
	if binary == "" {
		binary = DefaultSemgrepBinary
	}
	rulesDir := a.RulesDir
	if !filepath.IsAbs(rulesDir) {
		rulesDir = filepath.Join(a.Root, rulesDir)
	}

	// The scan target must be absolute: semgrep rejects relative roots on
	// some platforms ("Invalid scanning root").
	args := []string{"scan", "--config", rulesDir, "--json", "--quiet",
		filepath.Join(a.Root, path)}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = a.Root
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()

	// Semgrep reports its own failures inside the JSON ("errors" field) and
	// exits non-zero - try to parse the output regardless of exit status.
	if err != nil {
		if _, ok := err.(*exec.Error); ok {
			return nil, fmt.Errorf("semgrep: not installed or not in PATH: %w", err)
		}
		if p, perr := parseSemgrepJSON(out); perr == nil {
			if len(p.Errors) > 0 {
				return nil, fmt.Errorf("semgrep: scan errors: %s", p.Errors[0].Message)
			}
			// Results present despite non-zero exit; use them.
			return a.toFindings(p, path), nil
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("semgrep: run: %w: %s", err, truncateForLog(msg))
		}
		return nil, fmt.Errorf("semgrep: run: %w", err)
	}

	parsed, err := parseSemgrepJSON(out)
	if err != nil {
		return nil, fmt.Errorf("semgrep: parse JSON: %w", err)
	}
	return a.toFindings(parsed, path), nil
}

// toFindings converts parsed semgrep output to PreAnalysisFinding entries
// for the scanned file.
func (a SemgrepAnalyzer) toFindings(parsed *semgrepOutput, path string) []PreAnalysisFinding {
	findings := make([]PreAnalysisFinding, 0, len(parsed.Results))
	for _, res := range parsed.Results {
		line := res.Start.Line
		if line <= 0 {
			continue
		}
		ruleID := normalizeRuleID(res.CheckID, a.RulesDir)

		snippet := res.Extra.Lines
		if snippet == "" || strings.Contains(snippet, "requires login") {
			snippet = extractSourceLine(a.Root, path, line)
		}

		findings = append(findings, PreAnalysisFinding{
			ID:       Fingerprint(ToolSemgrep, ruleID, path, line),
			Tool:     ToolSemgrep,
			RuleID:   ruleID,
			FilePath: path,
			Line:     line,
			EndLine:  res.End.Line,
			Severity: severityFromSemgrep(res.Extra.Severity),
			Message:  res.Extra.Message,
			Snippet:  snippetOf(snippet, "", ""),
		})
	}
	return findings
}

// extractSourceLine reads the given 1-based line from the file under the
// workspace root. Missing/unreadable files yield an empty string.
func extractSourceLine(root, rel string, line int) string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || line < 1 {
		return ""
	}
	lines := strings.Split(string(data), "\n")
	if line > len(lines) {
		return ""
	}
	return strings.TrimSuffix(lines[line-1], "\r")
}

// normalizeRuleID strips the rules-directory prefix that semgrep prepends to
// check_id (e.g. "codereviewagent.rules.semgrep.sql-injection-concat" with a
// rules dir named "semgrep" becomes "sql-injection-concat"), keeping
// fingerprints stable across machines.
func normalizeRuleID(checkID, rulesDir string) string {
	base := filepath.Base(rulesDir)
	if base != "" && base != "." && base != "/" {
		marker := "." + base + "."
		if idx := strings.LastIndex(checkID, marker); idx >= 0 {
			return checkID[idx+len(marker):]
		}
	}
	return checkID
}

// parseSemgrepJSON decodes the `semgrep scan --json` output.
func parseSemgrepJSON(data []byte) (*semgrepOutput, error) {
	var parsed semgrepOutput
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}

// severityFromSemgrep maps semgrep severities to the FR-13 scale.
func severityFromSemgrep(s string) string {
	switch strings.ToUpper(s) {
	case "ERROR":
		return "critical"
	case "WARNING":
		return "warning"
	default:
		return "suggestion"
	}
}

// truncateForLog shortens CLI error output for error messages.
func truncateForLog(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
