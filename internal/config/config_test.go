package config

import "testing"

// AC-134: enabling the verifier with a nil LLM must fail fast.
func TestValidateVerifierRequiresLLM(t *testing.T) {
	cfg := &Config{Verifier: VerifierConfig{Enabled: true}}
	if err := cfg.ValidateVerifier(false); err == nil {
		t.Fatal("ValidateVerifier(nil LLM) = nil, want error")
	}
	if err := cfg.ValidateVerifier(true); err != nil {
		t.Errorf("ValidateVerifier(with LLM) = %v, want nil", err)
	}
	cfg.Verifier.Enabled = false
	if err := cfg.ValidateVerifier(false); err != nil {
		t.Errorf("disabled verifier must not require an LLM: %v", err)
	}
}
