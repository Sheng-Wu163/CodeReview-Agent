package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap"

	"codereviewagent/internal/ai/agents"
	"codereviewagent/internal/diff"
	"codereviewagent/internal/orchestrator"
	"codereviewagent/internal/review"
)

// verifierAgent returns one deterministic finding.
type verifierAgent struct{}

func (verifierAgent) Name() string { return "security" }

func (verifierAgent) Review(_ context.Context, _ agents.ReviewInput) ([]agents.AgentFinding, error) {
	return []agents.AgentFinding{{
		Agent: "security", Severity: agents.SeverityCritical,
		FilePath: "main.go", LineNumber: 2,
		Message: "test finding", Confidence: 0.9,
	}}, nil
}

// TestE2EVerifierRunsForStandardPR covers T78/AC-134: a Standard PR routes
// findings through the configured verifier.
func TestE2EVerifierRunsForStandardPR(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	verifier := &countingVerifier{}
	orch := orchestrator.New(zap.NewNop(), root,
		[]agents.ReviewAgent{verifierAgent{}},
		func(string) orchestrator.Verifier { return verifier },
		diff.NewFilter(diff.DefaultFilterRules()))
	orch.SetReviewStrategy(fixedAssessorForTest{level: review.ComplexityStandard}, review.NewSelector())

	diffText := `diff --git a/main.go b/main.go
index 0000000..1111111 100644
--- a/main.go
+++ b/main.go
@@ -1,1 +1,2 @@
 package main
+func added() {}
`
	res, err := orch.Run(context.Background(), orchestrator.Input{
		RepoFullName: "o/r", PRNumber: 1, HeadSHA: "e2e-verifier", DiffText: diffText,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if verifier.calls != 1 {
		t.Errorf("verifier calls = %d, want 1", verifier.calls)
	}
	if len(res.Findings) != 1 {
		t.Errorf("findings = %d, want 1", len(res.Findings))
	}
}

// fixedAssessorForTest pins the complexity level.
type fixedAssessorForTest struct{ level review.ComplexityLevel }

func (f fixedAssessorForTest) Assess(_ []diff.FileDiff) review.ComplexityLevel { return f.level }
