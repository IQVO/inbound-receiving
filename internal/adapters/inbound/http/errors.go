package http

import (
	"errors"
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/policy"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// problemBaseURI is the namespace of this service's RFC 7807 `type` URIs.
const problemBaseURI = "https://errors.inbound-receiving.warehouse-systems.dev/"

var (
	// errMalformedRequest marks a body or parameter the adapter itself
	// rejects (bad JSON, unknown field, unparsable If-Match).
	errMalformedRequest = errors.New("request body is not valid JSON for this operation")
	// errIdempotencyKeyRequired is a POST without an Idempotency-Key header.
	errIdempotencyKeyRequired = errors.New("every POST must send a non-empty Idempotency-Key")
	// errIdempotencyKeyInvalid is a key longer than 128 characters or with a
	// character outside printable ASCII.
	errIdempotencyKeyInvalid = errors.New("idempotency key must be 1..128 printable ASCII characters")
	// errIdempotencyKeyReused is a key seen before with a different request.
	errIdempotencyKeyReused = errors.New("the idempotency key was used with a different request")
)

// problem is the fixed (status, slug, title) of one error category; the
// detail comes from the error at write time.
type problem struct {
	status int
	slug   string
	title  string
	class  replayClass
}

// malformedRequestProblem is shared by the catalogue and the NUL guard, so the
// slug has one declaration (and one replay class).
var malformedRequestProblem = bad("malformed-request", "Malformed request", replayRetryable)

var internalProblem = problem{status: http.StatusInternalServerError, slug: "internal-error", title: "Internal server error", class: replayRetryable}

func bad(slug, title string, class replayClass) problem {
	return problem{status: http.StatusBadRequest, slug: slug, title: title, class: class}
}
func notFound(slug, title string, class replayClass) problem {
	return problem{status: http.StatusNotFound, slug: slug, title: title, class: class}
}
func conflict(slug, title string, class replayClass) problem {
	return problem{status: http.StatusConflict, slug: slug, title: title, class: class}
}
func unprocessable(slug, title string, class replayClass) problem {
	return problem{status: http.StatusUnprocessableEntity, slug: slug, title: title, class: class}
}

// problemCatalogue maps every typed error to its problem, in match order.
// Slugs are the catalogue of apis/openapi.yaml.
var problemCatalogue = []struct {
	err error
	p   problem
}{
	{errMalformedRequest, malformedRequestProblem},
	{usecases.ErrInvalidLimit, bad("invalid-limit", "Invalid limit", replayFinal)},
	{usecases.ErrInvalidCursor, bad("invalid-cursor", "Invalid cursor", replayFinal)},
	{errUnknownQuery, bad("unknown-query-parameter", "Unknown query parameter", replayFinal)},
	{usecases.ErrInvalidListQuery, bad("invalid-query", "Invalid query parameter", replayFinal)},
	{errIdempotencyKeyRequired, bad("idempotency-key-required", "Idempotency-Key header is required", replayFinal)},
	{errIdempotencyKeyInvalid, bad("idempotency-key-invalid", "Idempotency-Key header is invalid", replayFinal)},
	{errIdempotencyKeyReused, conflict("idempotency-key-reused", "Idempotency-Key was already used with a different request", replayRetryable)},

	{asn.ErrInvalidNumber, bad("invalid-asn-number", "ASN number is invalid", replayFinal)},
	{asn.ErrInvalidSupplierRef, bad("invalid-supplier-ref", "Supplier reference is invalid", replayFinal)},
	{asn.ErrNoLines, bad("asn-requires-lines", "An ASN requires at least one line", replayFinal)},
	{asn.ErrInvalidLineNo, bad("invalid-line-no", "Line numbers must run 1..n in order", replayFinal)},
	{asn.ErrDuplicateSKU, bad("duplicate-sku", "An ASN may list a SKU only once", replayFinal)},
	{shared.ErrInvalidSKU, bad("invalid-sku", "SKU is invalid", replayFinal)},
	{shared.ErrInvalidQuantity, bad("invalid-quantity", "Quantity is invalid", replayFinal)},
	{shared.ErrInvalidReason, bad("invalid-reason", "Reason is invalid", replayFinal)},
	{appointment.ErrInvalidID, bad("invalid-appointment-id", "Appointment id is invalid", replayFinal)},
	{receipt.ErrInvalidID, bad("invalid-receipt-id", "Receipt id is invalid", replayFinal)},
	{appointment.ErrInvalidDoorCode, bad("invalid-door-code", "Door code is invalid", replayFinal)},
	// ADR 0026: the policy admin surface's value validations.
	{receipt.ErrInvalidOverrideReason, bad("invalid-override-reason", "Override reason must be one of the declared receiving reason codes", replayFinal)},
	{policy.ErrInvalidBand, bad("invalid-tolerance-band", "Tolerance band must satisfy 0 <= tolerance < hardStop <= 1 per side", replayFinal)},
	{appointment.ErrInvalidCarrier, bad("invalid-carrier", "Carrier is invalid", replayFinal)},
	{appointment.ErrInvalidWindow, bad("invalid-window", "Window is invalid", replayFinal)},
	{appointment.ErrNoAsns, bad("appointment-requires-asns", "An appointment requires at least one ASN", replayFinal)},
	{appointment.ErrDuplicateAsn, bad("duplicate-asn-number", "An appointment may list an ASN only once", replayFinal)},
	{receipt.ErrInvalidCondition, bad("invalid-condition", "Condition must be Good or Damaged", replayFinal)},
	{shared.ErrInvalidOwnerRef, bad("invalid-owner-ref", "Owner reference is invalid", replayFinal)},
	{asn.ErrInvalidCartonKind, bad("invalid-carton-kind", "Carton kind must be HOMOGENEOUS_CASE or MIXED", replayFinal)},
	{asn.ErrInvalidCarton, bad("invalid-carton", "Carton is invalid", replayFinal)},
	// asn.ErrInvalidSscc wraps gs1.ErrInvalidSSCC: it must match first so a
	// register-time SSCC is a 422 and a scanned one (below) a 400.
	{asn.ErrInvalidSscc, unprocessable("invalid-sscc", "SSCC is invalid", replayFinal)},
	{usecases.ErrInvalidScanKind, bad("invalid-scan-kind", "Scan kind must be SSCC, CASE_GTIN or EACH_GTIN", replayFinal)},
	{usecases.ErrInvalidScan, bad("invalid-scan", "Scan is not a valid GS1 scan", replayFinal)},
	{receipt.ErrInvalidCases, bad("invalid-cases", "Cases must be between 1 and 1000 per scan", replayFinal)},
	{gs1.ErrVariableMeasure, bad("unsupported-gtin", "Variable measure GTINs are not supported", replayFinal)},
	{gs1.ErrInvalidGTIN, bad("invalid-gtin", "GTIN is invalid", replayFinal)},
	{gs1.ErrInvalidSSCC, bad("invalid-sscc", "SSCC is invalid", replayFinal)},

	{repository.ErrAsnNotFound, notFound("asn-not-found", "ASN not found", replayRetryable)},
	{repository.ErrAppointmentNotFound, notFound("appointment-not-found", "Appointment not found", replayRetryable)},
	{repository.ErrReceiptNotFound, notFound("receipt-not-found", "Receipt not found", replayFinal)},
	{repository.ErrPolicyNotFound, notFound("receiving-policy-not-found", "No receiving policy is declared for this owner and supplier", replayRetryable)},
	{usecases.ErrCartonNotFound, notFound("carton-not-found", "Carton not found on the receipt", replayRetryable)},

	{usecases.ErrAsnAlreadyExists, conflict("asn-already-exists", "ASN already exists", replayFinal)},
	{asn.ErrAsnInProgress, conflict("asn-in-progress", "ASN is being received", replayRetryable)},
	{asn.ErrAsnTerminal, conflict("asn-terminal", "ASN is closed or cancelled", replayFinal)},
	{usecases.ErrAppointmentCancelled, conflict("appointment-cancelled", "Appointment is cancelled", replayFinal)},
	{receipt.ErrAsnNotReceivable, conflict("asn-not-receivable", "ASN is not receivable", replayRetryable)},
	{asn.ErrAsnNotReceiving, conflict("asn-not-receivable", "ASN is not being received", replayRetryable)},
	{repository.ErrReceiptAlreadyOpen, conflict("receipt-already-open", "ASN already has an open receipt", replayRetryable)},
	{receipt.ErrReceiptClosed, conflict("receipt-closed", "Receipt is closed", replayFinal)},
	// ADR 0026: the two tolerance refusals. The detail carries the line, its
	// quantities, the variance and the band it was judged against.
	{receipt.ErrToleranceOverrideRequired, conflict("tolerance-override-required", "The variance is beyond the tolerance band: an override reason code is required", replayRetryable)},
	{receipt.ErrToleranceBlocked, conflict("tolerance-blocked", "The variance is at or beyond the tolerance hard stop: no override can pass and the receipt stays open", replayRetryable)},
	// ADR 0024: no company prefix is configured (a licensed asset, no default), or its serials are used up.
	{receipt.ErrCompanyPrefixNotConfigured, conflict("sscc-prefix-not-configured", "No GS1 company prefix is configured for receiver-assigned SSCCs", replayRetryable)},
	{repository.ErrSsccSerialsExhausted, conflict("sscc-serial-exhausted", "Every SSCC serial of the company prefix has been used", replayRetryable)},
	{repository.ErrConcurrentModification, conflict("concurrent-modification", "The resource was modified by another request; re-fetch the latest version and retry", replayRetryable)},

	{usecases.ErrUnknownSKU, unprocessable("unknown-sku", "SKU is not a registered product", replayRetryable)},
	{usecases.ErrUnknownAsn, unprocessable("unknown-asn", "ASN does not exist", replayRetryable)},
	{usecases.ErrUnknownAppointment, unprocessable("unknown-appointment", "Appointment does not exist", replayRetryable)},
	{usecases.ErrAsnNotOnAppointment, unprocessable("asn-not-on-appointment", "ASN is not covered by the appointment", replayRetryable)},
	{receipt.ErrLineNotOnAsn, unprocessable("line-not-on-asn", "Line is not on the ASN", replayRetryable)},
	{asn.ErrDuplicateSscc, unprocessable("duplicate-sscc", "An SSCC may appear once per site", replayRetryable)},
	{asn.ErrHomogeneousCartonMixed, unprocessable("homogeneous-carton-mixed", "A homogeneous case carton may hold one SKU only", replayFinal)},
	{asn.ErrCartonContentsMismatch, unprocessable("carton-contents-mismatch", "Carton contents must add up to every line", replayFinal)},
	{receipt.ErrCountedContentsNotMixed, unprocessable("counted-contents-not-mixed", "Counted contents apply to a mixed carton only", replayRetryable)},
	{receipt.ErrInvalidCount, bad("invalid-counted-contents", "Counted contents need a unique SKU and a quantity of zero or more", replayFinal)},
	{usecases.ErrCountedSkuUnknown, unprocessable("counted-sku-unknown", "A counted SKU has no each GTIN in the packaging view", replayRetryable)},
	{usecases.ErrScanKindMismatch, unprocessable("scan-kind-mismatch", "The scan is not of the kind the screen expects", replayRetryable)},
	{usecases.ErrUnsupportedPackLevel, unprocessable("unsupported-pack-level", "Only EACH and CASE pack levels can be received by scan", replayRetryable)},
	{usecases.ErrUnknownGtin, unprocessable("unknown-gtin", "The GTIN is not in the packaging view", replayRetryable)},

	{usecases.ErrVersionMismatch, problem{status: http.StatusPreconditionFailed, slug: "version-mismatch", title: "The If-Match version is stale", class: replayRetryable}},
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
