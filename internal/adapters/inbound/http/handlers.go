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
	cmd := usecases.RegisterAsnCommand{AsnNumber: req.AsnNumber, SupplierRef: req.SupplierRef}
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

// handleCancelAsn backs POST /asns/{asnNumber}/cancel (200).
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

// ---- Appointments -------------------------------------------------------------

// handleBookAppointment backs POST /appointments (201).
func (s *Server) handleBookAppointment(w http.ResponseWriter, r *http.Request) {
	var req bookAppointmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	d, err := s.BookAppointment.Handle(r.Context(), usecases.BookAppointmentCommand{
		DoorCode: req.DoorCode, Carrier: req.Carrier, WindowStart: req.WindowStart, WindowEnd: req.WindowEnd, AsnNumbers: req.AsnNumbers,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCreated(w, "/appointments/"+string(d.ID()), d.Version(), toAppointment(d))
}

// handleGetAppointment backs GET /appointments/{appointmentId}.
func (s *Server) handleGetAppointment(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "appointmentId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	d, err := s.GetAppointment.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, d.Version(), toAppointment(d))
}

// handleListAppointments backs
// GET /appointments?limit=&cursor=&door=&state=&from=&to=.
func (s *Server) handleListAppointments(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	from, err := instant(r, "from")
	if err != nil {
		writeError(w, r, err)
		return
	}
	to, err := instant(r, "to")
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := s.ListAppointments.Handle(r.Context(), usecases.ListAppointmentsQuery{
		Limit: limit, Cursor: cursor, Door: q.Get("door"), State: q.Get("state"), From: from, To: to,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := appointmentPageResponse{Items: make([]appointmentResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, d := range page.Items {
		out.Items = append(out.Items, toAppointment(d))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCheckInAppointment backs POST /appointments/{appointmentId}/check-in.
func (s *Server) handleCheckInAppointment(w http.ResponseWriter, r *http.Request) {
	s.appointmentAction(w, r, func(r *http.Request, cmd usecases.AppointmentActionCommand) (appointmentResponse, error) {
		d, err := s.CheckInAppointment.Handle(r.Context(), cmd)
		if err != nil {
			return appointmentResponse{}, err
		}
		return toAppointment(d), nil
	})
}

// handleCancelAppointment backs POST /appointments/{appointmentId}/cancel.
func (s *Server) handleCancelAppointment(w http.ResponseWriter, r *http.Request) {
	s.appointmentAction(w, r, func(r *http.Request, cmd usecases.AppointmentActionCommand) (appointmentResponse, error) {
		d, err := s.CancelAppointment.Handle(r.Context(), cmd)
		if err != nil {
			return appointmentResponse{}, err
		}
		return toAppointment(d), nil
	})
}

// appointmentAction is the shared request handling of check-in and cancel:
// path id, optional reason body, optional If-Match.
func (s *Server) appointmentAction(w http.ResponseWriter, r *http.Request, run func(*http.Request, usecases.AppointmentActionCommand) (appointmentResponse, error)) {
	id, err := pathParam(r, "appointmentId")
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
	out, err := run(r, usecases.AppointmentActionCommand{AppointmentID: id, Reason: req.Reason, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, out.Version, out)
}

// ---- Receipts ---------------------------------------------------------------------

// handleOpenReceipt backs POST /receipts (201).
func (s *Server) handleOpenReceipt(w http.ResponseWriter, r *http.Request) {
	var req openReceiptRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.OpenReceipt.Handle(r.Context(), usecases.OpenReceiptCommand{AsnNumber: req.AsnNumber, AppointmentID: req.AppointmentID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCreated(w, "/receipts/"+string(x.ID()), x.Version(), toReceipt(x))
}

// handleGetReceipt backs GET /receipts/{receiptId}.
func (s *Server) handleGetReceipt(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.GetReceipt.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, x.Version(), toReceipt(x))
}

// handleListReceipts backs GET /receipts?limit=&cursor=&asnNumber=&state=.
func (s *Server) handleListReceipts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := s.ListReceipts.Handle(r.Context(), usecases.ListReceiptsQuery{Limit: limit, Cursor: cursor, AsnNumber: q.Get("asnNumber"), State: q.Get("state")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := receiptPageResponse{Items: make([]receiptResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, x := range page.Items {
		out.Items = append(out.Items, toReceipt(x))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReceiveLine backs POST /receipts/{receiptId}/lines (201).
func (s *Server) handleReceiveLine(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req receiveLineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.ReceiveLine.Handle(r.Context(), usecases.ReceiveLineCommand{
		ReceiptID: id, LineNo: req.LineNo, Quantity: req.Quantity, Condition: req.Condition, ExpectedVersion: version,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusCreated, x.Version(), toReceipt(x))
}

// handleCloseReceipt backs POST /receipts/{receiptId}/close (200).
func (s *Server) handleCloseReceipt(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.CloseReceipt.Handle(r.Context(), usecases.CloseReceiptCommand{ReceiptID: id, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, x.Version(), toReceipt(x))
}

// ---- Docks ----------------------------------------------------------------------------

// handleListDocks backs GET /docks.
func (s *Server) handleListDocks(w http.ResponseWriter, r *http.Request) {
	docks, err := s.ListDocks.Handle(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDockList(docks))
}
