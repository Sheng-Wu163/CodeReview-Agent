package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// TestMountStaticServesIndex verifies the root route serves the no-build
// frontend (T46 completion: visiting / renders the page).
func TestMountStaticServesIndex(t *testing.T) {
	r := chi.NewRouter()
	MountStatic(r, "../../static")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="app"`) {
		t.Errorf("index.html missing the Vue mount point")
	}
	if !strings.Contains(body, "vue.global") {
		t.Errorf("index.html missing the Vue 3 CDN import")
	}
	// App script must be linked.
	if !strings.Contains(body, "app.js") {
		t.Errorf("index.html missing app.js")
	}
}
