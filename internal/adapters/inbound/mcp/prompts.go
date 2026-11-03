package mcp

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// receiveInboundShipmentSOP is the operator standard-operating-procedure the
// receive_inbound_shipment prompt hands to the model (Agentic Standard v2,
// AS-3; docs/adr/0018). Per the charter, prompts encode how to interpret
// results, when to escalate, and what "done" means; this is a read-only
// context, so the SOP only names tools — every state change (receiving
// counts, closing) is a human/REST operation the SOP escalates to.
const receiveInboundShipmentSOP = `You are helping warehouse receiving staff work an inbound shipment. Use only the read tools of this context; never assume state, and never claim to have changed anything: this context's agent surface is read-only — registering and cancelling ASNs, booking, checking in and cancelling appointments, and opening, receiving into and closing receipts are REST operations performed by people.

Procedure:
1. Identify the shipment: get_asn(asn_number) gives the supplier, the expected arrival (when announced), the state (Registered, Receiving, Closed or Cancelled) and the expected lines. An ASN in Registered has not started receiving; Closed or Cancelled ones are done.
2. Find its dock window: list_appointments (asn_numbers are not a filter, so match the appointment whose asn_numbers contains the ASN) or list_appointments(door:...) when the yard already assigned a door. CheckedIn means the carrier is at the door; Booked means the window is still ahead.
3. Follow the count: get_receipt(receipt_id) or list_receipts(asn_number:...) for the receipts counted against the ASN. While a receipt is Open its discrepancies are provisional; once Closed they are final.
4. Read the discrepancies per line: kind Short (fewer good units than expected), Over (more), Damaged (units counted as damaged). Each carries expected_qty against received/damaged counts.

Interpretation:
- A line with Short or Damaged discrepancies on a Closed receipt is a supplier/packing problem: it has already been handed to inventory-storage as counted facts, so raise it with the supplier-quality process, do not attempt to "fix" counts.
- An Open receipt with growing Damaged counts across reads suggests handling damage in progress: tell the dock lead which lines and how many units.
- An ASN in Receiving with no appointment window found means a walk-in or a booking gap: confirm with the yard which door it is at.

Escalate to a human when: an ASN is still Receiving well after its expected_arrival, a receipt stays Open across shifts (unclosed counts block the ASN from closing), or discrepancies on a closed receipt need a supplier claim. Closing a receipt, amending counts and cancelling are human/REST actions — surface them, do not perform them.

Done means: you have reported the ASN's state and lines, the dock window it is being received through, the state of every receipt counted against it, and any Short/Over/Damaged discrepancies with their quantities — and named the next human action where one is needed.`

// registerPrompts adds the workflow prompts (operational SOPs). Prompts are
// L1 by nature and, per the charter, need no tool Meta.
func (d Deps) registerPrompts(server *mcp.Server) {
	server.AddPrompt(&mcp.Prompt{
		Name:        "receive_inbound_shipment",
		Description: "Standard operating procedure for working an inbound shipment end to end: read the ASN, its dock window, the receipts counted against it and their discrepancies, and know when to escalate.",
	}, func(ctx context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "How to work an inbound shipment read-only: identify the ASN, find the dock window, follow the receipts and their discrepancies, and escalate closure and claims to humans.",
			Messages: []*mcp.PromptMessage{{
				Role:    "user",
				Content: &mcp.TextContent{Text: receiveInboundShipmentSOP},
			}},
		}, nil
	})
}
