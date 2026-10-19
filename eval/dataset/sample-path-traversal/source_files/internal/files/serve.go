package files

import (
	"net/http"
	"os"
	"path/filepath"
)

// Serve writes the requested upload to the response.
func Serve(w http.ResponseWriter, uploadsDir, name string) {
	p := filepath.Join(uploadsDir, name)
	data, err := os.ReadFile(p)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}