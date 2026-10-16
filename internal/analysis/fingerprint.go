package analysis

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Fingerprint builds the stable finding identifier (FR-16):
// <tool>:<ruleID>:<filePath>:<line>. It is a pure function: the same inputs
// always produce the same output, with no side effects. The format is
// frozen - changing it requires a change proposal.
func Fingerprint(tool, ruleID, filePath string, line int) string {
	return tool + ":" + ruleID + ":" + filePath + ":" + strconv.Itoa(line)
}

// AssignID sets the finding's ID from the fingerprint of its fields.
func AssignID(f *PreAnalysisFinding) {
	f.ID = Fingerprint(f.Tool, f.RuleID, f.FilePath, f.Line)
}

// MarshalPreFindings serializes pre-analysis findings to a JSON string for
// injection into agent prompts. A nil/empty slice marshals to "[]" (never
// "null").
func MarshalPreFindings(findings []PreAnalysisFinding) (string, error) {
	if len(findings) == 0 {
		return "[]", nil
	}
	data, err := json.Marshal(findings)
	if err != nil {
		return "", fmt.Errorf("analysis: marshal preFindings: %w", err)
	}
	return string(data), nil
}

// UnmarshalPreFindings parses pre-analysis findings serialized by
// MarshalPreFindings.
func UnmarshalPreFindings(data string) ([]PreAnalysisFinding, error) {
	findings := []PreAnalysisFinding{}
	if err := json.Unmarshal([]byte(data), &findings); err != nil {
		return nil, fmt.Errorf("analysis: unmarshal preFindings: %w", err)
	}
	return findings, nil
}
