// Agentic Standard v2 (AS-3; docs/adr/0018): the MCP surface is complete —
// tools plus at least one resource and one prompt. These drive the server
// through the SDK's in-memory transport over the seeded in-memory repos, the
// same harness the tool tests use.
package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestSurface_ResourcesListedAndReadable pins the receipt read-model
// resource: exactly the one template is advertised, and reading a concrete
// URI returns the same JSON the get_receipt tool returns.
func TestSurface_ResourcesListedAndReadable(t *testing.T) {
	h := newHarness(t)
	h.seedAsn(t, "ASN-1001")
	receiptID := h.seedReceipt(t, "ASN-1001", "")

	templates, err := h.session.ListResourceTemplates(t.Context(), nil)
	if err != nil {
		t.Fatalf("list resource templates: %v", err)
	}
	if len(templates.ResourceTemplates) != 1 {
		t.Fatalf("advertised %d resource templates, want exactly 1 (the receipt read model)", len(templates.ResourceTemplates))
	}
	tmpl := templates.ResourceTemplates[0]
	if !strings.Contains(tmpl.URITemplate, "warehouse://inbound/receipts/{receiptId}") {
		t.Fatalf("uriTemplate = %q, want the warehouse://inbound/receipts/{receiptId} template", tmpl.URITemplate)
	}

	res, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "warehouse://inbound/receipts/" + receiptID})
	if err != nil {
		t.Fatalf("read resource: %v", err)
	}
	if len(res.Contents) != 1 || res.Contents[0].URI != "warehouse://inbound/receipts/"+receiptID {
		t.Fatalf("contents = %+v, want the requested uri back", res.Contents)
	}
	var body struct {
		ReceiptID string `json:"receipt_id"`
		AsnNumber string `json:"asn_number"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal([]byte(res.Contents[0].Text), &body); err != nil {
		t.Fatalf("resource body is not the receipt JSON: %v", err)
	}
	if body.ReceiptID != receiptID || body.AsnNumber != "ASN-1001" || body.State != "Open" {
		t.Fatalf("resource body = %+v, want receipt %s of ASN-1001 in state Open", body, receiptID)
	}
}

// TestSurface_ResourceRejectsMalformedURIs proves a model-supplied URI that
// does not match the template is rejected, not silently defaulted.
func TestSurface_ResourceRejectsMalformedURIs(t *testing.T) {
	h := newHarness(t)
	for _, uri := range []string{
		"warehouse://inbound/receipts/",
		"warehouse://inbound/receipts/a/b",
		"warehouse://outbound/receipts/x",
	} {
		if _, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("read %q: expected an error, got none", uri)
		}
	}
}

// TestSurface_UnknownReceiptIsAReadError keeps the resource on the REST
// contract's error surface: an unknown id is a read failure carrying the
// receipt-not-found slug, never leaked infrastructure text.
func TestSurface_UnknownReceiptIsAReadError(t *testing.T) {
	h := newHarness(t)
	_, err := h.session.ReadResource(t.Context(), &sdk.ReadResourceParams{URI: "warehouse://inbound/receipts/rcpt-1b4e28ba-2fa1-4d3c-8f6e-0a1b2c3d4e5f"})
	if err == nil {
		t.Fatal("expected an error for an unknown receipt id")
	}
	if !strings.Contains(err.Error(), "receipt-not-found") {
		t.Fatalf("err = %v, want the receipt-not-found slug", err)
	}
}

// TestSurface_PromptGet pins the operator SOP prompt: exactly one prompt is
// advertised, and getting it returns the SOP text with no arguments required.
func TestSurface_PromptGet(t *testing.T) {
	h := newHarness(t)

	list, err := h.session.ListPrompts(t.Context(), nil)
	if err != nil {
		t.Fatalf("list prompts: %v", err)
	}
	if len(list.Prompts) != 1 {
		t.Fatalf("advertised %d prompts, want exactly 1 (the receiving SOP)", len(list.Prompts))
	}
	if list.Prompts[0].Name != "receive_inbound_shipment" {
		t.Fatalf("prompt name = %q, want receive_inbound_shipment", list.Prompts[0].Name)
	}

	res, err := h.session.GetPrompt(t.Context(), &sdk.GetPromptParams{Name: "receive_inbound_shipment"})
	if err != nil {
		t.Fatalf("get prompt: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want one user message", res.Messages)
	}
	sop, ok := res.Messages[0].Content.(*sdk.TextContent)
	if !ok {
		t.Fatalf("prompt content = %T, want text", res.Messages[0].Content)
	}
	for _, needle := range []string{"get_asn", "list_appointments", "list_receipts", "Escalate to a human", "Done means"} {
		if !strings.Contains(sop.Text, needle) {
			t.Errorf("SOP text does not mention %q", needle)
		}
	}
	if strings.Contains(sop.Text, "call register") || strings.Contains(sop.Text, "close the receipt yourself") {
		t.Errorf("SOP text must not instruct a write; the charter is read-only")
	}
}
