package http

import (
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// ---- Appointments -------------------------------------------------------------
//
// Appointments are owned by dock-scheduling (ADR 0019, plan YD-0002). Here only
// the two reads over the local copy remain, both deprecated in
// apis/openapi.yaml (removed at stage E), and the three former write routes
// answer 410.

// appointmentMoved names where one former write operation lives now.
type appointmentMoved string

// The detail of each 410, naming the new owner's endpoint.
const (
	moveBook    appointmentMoved = "Appointments are booked in dock-scheduling: POST /appointments on that service."
	moveCheckIn appointmentMoved = "A carrier's arrival is recorded at the gate by yard-management (POST /visits naming the appointmentId); " +
		"dock-scheduling derives the appointment's check-in from it. There is no check-in on appointments any more."
	moveCancel appointmentMoved = "Appointments are cancelled in dock-scheduling: POST /appointments/{appointmentId}/cancellations on that service."
)

// appointmentMovedProblem is the 410 problem of every former appointment write.
var appointmentMovedProblem = problem{http.StatusGone, "appointment-moved", "Appointment writes moved to dock-scheduling"}

// appointmentWriteMoved answers 410 appointment-moved with the detail of the
// operation. The body is not read and no key is required.
func appointmentWriteMoved(m appointmentMoved) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, appointmentMovedProblem, string(m))
	}
}

// handleGetAppointment backs GET /appointments/{appointmentId} (deprecated).
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
// GET /appointments?limit=&cursor=&door=&state=&from=&to= (deprecated).
func (s *Server) handleListAppointments(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listQuery(r, "door", "state", "from", "to")
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
