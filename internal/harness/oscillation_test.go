package harness

import (
	"errors"
	"testing"
)

// logFor builds a ToolLog with a stable argument hash for detector tests.
func logFor(t *testing.T, tool, args string) ToolLog {
	t.Helper()
	hash, err := ArgsHash(args)
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	return ToolLog{ToolName: tool, ArgsHash: hash}
}

func TestArgsHashDeterministic(t *testing.T) {
	a, err := ArgsHash("main.go:1:10")
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	b, err := ArgsHash("main.go:1:10")
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	c, err := ArgsHash("main.go:1:11")
	if err != nil {
		t.Fatalf("ArgsHash: %v", err)
	}
	if a != b {
		t.Errorf("ArgsHash not stable: %q != %q", a, b)
	}
	if a == c {
		t.Error("different arguments must produce different hashes")
	}
	if len(a) != 64 {
		t.Errorf("ArgsHash length = %d, want 64 (sha256 hex)", len(a))
	}
}

func TestDetectOscillation(t *testing.T) {
	tests := []struct {
		name    string
		logs    func(t *testing.T) []ToolLog
		wantErr bool
	}{
		{
			name: "no calls",
			logs: func(*testing.T) []ToolLog { return nil },
		},
		{
			name: "distinct calls",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "a.go"),
					logFor(t, "read_file", "b.go"),
					logFor(t, "search_code", "x"),
				}
			},
		},
		{
			name: "two identical calls below threshold",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "a.go"),
					logFor(t, "read_file", "a.go"),
				}
			},
		},
		{
			name: "three consecutive identical calls",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "main.go"),
					logFor(t, "read_file", "main.go"),
					logFor(t, "read_file", "main.go"),
				}
			},
			wantErr: true,
		},
		{
			name: "same tool different arguments is not a repeat",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "a.go"),
					logFor(t, "read_file", "b.go"),
					logFor(t, "read_file", "c.go"),
				}
			},
		},
		{
			name: "single alternation round is allowed",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "search_code", "x"),
					logFor(t, "read_file", "y"),
					logFor(t, "search_code", "x"),
				}
			},
		},
		{
			name: "alternating A-B-A-B",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "search_code", "x"),
					logFor(t, "read_file", "y"),
					logFor(t, "search_code", "x"),
					logFor(t, "read_file", "y"),
				}
			},
			wantErr: true,
		},
		{
			name: "alternation after unrelated prefix",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "setup.go"),
					logFor(t, "search_code", "x"),
					logFor(t, "read_file", "y"),
					logFor(t, "search_code", "x"),
					logFor(t, "read_file", "y"),
				}
			},
			wantErr: true,
		},
		{
			name: "A-B-C-B is not alternation",
			logs: func(t *testing.T) []ToolLog {
				return []ToolLog{
					logFor(t, "read_file", "a"),
					logFor(t, "read_file", "b"),
					logFor(t, "read_file", "c"),
					logFor(t, "read_file", "b"),
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DetectOscillation(tt.logs(t))
			if tt.wantErr {
				if !errors.Is(err, ErrOscillation) {
					t.Fatalf("DetectOscillation error = %v, want ErrOscillation", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectOscillation error = %v, want nil", err)
			}
		})
	}
}
