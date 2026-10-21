package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrOscillation is returned when an agent keeps repeating the same tool
// calls and must be interrupted (FR-51).
var ErrOscillation = errors.New("harness: tool call oscillation detected")

const (
	// RepeatThreshold is the number of consecutive identical fingerprints
	// that triggers oscillation detection.
	RepeatThreshold = 3
	// AlternationRounds is the number of A-B round trips that triggers
	// oscillation detection (2 rounds = A-B-A-B).
	AlternationRounds = 2
)

// ArgsHash serializes args to JSON and returns the hex-encoded SHA-256 of the
// serialized payload. JSON serialization keeps the hash stable across runs
// (CLAUDE.v.1.8.md).
func ArgsHash(args string) (string, error) {
	payload, err := json.Marshal(args)
	if err != nil {
		return "", fmt.Errorf("harness: marshal tool args: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}

// toolFingerprint combines the tool name and the argument hash into the
// identity used by the oscillation detector.
func toolFingerprint(log ToolLog) string {
	return log.ToolName + ":" + log.ArgsHash
}

// DetectOscillation inspects the recorded tool calls and returns
// ErrOscillation when the agent is stuck in a loop. It is a pure in-memory
// function: no database or network access is allowed here
// (CLAUDE.v.1.8.md).
//
// Two patterns are detected anywhere in the log:
//   - three consecutive identical fingerprints (A-A-A);
//   - an alternating pattern of at least two full rounds (A-B-A-B).
func DetectOscillation(logs []ToolLog) error {
	for i := 0; i < len(logs); i++ {
		if i+RepeatThreshold <= len(logs) && repeated(logs[i:i+RepeatThreshold]) {
			return fmt.Errorf("%w: tool %q repeated %d times consecutively",
				ErrOscillation, logs[i].ToolName, RepeatThreshold)
		}
		rounds := AlternationRounds * 2
		if i+rounds <= len(logs) && alternates(logs[i:i+rounds], AlternationRounds) {
			return fmt.Errorf("%w: tools %q and %q alternate for %d rounds",
				ErrOscillation, logs[i].ToolName, logs[i+1].ToolName, AlternationRounds)
		}
	}
	return nil
}

// repeated reports whether every fingerprint in window is identical.
func repeated(window []ToolLog) bool {
	first := toolFingerprint(window[0])
	for _, log := range window[1:] {
		if toolFingerprint(log) != first {
			return false
		}
	}
	return true
}

// alternates reports whether window is A-B-A-B... with exactly two distinct
// fingerprints alternating for the given number of rounds.
func alternates(window []ToolLog, rounds int) bool {
	a := toolFingerprint(window[0])
	b := toolFingerprint(window[1])
	if a == b {
		return false
	}
	for i := 0; i < rounds*2; i++ {
		want := a
		if i%2 == 1 {
			want = b
		}
		if toolFingerprint(window[i]) != want {
			return false
		}
	}
	return true
}
