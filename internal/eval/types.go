// Package eval implements the offline evaluation system (v1.6): it replays
// dataset samples through the review pipeline, matches the findings against
// a (possibly incomplete) ground truth, and reports relative trends plus an
// UBV (Unexpected-But-Valid) human review queue.
//
// The public types below are FROZEN as of v1.6 (spec 13.3). Do not add or
// remove fields without a change proposal under specs/changes/.
package eval

import (
	"codereviewagent/internal/ai/agents"
)

// GroundTruthItem is one human-annotated expected finding (spec 13.3,
// FROZEN). The ground truth is allowed to be incomplete: findings the
// agents report beyond it are not counted as false positives but queued
// for human review as UBV (FR-32).
type GroundTruthItem struct {
	// FilePath is the affected file path.
	FilePath string
	// Line is the 1-based expected line number.
	Line int
	// Severity is one of critical, warning, suggestion.
	Severity string
	// Category is one of "security", "logic", "performance", "style".
	Category string
	// Message describes the expected problem.
	Message string
}

// EvalMetrics holds the aggregate evaluation metrics (spec 13.3, FROZEN).
// UnreviewedDiscovery (UBV) replaces the traditional false-positive count:
// unmatched findings await human confirmation instead of penalizing the
// score (FR-31/FR-32).
type EvalMetrics struct { //nolint:revive // name is frozen by spec 13.3
	// TruePositives is the number of ground-truth items matched by agent
	// findings.
	TruePositives int `json:"tp"`
	// FalseNegatives is the number of ground-truth items no finding
	// matched.
	FalseNegatives int `json:"fn"`
	// UnreviewedDiscovery is the number of findings unmatched by the
	// ground truth, queued for human review.
	UnreviewedDiscovery int `json:"ubv"`
	// Precision is TP / (TP + UBV): the worst-case precision assuming all
	// UBV turn out to be false positives.
	Precision float64 `json:"precision"`
	// Recall is TP / (TP + FN).
	Recall float64 `json:"recall"`
}

// EvalReport is the final evaluation report (spec 13.3, FROZEN). It is
// serialized to eval/metrics.json; the human-facing markdown lives in
// eval/report.md (FR-33).
type EvalReport struct { //nolint:revive // name is frozen by spec 13.3
	// Version identifies the evaluated version (e.g. a git tag).
	Version string `json:"version"`
	// BaselineVersion identifies the baseline the deltas are computed
	// against (empty when no baseline was given).
	BaselineVersion string `json:"baseline_version"`
	// Metrics are the aggregate metrics of this run.
	Metrics EvalMetrics `json:"metrics"`
	// PrecisionDelta is Metrics.Precision - baseline precision (positive
	// means improvement).
	PrecisionDelta float64 `json:"precision_delta"`
	// RecallDelta is Metrics.Recall - baseline recall.
	RecallDelta float64 `json:"recall_delta"`
	// UBVItems lists the findings queued for human review (FR-32).
	UBVItems []agents.AgentFinding `json:"ubv_items"`
}

// categoryOf maps a review agent name to the ground-truth category
// vocabulary: the lint agent maps to "style" (spec 13.3 Category comment).
func categoryOf(agentName string) string {
	if agentName == "lint" {
		return "style"
	}
	return agentName
}
