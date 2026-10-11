// Package middleware provides HTTP middlewares for the service.
package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const signatureHeader = "X-Hub-Signature-256"

// HMACVerify returns a middleware that verifies the GitHub webhook
// HMAC-SHA256 signature carried in the X-Hub-Signature-256 header against
// the provided secret. Requests with a missing or invalid signature are
// rejected with 401 Unauthorized.
func HMACVerify(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "failed to read request body", http.StatusBadRequest)
				return
			}
			// Restore the body for downstream handlers.
			r.Body = io.NopCloser(strings.NewReader(string(body)))

			if !verifySignature(secret, r.Header.Get(signatureHeader), body) {
				http.Error(w, "signature verification failed", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// verifySignature checks a "sha256=<hex>" prefixed signature against an
// HMAC-SHA256 of the body using the given secret. The comparison is
// constant-time.
func verifySignature(secret, signature string, body []byte) bool {
	if secret == "" || !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	// hexDecode produced got; want is raw digest bytes.
	if len(got) != sha256.Size {
		return false
	}
	return hmac.Equal(got, want)
}

// ComputeSignature is a helper that produces the expected X-Hub-Signature-256
// header value for a payload signed with the given secret. It is primarily
// used by tests and local tooling.
func ComputeSignature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return fmt.Sprintf("sha256=%s", hex.EncodeToString(mac.Sum(nil)))
}
