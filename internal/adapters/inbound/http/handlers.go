package http

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// pathParam returns the named path segment, unescaped.
func pathParam(r *http.Request, name string) (string, error) {
	raw := chi.URLParam(r, name)
	value, err := url.PathUnescape(raw)
	if err != nil {
		return "", errMalformedRequest
	}
	return value, nil
}

// expectedVersion reads If-Match: the strong ETag `"<version>"` (a bare
// number is accepted too). An absent header or `*` is 0, "no precondition".
func expectedVersion(r *http.Request) (int64, error) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" || raw == "*" {
		return 0, nil
	}
	v, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 64)
	if err != nil || v < 1 {
		return 0, errMalformedRequest
	}
	return v, nil
}

// listQuery reads the shared limit/cursor parameters.
func listQuery(r *http.Request) (limit int, cursor string, err error) {
	values := r.URL.Query()
	cursor = values.Get("cursor")
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit == 0 {
			return 0, "", usecases.ErrInvalidListQuery
		}
	}
	return limit, cursor, nil
}

// instant parses an optional RFC 3339 query parameter.
func instant(r *http.Request, name string) (time.Time, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, invalidQueryParam(name, "must be an RFC 3339 instant")
	}
	return t.UTC(), nil
}

func invalidQueryParam(name, why string) error {
	return &queryError{msg: "invalid list query: " + name + " " + why}
}

type queryError struct{ msg string }

func (e *queryError) Error() string { return e.msg }
func (e *queryError) Unwrap() error { return usecases.ErrInvalidListQuery }

// ---- ASNs ----------------------------------------------------------------

// handleRegisterAsn backs POST /asns (201).
func (s *Server) handleRegisterAsn(w http.ResponseWriter, r *http.Request) {
	var req registerAsnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	cmd := usecases.RegisterAsnCommand{AsnNumber: req.AsnNumber, SupplierRef: req.SupplierRef, OwnerRef: req.OwnerRef, Cartons: req.cartonInputs()}
	if req.ExpectedArrival != nil {
		cmd.ExpectedArrival = *req.ExpectedArrival
	}
	for _, l := range req.Lines {
		cmd.Lines = append(cmd.Lines, usecases.AsnLineInput{LineNo: l.LineNo, SKU: l.SKU, ExpectedQty: l.ExpectedQty})
	}
	a, err := s.RegisterAsn.Handle(r.Context(), cmd)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCreated(w, "/asns/"+url.PathEscape(string(a.Number())), a.Version(), toAsn(a))
}

// handleGetAsn backs GET /asns/{asnNumber}.
func (s *Server) handleGetAsn(w http.ResponseWriter, r *http.Request) {
	number, err := pathParam(r, "asnNumber")
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.GetAsn.Handle(r.Context(), number)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, a.Version(), toAsn(a))
}

// handleGetAsnCartons backs GET /asns/{asnNumber}/cartons.
func (s *Server) handleGetAsnCartons(w http.ResponseWriter, r *http.Request) {
	number, err := pathParam(r, "asnNumber")
	if err != nil {
		writeError(w, r, err)
		return
	}
	v, err := s.GetAsnCartons.Handle(r.Context(), number)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, v.Asn.Version(), toAsnCartons(v))
}

// handleListAsns backs GET /asns?limit=&cursor=&state=.
func (s *Server) handleListAsns(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	page, err := s.ListAsns.Handle(r.Context(), usecases.ListAsnsQuery{Limit: limit, Cursor: cursor, State: r.URL.Query().Get("state")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := asnPageResponse{Items: make([]asnResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, a := range page.Items {
		out.Items = append(out.Items, toAsn(a))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCancelAsn backs POST /asns/{asnNumber}/cancellations (200).
func (s *Server) handleCancelAsn(w http.ResponseWriter, r *http.Request) {
	number, err := pathParam(r, "asnNumber")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req reasonRequest
	if err := decodeOptionalJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	a, err := s.CancelAsn.Handle(r.Context(), usecases.CancelAsnCommand{AsnNumber: number, Reason: req.Reason, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, a.Version(), toAsn(a))
}
