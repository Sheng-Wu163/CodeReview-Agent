package orchestrator

import (
	"context"

	ltools "github.com/tmc/langchaingo/tools"
)

// SSE review-stream event types (v1.7 FR-44).
const (
	// EventStatus marks a pipeline stage transition.
	EventStatus = "status"
	// EventAgentStart marks one review agent beginning its run.
	EventAgentStart = "agent_start"
	// EventToolCall marks an agent invoking a context tool.
	EventToolCall = "tool_call"
	// EventFinding marks a finding produced by an agent.
	EventFinding = "finding"
	// EventDone marks the end of the review run.
	EventDone = "done"
)

// ReviewEvent is one structured event pushed on the review stream
// (v1.7 FR-44). The wire shape is {"event": "<type>", "data": {...}}.
type ReviewEvent struct {
	// Event is one of the Event* constants.
	Event string `json:"event"`
	// Data carries the event payload (type-specific).
	Data map[string]any `json:"data"`
}

// eventChan is the optional event sink wired via SetEventChan. Nil means
// event emission is disabled (zero overhead).
type eventChan chan<- ReviewEvent

// emitEvent delivers ev to the sink without blocking: a slow or absent SSE
// consumer must never stall the review pipeline.
func (o *Orchestrator) emitEvent(ev ReviewEvent) {
	if o.events == nil {
		return
	}
	select {
	case o.events <- ev:
	default: // consumer slow or gone: drop the event
	}
}

// emittingTool wraps a review tool and emits a tool_call event for every
// invocation, tagging it with the calling agent's name (v1.7 FR-44).
type emittingTool struct {
	inner ltools.Tool
	agent string
	emit  func(ev ReviewEvent)
}

// Compile-time check that emittingTool implements tools.Tool.
var _ ltools.Tool = emittingTool{}

// Name implements tools.Tool.
func (t emittingTool) Name() string { return t.inner.Name() }

// Description implements tools.Tool.
func (t emittingTool) Description() string { return t.inner.Description() }

// Call implements tools.Tool: it emits the tool_call event, then delegates.
func (t emittingTool) Call(ctx context.Context, input string) (string, error) {
	if t.emit != nil {
		t.emit(ReviewEvent{Event: EventToolCall, Data: map[string]any{
			"agent": t.agent, "tool": t.inner.Name(), "input": input,
		}})
	}
	return t.inner.Call(ctx, input)
}

// wrapTools copies the tool slice, wrapping every tool so its calls are
// reported to the event stream under the given agent name.
func wrapTools(tools []ltools.Tool, agent string, emit func(ReviewEvent)) []ltools.Tool {
	out := make([]ltools.Tool, 0, len(tools))
	for _, tl := range tools {
		out = append(out, emittingTool{inner: tl, agent: agent, emit: emit})
	}
	return out
}
