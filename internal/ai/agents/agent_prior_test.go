package agents

import (
	"strings"
	"testing"
)

// AC-120: the first review has no PRIOR_FINDINGS section.
func TestBuildReviewQuestionOmitsPriorWhenEmpty(t *testing.T) {
	question := buildReviewQuestion("system", ReviewInput{Scope: "full"})
	if strings.Contains(question, "PRIOR_FINDINGS") {
		t.Errorf("first review must not include PRIOR_FINDINGS:\n%s", question)
	}
}

func TestBuildReviewQuestionInjectsPriorFindings(t *testing.T) {
	question := buildReviewQuestion("system", ReviewInput{
		Scope: "incremental",
		PriorFindings: []PriorFinding{{
			Fingerprint: "abc", Agent: "security", FilePath: "main.go",
			LineNumber: 10, Severity: "critical", Message: "SQL injection",
		}},
	})
	for _, want := range []string{"## 审查范围", "PRIOR_FINDINGS", "abc", "SQL injection", "fixed"} {
		if !strings.Contains(question, want) {
			t.Errorf("question missing %q:\n%s", want, question)
		}
	}
}

// AC-121/AC-122: fixed requires evidence, otherwise it is downgraded.
func TestEnforcePriorVerdict(t *testing.T) {
	fixed := &AgentFinding{PriorFingerprint: "fp", Verdict: VerdictFixed, Evidence: "current code"}
	enforcePriorVerdict(fixed)
	if fixed.Verdict != VerdictFixed {
		t.Errorf("fixed with evidence verdict = %q, want fixed", fixed.Verdict)
	}

	noEvidence := &AgentFinding{PriorFingerprint: "fp", Verdict: VerdictFixed}
	enforcePriorVerdict(noEvidence)
	if noEvidence.Verdict != VerdictUnverifiable {
		t.Errorf("fixed without evidence verdict = %q, want unverifiable", noEvidence.Verdict)
	}

	unknown := &AgentFinding{PriorFingerprint: "fp", Verdict: "maybe"}
	enforcePriorVerdict(unknown)
	if unknown.Verdict != VerdictUnverifiable {
		t.Errorf("unknown verdict = %q, want unverifiable", unknown.Verdict)
	}

	normal := &AgentFinding{Verdict: VerdictFixed}
	enforcePriorVerdict(normal)
	if normal.Verdict != VerdictFixed {
		t.Errorf("ordinary finding must keep its verdict field, got %q", normal.Verdict)
	}
}
