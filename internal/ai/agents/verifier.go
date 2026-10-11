package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"

	"codereviewagent/internal/diff"
)

// Verifier runs adversarial verification over the raw findings of all review
// agents (FR-10): line number validation, hallucination filtering and
// deduplication, producing VerifiedFinding entries with calibrated
// confidence scores.
type Verifier struct {
	// readFile is the read_file tool used to check that cited code snippets
	// exist in the workspace (hallucination filter).
	readFile tools.Tool
	// llm is optionally used to calibrate the final confidence score of
	// each surviving finding. When nil, deterministic scoring is used.
	llm llms.Model
}

// VerifiedFinding is an AgentFinding that survived verification.
type VerifiedFinding struct {
	AgentFinding
	// Confidence is the final calibrated score in [0,1].
	Confidence float64
}

// NewVerifier creates a Verifier. readFile must be a ReadFileTool bound to
// the review workspace. llm may be nil.
func NewVerifier(readFile tools.Tool, llm llms.Model) *Verifier {
	return &Verifier{readFile: readFile, llm: llm}
}

// dedupeKey groups findings that describe the same problem: same file, same
// line, same severity ("问题类型" as reported by the agents).
type dedupeKey struct {
	filePath string
	line     int
	severity string
}

// Verify runs the three verification steps and returns the surviving
// findings, deduplicated and with calibrated confidence.
func (v *Verifier) Verify(ctx context.Context, findings []AgentFinding, diffs []diff.FileDiff) ([]VerifiedFinding, error) {
	// Step 1: line number validation (drop findings outside diff ranges).
	valid := make([]AgentFinding, 0, len(findings))
	for _, f := range findings {
		if !lineInDiff(f, diffs) {
			continue
		}
		valid = append(valid, f)
	}

	// Step 2: hallucination filter (drop findings whose cited snippet does
	// not exist in the source file).
	scores := make(map[int]float64, len(valid))
	kept := make([]AgentFinding, 0, len(valid))
	for _, f := range valid {
		base := f.Confidence
		if base <= 0 || base > 1 {
			base = 0.5
		}
		bonus, ok, err := v.verifySnippet(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("verify finding %s:%d: %w", f.FilePath, f.LineNumber, err)
		}
		if !ok {
			continue // hallucination
		}
		scores[len(kept)] = clamp01(base + bonus)
		kept = append(kept, f)
	}

	// Step 3: deduplication. Later agents corroborating the same issue boost
	// the confidence of the surviving finding.
	type entry struct {
		finding AgentFinding
		score   float64
		copies  int
	}
	merged := make(map[dedupeKey]*entry)
	var order []*entry
	for i, f := range kept {
		key := dedupeKey{f.FilePath, f.LineNumber, f.Severity}
		if e, ok := merged[key]; ok {
			e.copies++
			if scores[i] > e.score {
				// Keep the better-worded (higher confidence) variant.
				e.score = scores[i]
			}
			continue
		}
		e := &entry{finding: f, score: scores[i], copies: 1}
		merged[key] = e
		order = append(order, e)
	}

	out := make([]VerifiedFinding, 0, len(order))
	for _, e := range order {
		score := e.score + 0.05*float64(e.copies-1)
		if v.llm != nil {
			llmScore, err := v.llmConfidence(ctx, e.finding)
			if err != nil {
				return nil, fmt.Errorf("verify confidence for %s:%d: %w",
					e.finding.FilePath, e.finding.LineNumber, err)
			}
			score = (score + llmScore) / 2
		}
		out = append(out, VerifiedFinding{
			AgentFinding: e.finding,
			Confidence:   clamp01(score),
		})
	}
	return out, nil
}

// verifySnippet checks that the finding's cited code snippet exists in the
// source file. It returns a confidence bonus (0.05 partial, 0.1 exact) and
// ok=false when the snippet cannot be found (hallucination).
func (v *Verifier) verifySnippet(ctx context.Context, f AgentFinding) (bonus float64, ok bool, err error) {
	snippet := strings.TrimSpace(f.CodeSnippet)
	if snippet == "" {
		// Nothing to verify; do not punish agents that omit snippets.
		return 0, true, nil
	}
	content, err := v.readFile.Call(ctx, f.FilePath)
	if err != nil {
		return 0, false, nil // unreadable file: treat as hallucination
	}

	if containsNormalized(content, snippet) {
		return 0.1, true, nil
	}
	// Fall back to single-line matching: the snippet may span lines that
	// were edited independently.
	for _, line := range strings.Split(snippet, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && containsNormalized(content, line) {
			return 0.05, true, nil
		}
	}
	return 0, false, nil
}

// llmConfidence asks the LLM to score the finding and returns the parsed
// score in [0,1].
func (v *Verifier) llmConfidence(ctx context.Context, f AgentFinding) (float64, error) {
	system, err := loadPrompt("verifier.txt")
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(f)
	if err != nil {
		return 0, fmt.Errorf("marshal finding: %w", err)
	}
	resp, err := v.llm.GenerateContent(ctx, []llms.MessageContent{
		{Role: llms.ChatMessageTypeHuman, Parts: []llms.ContentPart{
			llms.TextContent{Text: system + "\n\n=== FINDING ===\n" + string(payload)},
		}},
	})
	if err != nil {
		return 0, fmt.Errorf("llm call: %w", err)
	}
	if len(resp.Choices) == 0 {
		return 0, fmt.Errorf("llm returned no choices")
	}
	var score float64
	if err := json.Unmarshal([]byte(strings.TrimSpace(resp.Choices[0].Content)), &score); err != nil {
		return 0, fmt.Errorf("parse confidence %q: %w", resp.Choices[0].Content, err)
	}
	return clamp01(score), nil
}

// lineInDiff reports whether the finding's line number falls inside one of
// the diff ranges of the same file (FR-10 step 1). Deleted files carry their
// path in OldPath, so both spellings are accepted.
func lineInDiff(f AgentFinding, diffs []diff.FileDiff) bool {
	if f.FilePath == "" || f.LineNumber <= 0 {
		return false
	}
	for _, fd := range diffs {
		fdPath := fd.Path
		if fdPath == "" {
			fdPath = fd.OldPath
		}
		if fdPath != f.FilePath {
			continue
		}
		for _, h := range fd.Hunks {
			if h.OldLines > 0 && f.LineNumber >= h.OldStartLine && f.LineNumber < h.OldStartLine+h.OldLines {
				return true
			}
			if h.NewLines > 0 && f.LineNumber >= h.NewStartLine && f.LineNumber < h.NewStartLine+h.NewLines {
				return true
			}
		}
	}
	return false
}

// containsNormalized reports whether s contains needle, ignoring all
// whitespace differences.
func containsNormalized(s, needle string) bool {
	norm := func(x string) string {
		var b strings.Builder
		for _, r := range x {
			switch r {
			case ' ', '\t', '\r', '\n':
			default:
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return strings.Contains(norm(s), norm(needle))
}

// clamp01 constrains x to [0,1].
func clamp01(x float64) float64 {
	return math.Min(1, math.Max(0, x))
}
