package review

import "testing"

// TestFindingFingerprintIgnoresLineNumber covers AC-125: moving a finding
// down the file (line number changes) must not change its identity.
func TestFindingFingerprintIgnoresLineNumber(t *testing.T) {
	msg := "SQL injection in query construction"
	a := FindingFingerprint("internal/store/order.go", "security", msg)
	b := FindingFingerprint("internal/store/order.go", "security", msg)
	if a != b {
		t.Fatalf("fingerprint not stable: %q != %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("fingerprint length = %d, want 64", len(a))
	}
	if a == FindingFingerprint("internal/store/user.go", "security", msg) {
		t.Error("different paths must produce different fingerprints")
	}
	if a == FindingFingerprint("internal/store/order.go", "logic", msg) {
		t.Error("different agents must produce different fingerprints")
	}
}

func TestFindingFingerprintNormalizesInput(t *testing.T) {
	base := FindingFingerprint("internal/api/handler.go", "logic", "unhandled error")
	if got := FindingFingerprint("  internal/api/handler.go  ", " logic ", "  unhandled   error  "); got != base {
		t.Errorf("whitespace normalization mismatch: %q != %q", got, base)
	}
	if got := FindingFingerprint("internal/api/handler.go", "logic", "unhandled error 第 42 行"); got != base {
		t.Errorf("line reference must be removed: %q != %q", got, base)
	}
	if got := FindingFingerprint("./internal/api/handler.go", "logic", "unhandled error"); got != base {
		t.Errorf("./ prefix must be normalized: %q != %q", got, base)
	}
}
