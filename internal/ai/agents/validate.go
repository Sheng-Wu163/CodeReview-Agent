package agents

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"codereviewagent/internal/diff"
)

// FindingValidator validates agent output before verification (v1.13
// FR-75).
type FindingValidator interface {
	Validate(findings []AgentFinding, diffLines map[string]map[int]bool) (kept []AgentFinding, dropped int)
}

// StructuralValidator implements FindingValidator using the diff's file set
// and new-file line ranges.
type StructuralValidator struct{}

// Compile-time check that StructuralValidator implements FindingValidator.
var _ FindingValidator = (*StructuralValidator)(nil)

// NewStructuralValidator creates the default structural validator.
func NewStructuralValidator() *StructuralValidator { return &StructuralValidator{} }

// BuildDiffLineIndex maps normalized file paths to their valid new-file line
// numbers.
func BuildDiffLineIndex(diffs []diff.FileDiff) map[string]map[int]bool {
	index := make(map[string]map[int]bool)
	for _, file := range diffs {
		filePath := file.Path
		if filePath == "" {
			filePath = file.OldPath
		}
		filePath = normalizeFindingPath(filePath)
		if filePath == "" {
			continue
		}
		lines := index[filePath]
		if lines == nil {
			lines = make(map[int]bool)
			index[filePath] = lines
		}
		for _, hunk := range file.Hunks {
			for line := hunk.NewStartLine; line < hunk.NewStartLine+hunk.NewLines; line++ {
				lines[line] = true
			}
		}
	}
	return index
}

// Validate implements FindingValidator. PRIOR_FINDING verdict records are
// exempt because they may legitimately reference files outside an
// incremental diff.
func (*StructuralValidator) Validate(findings []AgentFinding, diffLines map[string]map[int]bool) ([]AgentFinding, int) {
	if len(diffLines) == 0 {
		// No diff information (e.g. a direct unit invocation): keep output
		// rather than dropping every finding.
		return findings, 0
	}
	kept := make([]AgentFinding, 0, len(findings))
	dropped := 0
	for _, finding := range findings {
		if finding.PriorFingerprint != "" {
			kept = append(kept, finding)
			continue
		}
		if !validSeverity(finding.Severity) {
			dropped++
			continue
		}
		filePath := normalizeFindingPath(finding.FilePath)
		lines, ok := diffLines[filePath]
		if !ok || !lines[finding.LineNumber] {
			dropped++
			continue
		}
		kept = append(kept, finding)
	}
	return kept, dropped
}

// validSeverity reports whether severity is one of the frozen values.
func validSeverity(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case SeverityCritical, SeverityWarning, SeveritySuggestion:
		return true
	default:
		return false
	}
}

// normalizeFindingPath normalizes ./ and Windows separators for diff-set
// membership checks.
func normalizeFindingPath(filePath string) string {
	p := strings.ReplaceAll(strings.TrimSpace(filePath), "\\", "/")
	p = strings.TrimPrefix(p, "./")
	p = path.Clean(p)
	if p == "." || p == "/" {
		return ""
	}
	return strings.TrimPrefix(p, "/")
}

// allowedFindingFields is the v1.13 output whitelist.
var allowedFindingFields = map[string]bool{
	"agent": true, "severity": true, "file_path": true, "line_number": true,
	"message": true, "code_snippet": true, "suggestion": true, "confidence": true,
	"prior_fingerprint": true, "verdict": true, "evidence": true,
	"unverified_citation": true,
}

// StripNonWhitelistedFindingFields removes tool-call/instruction-style keys
// from one finding JSON object (AC-141).
func StripNonWhitelistedFindingFields(raw []byte) ([]byte, int, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, 0, fmt.Errorf("strip finding fields: %w", err)
	}
	stripped := 0
	for key := range fields {
		if !allowedFindingFields[key] {
			delete(fields, key)
			stripped++
		}
	}
	clean, err := json.Marshal(fields)
	if err != nil {
		return nil, stripped, fmt.Errorf("marshal sanitized finding: %w", err)
	}
	return clean, stripped, nil
}
