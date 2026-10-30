// Package a2a is the inbound Agent2Agent adapter: it exposes this bounded
// context to the fleet's agent ecosystem as a third driving adapter over the
// same application-layer read use cases the REST and MCP adapters use. It is
// built on the official A2A Go SDK (a2aproject/a2a-go) and speaks JSON-RPC 2.0
// over HTTP, with the Agent Card served at the well-known path.
//
// Per docs/adr/0016-a2a-inbound-adapter.md, A2A is the DELEGATION channel
// between bounded contexts: an agent client sends a goal-shaped skill request
// as a structured message part and this agent answers from its own state
// through its own use cases. It is not a data-sync channel: facts between
// contexts still travel exclusively as CloudEvents on Kafka (ADR 0004),
// published through the transactional outbox.
//
// This agent is READ-ONLY, exactly like the MCP adapter (ADR 0005): both
// skills read the inbound dock workflow. Registering and cancelling ASNs,
// booking, checking in and cancelling appointments, and opening, receiving
// into and closing receipts stay REST-only, because their events feed other
// contexts and their idempotency and version guards live on the REST
// contract. This package never writes, never calls another bounded context
// (CLAUDE.md rule 5) and contains no outbound A2A client.
//
// Like the MCP adapter, this package depends inward on the application layer
// (use cases) and the domain only, never on an outbound adapter, and nothing
// else may depend on it (internal/architecture's TestA2AAdapterDependencyRule).
// The composition root (cmd/a2a) wires the concrete repositories into the use
// cases. Domain structs never leak across the message boundary.
package a2a

import (
	"context"
	"iter"
	"net/http"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// Config carries the deployment-specific identity of this agent: the absolute
// URL clients use to reach its JSON-RPC endpoint (advertised on the Agent
// Card) and the card version. It is supplied by the composition root from
// A2A_AGENT_URL; the card MUST advertise a URL the fleet's agent clients can
// actually dial (in-cluster: http://<release>-a2a.<ns>.svc.cluster.local:8093/).
type Config struct {
	AgentURL string
	Version  string
}

// Deps is everything the A2A skills need, injected by the composition root.
// It carries the SAME read use cases the REST and MCP adapters use; the
// adapter never constructs an outbound adapter itself, and it holds no write
// use case at all (TestA2AAdapterIsReadOnlyAndHasNoOutboundClient).
type Deps struct {
	// GetAsn is the existing read use case, reused unchanged.
	GetAsn *usecases.GetAsn
	// ListReceipts is the existing read use case, reused unchanged: the
	// receipts of one ASN (filter asnNumber).
	ListReceipts *usecases.ListReceipts
	// ListAppointments is the existing read use case, reused unchanged: the
	// dock appointments, filtered by door, state and a time range.
	ListAppointments *usecases.ListAppointments
}

// Card builds this agent's Agent Card (docs/adr/0016). The surface is two
// curated, goal-shaped READ skills derived from the MCP tools (ADR 0005):
// asn_receiving_status composes get_asn and list_receipts into "where is this
// ASN in the dock workflow", dock_schedule is list_appointments shaped as
// "what is booked at the docks". This context's MCP charter is entirely
// read-only, so the agent is read-only too and never widens what the context
// exposes. The card declares no security schemes on purpose: this surface is
// unauthenticated by the same fleet-wide decision as REST and MCP; schemes are
// added here, not in a redesign, if that decision is ever reversed.
func Card(cfg Config) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:        "inbound-receiving",
		Description: "Inbound Receiving agent for the Warehouse Management System (read-only): answers where an advance ship notice stands in the inbound dock workflow (its state and lines, the receipts counted against it and the Short, Over or Damaged discrepancies they found) and what is booked at the dock doors (carrier appointments by door, state and time window). It never registers, books, receives or closes anything, and it does not decide stow or putaway.",
		Version:     cfg.Version,
		Provider: &a2a.AgentProvider{
			Org: "IQVO",
			URL: "https://github.com/IQVO/inbound-receiving",
		},
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(cfg.AgentURL, a2a.TransportProtocolJSONRPC),
		},
		DefaultInputModes:  []string{"application/json", "text/plain"},
		DefaultOutputModes: []string{"application/json", "text/plain"},
		Capabilities:       a2a.AgentCapabilities{Streaming: false},
		Skills: []a2a.AgentSkill{
			{
				ID:          skillAsnReceivingStatus,
				Name:        "Receiving status of an ASN",
				Description: "Composed read for one advance ship notice by asnNumber: the ASN (supplierRef, expectedArrival when announced, state Registered, Receiving, Closed or Cancelled, lines with expectedQty) and the receipts counted against it (state Open or Closed, door and appointment when not a walk-in, per-line receivedGood and receivedDamaged, and the discrepancies Short, Over or Damaged: provisional while the receipt is Open, final once Closed). Answers a 404 problem for an unknown ASN. Persists nothing.",
				Tags:        []string{"inbound", "asn", "receipt", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"asn_receiving_status","arguments":{"asnNumber":"ASN-1001"}}`,
				},
			},
			{
				ID:          skillDockSchedule,
				Name:        "Dock appointment schedule",
				Description: "Read the carrier dock appointments in ascending window-start order, optionally only those at one door, in one state (Booked, CheckedIn, Completed or Cancelled) and/or overlapping a from/to range (RFC 3339 instants: from keeps appointments whose window ends after it, to those whose window starts before it). Each entry carries appointmentId, doorCode, carrier, windowStart, windowEnd (UTC), the asnNumbers it covers and its state. At most limit entries (1..100, default 50) are returned; truncated is true when more exist, so narrow the range instead of paging. Persists nothing.",
				Tags:        []string{"inbound", "dock", "appointment", "read"},
				InputModes:  []string{"application/json"},
				Examples: []string{
					`{"skill":"dock_schedule","arguments":{}}`,
					`{"skill":"dock_schedule","arguments":{"door":"DOOR-3","state":"Booked","from":"2026-10-29T00:00:00Z","to":"2026-10-30T00:00:00Z","limit":20}}`,
				},
			},
		},
	}
}

// NewExecutor returns the AgentExecutor that maps an incoming message to a
// skill and answers with a single agent Message (a message-only agent in A2A
// terms): both curated skills are transactional request/response reads, so
// there is no long-running Task lifecycle to track in this first A2A surface.
// The response carries the caller's contextId (and taskId when the client
// referenced one) so a delegating saga can group its interactions.
func NewExecutor(deps Deps) a2asrv.AgentExecutor {
	return a2asrv.AgentExecutorFunc(func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			yield(answer(ctx, deps, ec), nil)
		}
	})
}

// Handler wires the executor into the transport-agnostic A2A request handler
// and wraps it in the JSON-RPC 2.0 HTTP handler, the only transport this
// agent ships (docs/adr/0016), mirroring ADR 0005's "Streamable HTTP only"
// minimalism for MCP.
func Handler(executor a2asrv.AgentExecutor) http.Handler {
	return a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
}
