package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHMACVerify(t *testing.T) {
	const secret = "test-secret"
	body := []byte(`{"action":"opened"}`)

	middleware := HMACVerify(secret)
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		// Downstream must be able to read the body again.
		buf := make([]byte, len(body))
		if _, err := r.Body.Read(buf); err != nil && err.Error() != "EOF" {
			t.Errorf("read restored body: %v", err)
		}
		if string(buf) != string(body) {
			t.Errorf("body mismatch: got %q, want %q", string(buf), string(body))
		}
		w.WriteHeader(http.StatusOK)
	})

	tests := []struct {
		name       string
		signature  string
		body       []byte
		wantStatus int
		wantNext   bool
	}{
		{
			name:       "valid signature",
			signature:  ComputeSignature(secret, body),
			body:       body,
			wantStatus: http.StatusOK,
			wantNext:   true,
		},
		{
			name:       "invalid signature",
			signature:  "sha256=" + strings.Repeat("0", 64),
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "missing signature header",
			signature:  "",
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "malformed signature prefix",
			signature:  "md5=abcdef",
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "non-hex signature",
			signature:  "sha256=zzzz",
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "wrong length signature",
			signature:  "sha256=abcd",
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "signature for different body",
			signature:  ComputeSignature(secret, []byte(`{"other":"payload"}`)),
			body:       body,
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nextCalled = false
			req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(tt.body)))
			req.Header.Set(signatureHeader, tt.signature)
			rec := httptest.NewRecorder()

			middleware(next).ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if nextCalled != tt.wantNext {
				t.Errorf("next called = %v, want %v", nextCalled, tt.wantNext)
			}
		})
	}
}

func TestHMACVerifyEmptySecret(t *testing.T) {
	middleware := HMACVerify("")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(`{}`))
	req.Header.Set(signatureHeader, ComputeSignature("", []byte(`{}`)))

	middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})).ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want %d with empty secret", rec.Code, http.StatusUnauthorized)
	}
}
