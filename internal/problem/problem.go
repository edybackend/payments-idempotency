// Package problem writes RFC 9457 problem details.
package problem

import (
	"encoding/json"
	"net/http"
)

// Details is an RFC 9457 problem document.
type Details struct {
	Type   string            `json:"type"`
	Title  string            `json:"title"`
	Status int               `json:"status"`
	Detail string            `json:"detail,omitempty"`
	Errors map[string]string `json:"errors,omitempty"`
}

// Write sends a problem with the given status, title and detail.
func Write(w http.ResponseWriter, status int, title, detail string) {
	WriteDetails(w, Details{Type: "about:blank", Title: title, Status: status, Detail: detail})
}

// WriteDetails sends d as application/problem+json.
func WriteDetails(w http.ResponseWriter, d Details) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(d.Status)
	_ = json.NewEncoder(w).Encode(d)
}
