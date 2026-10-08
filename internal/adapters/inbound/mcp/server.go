// Package mcp is the inbound Model Context Protocol adapter: it exposes this
// bounded context to the AI ecosystem as a second driving adapter over the
// same application-layer read use cases the REST adapter uses. It is built on
// the official MCP Go SDK and served over Streamable HTTP only.
//
// Per docs/adr/0005-mcp-server-adoption.md the surface is READ-ONLY: every
// tool reads inbound data (get_asn, list_asns, get_appointment,
// list_appointments, get_receipt, list_receipts, list_docks); writes stay on
// REST, and governance_test.go fails the build on a write-verb tool name.
// This package depends inward on the application layer and the domain only --
// never on an outbound adapter or the REST adapter -- and nothing else may
// depend on it (internal/architecture's TestMCPAdapterDependencyRule). The
// composition root (cmd/mcp) wires concrete repositories into the use cases.
// There is no auth of any kind (fleet-wide revert 2026-09-11;
// TestNoAuthMiddlewareReintroduced).
package mcp

import (
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// serverName and serverVersion identify this server in the MCP initialize
// handshake.
const (
	serverName    = "inbound-receiving-mcp"
	serverVersion = "1.0.0"
)

// instructions is what an MCP client is told about this server at initialize.
const instructions = "Inbound dock workflow of the warehouse (read-only): look up one advance ship notice with get_asn " +
	"(supplier, expected arrival, lines, state Registered/Receiving/Closed/Cancelled) or page through them with list_asns " +
	"(optionally by state); read one carrier dock appointment with get_appointment (door, window, ASNs, state " +
	"Booked/CheckedIn/Completed/Cancelled) or page through them with list_appointments (optionally by door, state and a " +
	"from/to time range); read one counted receipt with get_receipt (good and damaged counts per line, discrepancies " +
	"Short/Over/Damaged) or page through them with list_receipts (optionally by asn_number and state Open/Closed); " +
	"list the inbound dock doors this service knows with list_docks. Nothing here changes data: registering and cancelling " +
	"ASNs, booking, checking in and cancelling appointments, and opening, receiving into and closing receipts are REST " +
	"operations of inbound-receiving."

// NewServer builds the MCP server for this bounded context with every tool
// registered.
func NewServer(deps Deps) *mcp.Server {
	server := mcp.NewServer(
		&mcp.Implementation{Name: serverName, Version: serverVersion},
		&mcp.ServerOptions{Instructions: instructions},
	)
	deps.registerTools(server)
	return server
}

// Handler returns the Streamable HTTP handler for the MCP server.
func Handler(server *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
}
