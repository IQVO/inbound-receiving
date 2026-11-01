package http

import (
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

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
