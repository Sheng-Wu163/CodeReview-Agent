package agents

import (
	"strings"
	"testing"

	"github.com/tmc/langchaingo/schema"

	ctxcontext "codereviewagent/internal/context"
)

func TestCompressingAgentCompressesOlderSteps(t *testing.T) {
	agent := &compressingAgent{
		modelWindow: 0,
		cfg: ctxcontext.CompressionConfig{
			TriggerRatio:       0.65,
			AbsoluteTokenLimit: 5,
			KeepRecentRounds:   2,
		},
	}
	steps := []schema.AgentStep{
		{Observation: "file1.go:\nSECRET_1"},
		{Observation: "file2.go:\nSECRET_2"},
		{Observation: "file3.go:\nSECRET_3"},
		{Observation: "file4.go:\nRECENT_4"},
		{Observation: "file5.go:\nRECENT_5"},
	}

	got := agent.compressSteps(steps, "system prompt + diff")

	if !strings.Contains(got[3].Observation, "RECENT_4") || !strings.Contains(got[4].Observation, "RECENT_5") {
		t.Errorf("newest two observations must be intact: %q / %q",
			got[3].Observation, got[4].Observation)
	}
	for i := 0; i < 3; i++ {
		if strings.Contains(got[i].Observation, "SECRET_") {
			t.Errorf("step %d should be summarized, got %q", i, got[i].Observation)
		}
		if !strings.Contains(got[i].Observation, ctxcontext.CompressedPrefix) {
			t.Errorf("step %d = %q, want compressed summary", i, got[i].Observation)
		}
	}
}

func TestCompressingAgentLeavesSmallHistoryAlone(t *testing.T) {
	agent := &compressingAgent{cfg: ctxcontext.CompressionConfig{AbsoluteTokenLimit: 32000}}
	steps := []schema.AgentStep{
		{Observation: "small output 1"},
		{Observation: "small output 2"},
	}
	got := agent.compressSteps(steps, "input")
	if got[0].Observation != steps[0].Observation || got[1].Observation != steps[1].Observation {
		t.Errorf("small history must not be compressed: %+v", got)
	}
}
