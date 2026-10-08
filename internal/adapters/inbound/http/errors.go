package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.inbound-receiving.warehouse-systems.dev/"

var (
	// errMalformedRequest marks a body or parameter the adapter itself
	// rejects (bad JSON, unknown field, unparsable If-Match).
	errMalformedRequest = errors.New("request body is not valid JSON for this operation")
	// errIdempotencyKeyRequired is a resource-creating POST without an
	// Idempotency-Key header.
	errIdempotencyKeyRequired = errors.New("resource-creating requests must send a non-empty Idempotency-Key")
	// errIdempotencyKeyReused is a key seen before with a different request.
	errIdempotencyKeyReused = errors.New("the idempotency key was used with a different request body")
)

// problem is the fixed (status, slug, title) of one error category; the
// detail comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
}

var internalProblem = problem{http.StatusInternalServerError, "internal-error", "Internal server error"}

func bad(slug, title string) problem      { return problem{http.StatusBadRequest, slug, title} }
func notFound(slug, title string) problem { return problem{http.StatusNotFound, slug, title} }
func conflict(slug, title string) problem { return problem{http.StatusConflict, slug, title} }
func unprocessable(slug, title string) problem {
	return problem{http.StatusUnprocessableEntity, slug, title}
}

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of apis/openapi.yaml.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, bad("malformed-request", "Malformed request")},
	{usecases.ErrInvalidListQuery, bad("invalid-query", "Invalid query parameter")},
	{errIdempotencyKeyRequired, bad("idempotency-key-required", "Idempotency-Key header is required")},
	{errIdempotencyKeyReused, unprocessable("idempotency-key-reused", "Idempotency-Key was already used with a different request")},

	{asn.ErrInvalidNumber, bad("invalid-asn-number", "ASN number is invalid")},
	{asn.ErrInvalidSupplierRef, bad("invalid-supplier-ref", "Supplier reference is invalid")},
	{asn.ErrNoLines, bad("asn-requires-lines", "An ASN requires at least one line")},
	{asn.ErrInvalidLineNo, bad("invalid-line-no", "Line numbers must run 1..n in order")},
	{asn.ErrDuplicateSKU, bad("duplicate-sku", "An ASN may list a SKU only once")},
	{shared.ErrInvalidSKU, bad("invalid-sku", "SKU is invalid")},
	{shared.ErrInvalidQuantity, bad("invalid-quantity", "Quantity is invalid")},
	{shared.ErrInvalidReason, bad("invalid-reason", "Reason is invalid")},
	{appointment.ErrInvalidID, bad("invalid-appointment-id", "Appointment id is invalid")},
	{receipt.ErrInvalidID, bad("invalid-receipt-id", "Receipt id is invalid")},
	{appointment.ErrInvalidDoorCode, bad("invalid-door-code", "Door code is invalid")},
	{appointment.ErrInvalidCarrier, bad("invalid-carrier", "Carrier is invalid")},
	{appointment.ErrInvalidWindow, bad("invalid-window", "Window is invalid")},
	{appointment.ErrWindowInPast, bad("window-in-past", "Window starts in the past")},
	{appointment.ErrNoAsns, bad("appointment-requires-asns", "An appointment requires at least one ASN")},
	{appointment.ErrDuplicateAsn, bad("duplicate-asn-number", "An appointment may list an ASN only once")},
	{receipt.ErrInvalidCondition, bad("invalid-condition", "Condition must be Good or Damaged")},

	{repository.ErrAsnNotFound, notFound("asn-not-found", "ASN not found")},
	{repository.ErrAppointmentNotFound, notFound("appointment-not-found", "Appointment not found")},
	{repository.ErrReceiptNotFound, notFound("receipt-not-found", "Receipt not found")},

	{usecases.ErrAsnAlreadyExists, conflict("asn-already-exists", "ASN already exists")},
	{asn.ErrAsnInProgress, conflict("asn-in-progress", "ASN is being received")},
	{asn.ErrAsnTerminal, conflict("asn-terminal", "ASN is closed or cancelled")},
	{receipt.ErrAsnNotReceivable, conflict("asn-not-receivable", "ASN is not receivable")},
	{asn.ErrAsnNotReceiving, conflict("asn-not-receivable", "ASN is not being received")},
	{appointment.ErrNotBooked, conflict("appointment-not-booked", "Appointment is not booked")},
	{appointment.ErrNotCheckedIn, conflict("appointment-not-checked-in", "Appointment is not checked in")},
	{usecases.ErrAppointmentNotCheckedIn, conflict("appointment-not-checked-in", "Appointment is not checked in")},
	{appointment.ErrOutsideCheckInWindow, conflict("outside-check-in-window", "Check-in is outside the allowed window")},
	{appointment.ErrWindowOverlap, conflict("door-window-overlap", "Door is already booked in an overlapping window")},
	{repository.ErrReceiptAlreadyOpen, conflict("receipt-already-open", "ASN already has an open receipt")},
	{receipt.ErrReceiptClosed, conflict("receipt-closed", "Receipt is closed")},
	{repository.ErrConcurrentModification, conflict("concurrent-modification", "The resource was modified by another request; re-fetch the latest version and retry")},

	{usecases.ErrUnknownSKU, unprocessable("unknown-sku", "SKU is not a registered product")},
	{usecases.ErrUnknownDockDoor, unprocessable("unknown-dock-door", "Door is not a known inbound dock door")},
	{usecases.ErrUnknownAsn, unprocessable("unknown-asn", "ASN does not exist")},
	{usecases.ErrUnknownAppointment, unprocessable("unknown-appointment", "Appointment does not exist")},
	{usecases.ErrAsnNotOnAppointment, unprocessable("asn-not-on-appointment", "ASN is not covered by the appointment")},
	{receipt.ErrLineNotOnAsn, unprocessable("line-not-on-asn", "Line is not on the ASN")},

	{usecases.ErrVersionMismatch, problem{http.StatusPreconditionFailed, "version-mismatch", "The If-Match version is stale"}},
}

// problemFor returns the problem of the first catalogue entry err matches,
// or the internal-error problem.
func problemFor(err error) problem {
	for _, entry := range problemCatalogue {
		if errors.Is(err, entry.err) {
			return entry.p
		}
	}
	return internalProblem
}
