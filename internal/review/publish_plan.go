package review

import (
	"sort"

	"codereviewagent/internal/ai/agents"
)

// CommentDisposition classifies one finding/thread identity (v1.11 FR-67).
type CommentDisposition string

const (
	// DispositionNew means the finding has no matching existing thread.
	DispositionNew CommentDisposition = "new"
	// DispositionPersisting means a matching thread already exists.
	DispositionPersisting CommentDisposition = "persisting"
	// DispositionResolved means a thread exists but the current round no
	// longer reports the finding.
	DispositionResolved CommentDisposition = "resolved"
)

// ThreadState is one GitHub review thread (v1.11 spec 21.3, FROZEN).
type ThreadState struct {
	ThreadID    string
	Fingerprint string
	Path        string
	Line        int
	IsResolved  bool
	IsOutdated  bool
}

// PublishPlan is the deduplicated review publication plan (v1.11 spec 21.3,
// FROZEN).
type PublishPlan struct {
	New        []agents.VerifiedFinding
	Persisting []agents.VerifiedFinding
	Resolved   []ThreadState
	Event      string // COMMENT | REQUEST_CHANGES
}

// EffectiveFingerprint returns the identity used for deduplication: a
// still_present PRIOR_FINDING keeps the previous fingerprint, while new
// findings are fingerprinted from their current content (FR-66).
func EffectiveFingerprint(f agents.VerifiedFinding) string {
	if f.PriorFingerprint != "" {
		return f.PriorFingerprint
	}
	return FindingFingerprint(f.FilePath, f.Agent, f.Message)
}

// PlanPublish computes the publication plan as a pure function: no I/O, no
// network (FR-67/FR-69).
//
// Findings and threads are matched by fingerprint + occurrence index so
// repeated issues in the same file are neither lost nor duplicated. Threads
// left unmatched and still open become candidates for FR-68 resolution.
func PlanPublish(findings []agents.VerifiedFinding, threads []ThreadState, verdicts map[string]agents.PriorFindingVerdict) PublishPlan {
	_ = verdicts // resolution is only applied for the fixed verdict by the caller

	threadsByFingerprint := make(map[string][]ThreadState)
	for _, thread := range threads {
		if thread.Fingerprint == "" {
			continue
		}
		threadsByFingerprint[thread.Fingerprint] = append(threadsByFingerprint[thread.Fingerprint], thread)
	}

	used := make(map[string]int)
	var plan PublishPlan
	unresolvedCritical := false

	for _, finding := range findings {
		fingerprint := EffectiveFingerprint(finding)
		index := used[fingerprint]
		used[fingerprint] = index + 1

		matches := threadsByFingerprint[fingerprint]
		if index < len(matches) {
			thread := matches[index]
			plan.Persisting = append(plan.Persisting, finding)
			if finding.Severity == agents.SeverityCritical && !thread.IsResolved {
				unresolvedCritical = true
			}
			continue
		}

		plan.New = append(plan.New, finding)
		if finding.Severity == agents.SeverityCritical {
			unresolvedCritical = true
		}
	}

	for fingerprint, matches := range threadsByFingerprint {
		for i := used[fingerprint]; i < len(matches); i++ {
			thread := matches[i]
			if thread.IsResolved {
				continue
			}
			plan.Resolved = append(plan.Resolved, thread)
		}
	}
	sort.SliceStable(plan.Resolved, func(i, j int) bool {
		if plan.Resolved[i].Path != plan.Resolved[j].Path {
			return plan.Resolved[i].Path < plan.Resolved[j].Path
		}
		if plan.Resolved[i].Line != plan.Resolved[j].Line {
			return plan.Resolved[i].Line < plan.Resolved[j].Line
		}
		return plan.Resolved[i].ThreadID < plan.Resolved[j].ThreadID
	})

	plan.Event = "COMMENT"
	if unresolvedCritical {
		plan.Event = "REQUEST_CHANGES"
	}
	return plan
}
