package review

import (
	"testing"

	"codereviewagent/internal/ai/agents"
)

func verified(agent, severity, path, message string) agents.VerifiedFinding {
	return agents.VerifiedFinding{
		AgentFinding: agents.AgentFinding{
			Agent: agent, Severity: severity, FilePath: path, LineNumber: 10,
			Message: message, Confidence: 0.9,
		},
		Confidence: 0.9,
	}
}

func thread(id, fingerprint string, resolved bool) ThreadState {
	return ThreadState{ThreadID: id, Fingerprint: fingerprint, Path: "main.go", Line: 10, IsResolved: resolved}
}

func TestPlanPublishNewFindingOnly(t *testing.T) {
	f := verified("security", agents.SeverityWarning, "main.go", "issue")
	plan := PlanPublish([]agents.VerifiedFinding{f}, nil, nil)
	if len(plan.New) != 1 || len(plan.Persisting) != 0 || len(plan.Resolved) != 0 {
		t.Fatalf("plan = %+v, want only one new finding", plan)
	}
	if plan.Event != "COMMENT" {
		t.Errorf("Event = %q, want COMMENT", plan.Event)
	}
}

// AC-126: a finding with a matching existing thread is not re-commented.
func TestPlanPublishPersistingNotReCommented(t *testing.T) {
	f := verified("security", agents.SeverityWarning, "main.go", "issue")
	fp := EffectiveFingerprint(f)
	plan := PlanPublish([]agents.VerifiedFinding{f}, []ThreadState{thread("t1", fp, false)}, nil)
	if len(plan.New) != 0 {
		t.Errorf("New = %d, want 0 duplicate comments", len(plan.New))
	}
	if len(plan.Persisting) != 1 {
		t.Errorf("Persisting = %d, want 1", len(plan.Persisting))
	}
}

// AC-127: only the genuinely new finding is submitted inline.
func TestPlanPublishOnlyNewFindingSubmitted(t *testing.T) {
	existing := verified("logic", agents.SeverityWarning, "main.go", "existing")
	fresh := verified("security", agents.SeverityCritical, "main.go", "brand new")
	plan := PlanPublish([]agents.VerifiedFinding{existing, fresh},
		[]ThreadState{thread("t1", EffectiveFingerprint(existing), false)}, nil)
	if len(plan.New) != 1 || plan.New[0].Message != "brand new" {
		t.Errorf("New = %+v, want only the new finding", plan.New)
	}
	if len(plan.Persisting) != 1 {
		t.Errorf("Persisting = %d, want 1", len(plan.Persisting))
	}
}

// AC-130: a resolved critical does not block the PR any more.
func TestPlanPublishResolvedCriticalUsesComment(t *testing.T) {
	critical := verified("security", agents.SeverityCritical, "main.go", "fixed sql")
	fp := EffectiveFingerprint(critical)
	plan := PlanPublish(nil, []ThreadState{thread("t1", fp, false)},
		map[string]agents.PriorFindingVerdict{fp: agents.VerdictFixed})
	if len(plan.Resolved) != 1 {
		t.Fatalf("Resolved = %d, want 1", len(plan.Resolved))
	}
	if plan.Event != "COMMENT" {
		t.Errorf("Event = %q, want COMMENT for a fixed prior critical", plan.Event)
	}
}

// AC-132: a still_present accepted_implicit finding keeps its thread open.
func TestPlanPublishStillPresentCriticalRequestsChanges(t *testing.T) {
	f := verified("security", agents.SeverityCritical, "main.go", "still broken")
	fp := EffectiveFingerprint(f)
	plan := PlanPublish([]agents.VerifiedFinding{f},
		[]ThreadState{thread("t1", fp, false)},
		map[string]agents.PriorFindingVerdict{fp: agents.VerdictStillPresent})
	if len(plan.Resolved) != 0 {
		t.Errorf("Resolved = %d, want 0 for still_present", len(plan.Resolved))
	}
	if plan.Event != "REQUEST_CHANGES" {
		t.Errorf("Event = %q, want REQUEST_CHANGES", plan.Event)
	}
}

// Same-fingerprint duplicates are matched by occurrence index.
func TestPlanPublishDuplicateFingerprintBuckets(t *testing.T) {
	a := verified("lint", agents.SeverityWarning, "main.go", "duplicate")
	b := a
	fp := EffectiveFingerprint(a)
	threads := []ThreadState{thread("t1", fp, false), thread("t2", fp, false)}

	plan := PlanPublish([]agents.VerifiedFinding{a, b}, threads, nil)
	if len(plan.New) != 0 || len(plan.Persisting) != 2 {
		t.Errorf("plan = %+v, want both findings persisting", plan)
	}

	plan = PlanPublish([]agents.VerifiedFinding{a}, threads, nil)
	if len(plan.New) != 0 || len(plan.Persisting) != 1 || len(plan.Resolved) != 1 {
		t.Errorf("plan = %+v, want one persisting + one resolved thread", plan)
	}
}

// A human-resolved thread is never selected for system resolution.
func TestPlanPublishIgnoresHumanResolvedThread(t *testing.T) {
	fp := FindingFingerprint("main.go", "security", "old issue")
	plan := PlanPublish(nil, []ThreadState{thread("t1", fp, true)}, nil)
	if len(plan.Resolved) != 0 {
		t.Errorf("Resolved = %d, want 0 for an already resolved thread", len(plan.Resolved))
	}
}
