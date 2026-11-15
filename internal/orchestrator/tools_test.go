package orchestrator

import (
	"testing"

	"go.uber.org/zap"
)

func TestBuildToolsIncludesReadOnlySearchTools(t *testing.T) {
	o := New(zap.NewNop(), t.TempDir(), nil, nil, nil)
	tools := o.buildTools(t.TempDir())
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		names[tool.Name()] = true
	}
	for _, want := range []string{"read_file", "search_code", "get_function_definition"} {
		if !names[want] {
			t.Errorf("buildTools missing %q (AC-144): %v", want, names)
		}
	}
}
