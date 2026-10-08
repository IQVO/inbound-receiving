// Package idempotency is the vocabulary of ports.IdempotencyStore, the
// store behind the Idempotency-Key middleware of the HTTP adapter (fleet
// rule fleet/idempotency-and-outbox.md). It carries no HTTP or database
// types, so both adapters can speak it without importing each other.
package idempotency

// Request identifies one resource-creating request. BodyHash fingerprints
// method, path and body, so the same key reused for anything else is
// detectable.
type Request struct {
	Key      string
	BodyHash string
}

// Response is the outcome the first execution produced, replayed verbatim
// for every retry of the same key and body.
type Response struct {
	Status int
	Header map[string][]string
	Body   []byte
}

// Outcome says how Do resolved a request.
type Outcome int

// The outcomes of IdempotencyStore.Do.
const (
	// Fresh: the key was new; the handler ran and its response was stored.
	Fresh Outcome = iota
	// Replayed: the key was seen with the same request; the stored
	// response is returned and the handler did not run.
	Replayed
	// KeyReused: the key was seen with a different request.
	KeyReused
)
