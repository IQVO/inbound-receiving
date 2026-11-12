package usecases

import (
	"context"

	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// raiseAsnLate evaluates the ASN-timeliness fact of ADR 0025 and, when the
// ASN was registered after the visit it is received under had already
// arrived, marks the exactly-once marker and enqueues AsnLateForArrival in
// the SAME unit of work as the open. OpenReceipt is the only caller and the
// only trigger: it is the only place an ASN and an appointment meet.
//
// Nothing is raised when the receipt is a walk-in, when no arrival was seen
// for the appointment yet, when the ASN's registration time is unknown (a
// legacy row), when the ASN was on time, or when the marker says the event
// already left. A nil StowStore (a test) skips the evaluation.
func (uc *OpenReceipt) raiseAsnLate(ctx context.Context, a *asn.Asn, apptID appointment.ID) error {
	if uc.Stows == nil || apptID == "" {
		return nil
	}
	arrival, known, err := uc.Stows.Arrival(ctx, apptID)
	if err != nil || !known {
		return err
	}
	late := a.LateForArrival(string(apptID), arrival.VisitID, arrival.ArrivedAt, uc.now())
	if late == nil {
		return nil
	}
	first, err := uc.Asns.MarkLateArrival(ctx, a.Number(), string(apptID), arrival.VisitID, late.OccurredAt())
	if err != nil || !first {
		return err
	}
	msgs, err := uc.Encoder.EncodeAsn(*late)
	return uc.enqueue(ctx, msgs, err)
}
