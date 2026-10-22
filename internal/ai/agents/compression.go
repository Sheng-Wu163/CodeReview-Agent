package agents

import (
	"context"

	"github.com/tmc/langchaingo/agents"
	"github.com/tmc/langchaingo/chains"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/tools"

	ctxcontext "codereviewagent/internal/context"
)

// compressingAgent wraps a LangChainGo agent and compresses older tool
// observations before every planning step (v1.9 FR-53/FR-54). It is the
// in-loop counterpart of the Harness prompt assembler: the newest
// KeepRecentRounds tool outputs stay intact, older ones become summaries.
type compressingAgent struct {
	inner       agents.Agent
	modelWindow int
	cfg         ctxcontext.CompressionConfig
}

// Compile-time check that compressingAgent implements agents.Agent.
var _ agents.Agent = (*compressingAgent)(nil)

// Plan implements agents.Agent: it compresses the intermediate steps, then
// delegates to the wrapped agent.
func (a *compressingAgent) Plan(ctx context.Context, steps []schema.AgentStep, inputs map[string]string, options ...chains.ChainCallOption) ([]schema.AgentAction, *schema.AgentFinish, error) {
	return a.inner.Plan(ctx, a.compressSteps(steps, inputs["input"]), inputs, options...)
}

// GetInputKeys implements agents.Agent.
func (a *compressingAgent) GetInputKeys() []string { return a.inner.GetInputKeys() }

// GetOutputKeys implements agents.Agent.
func (a *compressingAgent) GetOutputKeys() []string { return a.inner.GetOutputKeys() }

// GetTools implements agents.Agent.
func (a *compressingAgent) GetTools() []tools.Tool { return a.inner.GetTools() }

// compressionSetter is implemented by review agents that support v1.9
// in-loop compression. Defined in the consumer-style local scope so mocks
// remain free to ignore it.
type compressionSetter interface {
	SetCompression(modelWindow int, cfg ctxcontext.CompressionConfig)
}

// ConfigureCompression applies the v1.9 compression settings to every agent
// that supports them. It is called before reviews start.
func ConfigureCompression(reviewAgents []ReviewAgent, modelWindow int, cfg ctxcontext.CompressionConfig) {
	for _, agent := range reviewAgents {
		if setter, ok := agent.(compressionSetter); ok {
			setter.SetCompression(modelWindow, cfg)
		}
	}
}

// compressSteps turns intermediate tool observations into ContextBlocks,
// applies the sliding-window compressor and writes the compressed content
// back using Sequence (the step index), never slice positions
// (CLAUDE.v.1.9.md).
func (a *compressingAgent) compressSteps(steps []schema.AgentStep, input string) []schema.AgentStep {
	if len(steps) == 0 {
		return steps
	}

	blocks := make([]ctxcontext.ContextBlock, 0, len(steps)+1)
	blocks = append(blocks, ctxcontext.ContextBlock{
		Source:   ctxcontext.SourceTaskState,
		Content:  input,
		IsPinned: true,
		Sequence: 0,
	})
	for i, step := range steps {
		blocks = append(blocks, ctxcontext.ContextBlock{
			Source:   ctxcontext.SourceToolOutput,
			Content:  step.Observation,
			Sequence: i,
		})
	}

	if !ctxcontext.ShouldCompress(blocks, a.modelWindow, a.cfg) {
		return steps
	}

	compressed := ctxcontext.NewCompressor(a.cfg).Compress(blocks)
	bySequence := make(map[int]string, len(compressed))
	for _, block := range compressed {
		if block.Source == ctxcontext.SourceToolOutput {
			bySequence[block.Sequence] = block.Content
		}
	}

	out := make([]schema.AgentStep, len(steps))
	copy(out, steps)
	for i := range out {
		if content, ok := bySequence[i]; ok {
			out[i].Observation = content
		}
	}
	return out
}
