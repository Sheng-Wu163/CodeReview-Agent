package review

import (
	"reflect"
	"testing"
)

func TestSelectorTrivial(t *testing.T) {
	got := NewSelector().Select(ComplexityTrivial)

	if got.Level != ComplexityTrivial {
		t.Errorf("Level = %q, want trivial", got.Level)
	}
	if !reflect.DeepEqual(got.EnabledAgents, []string{"security", "lint"}) {
		t.Errorf("EnabledAgents = %v, want [security lint]", got.EnabledAgents)
	}
	if got.EnableVerifier {
		t.Error("Trivial must not enable the verifier")
	}
}

func TestSelectorStandardAndComplex(t *testing.T) {
	full := []string{"security", "logic", "performance", "lint"}
	for _, level := range []ComplexityLevel{ComplexityStandard, ComplexityComplex} {
		got := NewSelector().Select(level)
		if got.Level != level {
			t.Errorf("Level = %q, want %q", got.Level, level)
		}
		if !reflect.DeepEqual(got.EnabledAgents, full) {
			t.Errorf("%s EnabledAgents = %v, want %v", level, got.EnabledAgents, full)
		}
		if !got.EnableVerifier {
			t.Errorf("%s must enable the verifier", level)
		}
	}
}

func TestIsAgentEnabled(t *testing.T) {
	trivial := NewSelector().Select(ComplexityTrivial)
	if !IsAgentEnabled(trivial, "security") || !IsAgentEnabled(trivial, "lint") {
		t.Error("security and lint must be enabled for Trivial")
	}
	if IsAgentEnabled(trivial, "logic") || IsAgentEnabled(trivial, "performance") {
		t.Error("logic and performance must be disabled for Trivial")
	}
}

// TestSelectorsReturnFreshSlices guards against callers mutating the shared
// package-level strategy (determinism across runs).
func TestSelectorsReturnFreshSlices(t *testing.T) {
	s := NewSelector()
	first := s.Select(ComplexityStandard)
	first.EnabledAgents[0] = "mutated"

	second := s.Select(ComplexityStandard)
	if second.EnabledAgents[0] != "security" {
		t.Errorf("Select returned a shared slice: %v", second.EnabledAgents)
	}
}
