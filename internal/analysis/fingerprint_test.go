package analysis

import (
	"reflect"
	"testing"
)

func TestFingerprint(t *testing.T) {
	tests := []struct {
		name   string
		tool   string
		ruleID string
		file   string
		line   int
		want   string
	}{
		{
			name:   "typical finding",
			tool:   "semgrep",
			ruleID: "sql-injection",
			file:   "internal/db/users.go",
			line:   42,
			want:   "semgrep:sql-injection:internal/db/users.go:42",
		},
		{
			name:   "same input same fingerprint (pure function)",
			tool:   "gitleaks",
			ruleID: "generic-api-key",
			file:   "main.go",
			line:   7,
			want:   Fingerprint("gitleaks", "generic-api-key", "main.go", 7),
		},
		{
			name:   "different line different fingerprint",
			tool:   "gitleaks",
			ruleID: "generic-api-key",
			file:   "main.go",
			line:   8,
			want:   "gitleaks:generic-api-key:main.go:8",
		},
		{
			name:   "different tool different fingerprint",
			tool:   "golangci-lint",
			ruleID: "generic-api-key",
			file:   "main.go",
			line:   7,
			want:   "golangci-lint:generic-api-key:main.go:7",
		},
		{
			name:   "empty fields",
			tool:   "",
			ruleID: "",
			file:   "",
			line:   0,
			want:   ":::0",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Fingerprint(tt.tool, tt.ruleID, tt.file, tt.line)
			if got != tt.want {
				t.Errorf("Fingerprint(%q, %q, %q, %d) = %q, want %q",
					tt.tool, tt.ruleID, tt.file, tt.line, got, tt.want)
			}
		})
	}
}

func TestAssignID(t *testing.T) {
	f := PreAnalysisFinding{Tool: "gitleaks", RuleID: "aws-key", FilePath: "a/b.go", Line: 12}
	AssignID(&f)
	if f.ID != "gitleaks:aws-key:a/b.go:12" {
		t.Errorf("ID = %q, want gitleaks:aws-key:a/b.go:12", f.ID)
	}
}

func TestMarshalUnmarshalRoundTrip(t *testing.T) {
	findings := []PreAnalysisFinding{
		{
			ID: "semgrep:sql-injection:db.go:11", Tool: "semgrep",
			RuleID: "sql-injection", FilePath: "db.go",
			Line: 11, EndLine: 11, Severity: "critical",
			Message: "SQL injection", Snippet: `db.Query("SELECT " + x)`,
		},
		{
			ID: "golangci-lint:errcheck:main.go:5", Tool: "golangci-lint",
			RuleID: "errcheck", FilePath: "main.go",
			Line: 5, EndLine: 5, Severity: "warning",
			Message: "unhandled error",
		},
	}

	data, err := MarshalPreFindings(findings)
	if err != nil {
		t.Fatalf("MarshalPreFindings: %v", err)
	}
	got, err := UnmarshalPreFindings(data)
	if err != nil {
		t.Fatalf("UnmarshalPreFindings: %v", err)
	}
	if !reflect.DeepEqual(got, findings) {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, findings)
	}
}

func TestMarshalEmptySlice(t *testing.T) {
	data, err := MarshalPreFindings(nil)
	if err != nil {
		t.Fatalf("MarshalPreFindings(nil): %v", err)
	}
	if data != "[]" {
		t.Errorf("MarshalPreFindings(nil) = %q, want [] (not null)", data)
	}
}

func TestUnmarshalEmptyArray(t *testing.T) {
	got, err := UnmarshalPreFindings("[]")
	if err != nil {
		t.Fatalf("UnmarshalPreFindings: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

func TestUnmarshalInvalidJSON(t *testing.T) {
	if _, err := UnmarshalPreFindings("not-json{"); err == nil {
		t.Error("invalid JSON should produce an error")
	}
}
