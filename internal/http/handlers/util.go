package handlers

import (
	"encoding/json"
	"net/http"
)

// respondJSON writes the provided payload as a JSON response with the given status code.
// All handlers in this package should use this helper for consistency.
func respondJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeJSON is an alias for respondJSON for files that prefer the writeJSON name.
func writeJSON(w http.ResponseWriter, status int, data any) {
	respondJSON(w, status, data)
}
