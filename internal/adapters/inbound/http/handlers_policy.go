package http

import (
	"net/http"
	"time"

	"github.com/claudioed/inbound-receiving/internal/adapters/inbound/http/page"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/policy"
)

// ---- Receiving policies (ADR 0026) ---------------------------------------------

// receivingPolicyResponse is the stored policy: what every enforcement seam
// resolves for the exact (ownerRef, supplierRef) key.
type receivingPolicyResponse struct {
	OwnerRef       string    `json:"ownerRef,omitempty"`
	SupplierRef    string    `json:"supplierRef"`
	OverTolerance  float64   `json:"overTolerance"`
	UnderTolerance float64   `json:"underTolerance"`
	OverHardStop   float64   `json:"overHardStop"`
	UnderHardStop  float64   `json:"underHardStop"`
	Version        int64     `json:"version"`
	DeclaredAt     time.Time `json:"declaredAt"`
}

func toReceivingPolicy(p *policy.ReceivingPolicy) receivingPolicyResponse {
	return receivingPolicyResponse{
		OwnerRef: p.OwnerRef().String(), SupplierRef: p.SupplierRef(),
		OverTolerance: p.OverTolerance(), UnderTolerance: p.UnderTolerance(),
		OverHardStop: p.OverHardStop(), UnderHardStop: p.UnderHardStop(),
		Version: p.Version(), DeclaredAt: p.DeclaredAt(),
	}
}

// handleDeclareReceivingPolicy backs POST /receiving-policies (ADR 0026):
// 201 on create, 200 on replace, both answering the stored policy.
func (s *Server) handleDeclareReceivingPolicy(w http.ResponseWriter, r *http.Request) {
	var req declareReceivingPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	p, created, err := s.DeclareReceivingPolicy.Handle(r.Context(), usecases.DeclareReceivingPolicyCommand{
		OwnerRef: req.OwnerRef, SupplierRef: req.SupplierRef,
		OverTolerance: req.OverTolerance, UnderTolerance: req.UnderTolerance,
		OverHardStop: req.OverHardStop, UnderHardStop: req.UnderHardStop,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeVersioned(w, status, p.Version(), toReceivingPolicy(p))
}

// handleGetReceivingPolicy backs GET /receiving-policies/{supplierRef}: the
// house default when ownerRef is absent, that owner's policy when present.
func (s *Server) handleGetReceivingPolicy(w http.ResponseWriter, r *http.Request) {
	supplierRef, err := pathParam(r, "supplierRef")
	if err != nil {
		writeError(w, r, err)
		return
	}
	ownerRef, err := ownerQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	p, err := s.GetReceivingPolicy.Handle(r.Context(), ownerRef, supplierRef)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, p.Version(), toReceivingPolicy(p))
}

// ownerQuery reads the optional ownerRef filter of the policy read: the only
// query parameter the route declares, absent meaning the house default.
func ownerQuery(r *http.Request) (string, error) {
	if name := page.Unknown(r, "ownerRef"); name != "" {
		return "", &unknownParamError{name: name}
	}
	return r.URL.Query().Get("ownerRef"), nil
}
