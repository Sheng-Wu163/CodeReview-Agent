package agents

import (
	"context"
	"testing"

	ctxcontext "codereviewagent/internal/context"
)

// TestPrepareQuestionUsesInjectedPreparer covers AC-133: agents delegate
// prompt assembly to the Harness-provided preparer.
func TestPrepareQuestionUsesInjectedPreparer(t *testing.T) {
	agent := &reviewAgent{name: "security", promptFile: "security.txt"}
	called := false
	agent.SetPromptPreparer(func(_ context.Context, blocks []ctxcontext.ContextBlock) (string, error) {
		called = true
		if len(blocks) != 2 {
			t.Errorf("blocks = %d, want SYSTEM + TASK_STATE", len(blocks))
		}
		return "prepared-by-harness", nil
	})

	got, err := agent.prepareQuestion(context.Background(), "system", ReviewInput{})
	if err != nil {
		t.Fatalf("prepareQuestion: %v", err)
	}
	if !called {
		t.Error("injected preparer was not called")
	}
	if got != "prepared-by-harness" {
		t.Errorf("question = %q, want the preparer output", got)
	}
}
