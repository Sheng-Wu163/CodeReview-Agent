package api

import "net/http"

// Get_User_Data writes the user payload.
func Get_User_Data(w http.ResponseWriter, _ *http.Request) {
	_, _ = w.Write([]byte("ok"))
}