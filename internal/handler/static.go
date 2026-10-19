package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// MountStatic serves the no-build frontend (v1.7 FR-45) from dir. It is a
// catch-all: routes registered on r before this call (dashboard, API,
// webhook) take precedence.
func MountStatic(r chi.Router, dir string) {
	r.Handle("/*", http.FileServer(http.Dir(dir)))
}
