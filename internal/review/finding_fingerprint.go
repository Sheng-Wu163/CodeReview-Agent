package review

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// lineReferenceRe matches Chinese line references such as "第 12 行" or
// "第12行" that must not influence the fingerprint (FR-66).
var lineReferenceRe = regexp.MustCompile(`第\s*\d+\s*行`)

// FindingFingerprint returns the stable identity of an LLM finding
// (v1.11 FR-66). It is a pure function: the fingerprint is
// SHA-256(normalized file path | agent | normalized message) and never
// includes the line number, so moved code keeps its identity.
func FindingFingerprint(filePath, agent, message string) string {
	normalizedPath := normalizePath(filePath)
	normalizedAgent := strings.TrimSpace(agent)
	normalizedMessage := normalizeMessage(message)
	sum := sha256.Sum256([]byte(normalizedPath + "|" + normalizedAgent + "|" + normalizedMessage))
	return hex.EncodeToString(sum[:])
}

// normalizePath trims whitespace and uses forward slashes for stable output
// across operating systems.
func normalizePath(filePath string) string {
	p := strings.ReplaceAll(strings.TrimSpace(filePath), "\\", "/")
	p = strings.TrimPrefix(p, "./")
	return p
}

// normalizeMessage trims, collapses consecutive whitespace, and removes
// "第 N 行" line references.
func normalizeMessage(message string) string {
	message = lineReferenceRe.ReplaceAllString(message, "")
	return strings.Join(strings.Fields(message), " ")
}
