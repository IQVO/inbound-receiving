package http

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
)

// maxBodyBytes bounds a request body; ASN bodies with many lines are the
// largest and stay well below this.
const maxBodyBytes = 1 << 20

type statusBody struct {
	Status string `json:"status"`
}

// problemDetail is the RFC 7807 application/problem+json body.
type problemDetail struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Instance string `json:"instance"`
}

// writeJSON encodes v as the response body with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeVersioned is writeJSON plus the strong ETag of the resource.
func writeVersioned(w http.ResponseWriter, status int, version int64, v any) {
	w.Header().Set("ETag", etag(version))
	writeJSON(w, status, v)
}

// writeCreated is a 201 with ETag and Location.
func writeCreated(w http.ResponseWriter, location string, version int64, v any) {
	w.Header().Set("Location", location)
	writeVersioned(w, http.StatusCreated, version, v)
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// decodeJSON strictly decodes r's body into dest: unknown fields, trailing
// data and malformed JSON are errMalformedRequest.
func decodeJSON(r *http.Request, dest any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after the JSON body", errMalformedRequest)
	}
	return nil
}

// decodeOptionalJSON is decodeJSON for a body that may be absent entirely
// (the action POSTs): an empty body leaves dest untouched.
func decodeOptionalJSON(r *http.Request, dest any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return fmt.Errorf("%w: %v", errMalformedRequest, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: trailing data after the JSON body", errMalformedRequest)
	}
	return nil
}

// writeError writes err as its RFC 7807 problem. An unmapped error is a 500
// whose detail never leaks the internal message (it is logged instead).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	p := problemFor(err)
	detail := err.Error()
	if p == internalProblem {
		slog.ErrorContext(r.Context(), "request failed", "error", err, "method", r.Method, "path", r.URL.Path)
		detail = "an unexpected error occurred"
	}
	writeProblem(w, r, p, detail)
}

func writeProblem(w http.ResponseWriter, r *http.Request, p problem, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.status)
	_ = json.NewEncoder(w).Encode(problemDetail{
		Type:     problemBaseURI + p.slug,
		Title:    p.title,
		Status:   p.status,
		Detail:   detail,
		Instance: r.URL.EscapedPath(),
	})
}
