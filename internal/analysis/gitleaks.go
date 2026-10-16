package analysis

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/detect"
	"github.com/zricethezav/gitleaks/v8/report"

	"codereviewagent/internal/diff"
)

// reportFinding is the finding type returned by the gitleaks detector.
type reportFinding = report.Finding

// SecretRule is a user-defined secret detection rule that extends the
// gitleaks default ruleset (FR-18). Loaded from the static_analysis config.
type SecretRule struct {
	// ID is the rule identifier reported in PreAnalysisFinding.RuleID.
	ID string
	// Description is a human-readable description of the rule.
	Description string
	// Pattern is the regular expression matching the secret.
	Pattern string
	// Keywords are quick pre-filter strings (optional).
	Keywords []string
}

// GitleaksAnalyzer scans changed files for hardcoded secrets using gitleaks
// as an embedded library (FR-18). The default ruleset is always enabled and
// can be extended with custom SecretRules.
type GitleaksAnalyzer struct {
	// Root is the workspace root the scanned files live in.
	Root string

	// cfg is the default ruleset merged with the custom rules, built once at
	// construction time.
	cfg config.Config
}

// Compile-time check that GitleaksAnalyzer implements the Analyzer interface.
var _ Analyzer = GitleaksAnalyzer{}

// NewGitleaksAnalyzer creates a secret scanner with the default gitleaks
// ruleset plus the given custom rules. root is the review workspace.
func NewGitleaksAnalyzer(root string, extraRules []SecretRule) (*GitleaksAnalyzer, error) {
	// NewDetectorDefaultConfig loads gitleaks' embedded default ruleset.
	base, err := detect.NewDetectorDefaultConfig()
	if err != nil {
		return nil, fmt.Errorf("gitleaks: load default rules: %w", err)
	}
	cfg := base.Config

	for _, r := range extraRules {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return nil, fmt.Errorf("gitleaks: compile custom rule %q: %w", r.ID, err)
		}
		cfg.Rules[r.ID] = config.Rule{
			RuleID:      r.ID,
			Description: r.Description,
			Regex:       re,
			Keywords:    r.Keywords,
			Tags:        []string{"custom"},
		}
		// Rule-level keywords must also be registered in the global keyword
		// index, otherwise the prefilter trie never activates the rule.
		if cfg.Keywords == nil {
			cfg.Keywords = make(map[string]struct{})
		}
		for _, k := range r.Keywords {
			cfg.Keywords[k] = struct{}{}
		}
		if cfg.OrderedRules != nil {
			cfg.OrderedRules = append(cfg.OrderedRules, r.ID)
		}
	}

	return &GitleaksAnalyzer{Root: root, cfg: cfg}, nil
}

// Name implements Analyzer.
func (a GitleaksAnalyzer) Name() string { return ToolGitleaks }

// Analyze scans one changed file for hardcoded secrets. Deleted files have
// no content to scan and yield no findings. All secret findings are
// reported as critical (FR-18).
func (a GitleaksAnalyzer) Analyze(ctx context.Context, file diff.FileDiff) ([]PreAnalysisFinding, error) {
	path := file.Path
	if path == "" {
		// Deleted file: the content no longer exists in the workspace.
		return nil, nil
	}

	full, err := resolveWorkspaceFile(a.Root, path)
	if err != nil {
		return nil, fmt.Errorf("gitleaks: %w", err)
	}
	content, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("gitleaks: read %q: %w", path, err)
	}

	detector := detect.NewDetectorContext(ctx, a.cfg)
	reported := detector.DetectContext(ctx, detect.Fragment{
		Raw:       string(content),
		Bytes:     content,
		FilePath:  filepath.ToSlash(path),
		StartLine: 1, // report findings with 1-based line numbers
	})

	findings := make([]PreAnalysisFinding, 0, len(reported))
	for _, rf := range reported {
		findings = append(findings, PreAnalysisFinding{
			ID:       Fingerprint(ToolGitleaks, rf.RuleID, path, rf.StartLine),
			Tool:     ToolGitleaks,
			RuleID:   rf.RuleID,
			FilePath: path,
			Line:     rf.StartLine,
			EndLine:  rf.EndLine,
			Severity: "critical",
			Message:  secretMessage(rf),
			Snippet:  snippetOf(rf.Line, rf.Secret, rf.Match),
		})
	}
	return findings, nil
}

// secretMessage renders the finding description, falling back when a rule
// has no description.
func secretMessage(rf reportFinding) string {
	if rf.Description != "" {
		return "Hardcoded secret detected: " + rf.Description
	}
	return "Hardcoded secret detected (rule " + rf.RuleID + ")"
}

// snippetOf picks the best available snippet (matched line first), trimmed
// to at most 160 characters.
func snippetOf(line, secret, match string) string {
	s := strings.TrimSpace(line)
	if s == "" {
		s = strings.TrimSpace(secret)
	}
	if s == "" {
		s = strings.TrimSpace(match)
	}
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

// resolveWorkspaceFile joins the workspace root with a relative file path,
// mirroring the safety rules of the AI tools.
func resolveWorkspaceFile(root, rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty file path")
	}
	if filepath.IsAbs(rel) || strings.HasPrefix(rel, "/") {
		return "", fmt.Errorf("absolute paths are not allowed: %q", rel)
	}
	full := filepath.Clean(filepath.Join(root, rel))
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", rel, err)
	}
	if !strings.HasPrefix(abs, rootAbs+string(os.PathSeparator)) && abs != rootAbs {
		return "", fmt.Errorf("path %q escapes the workspace", rel)
	}
	return abs, nil
}
