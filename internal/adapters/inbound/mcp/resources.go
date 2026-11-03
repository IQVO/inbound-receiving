package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// mimeTypeJSON is the MIME type of every resource this server serves.
const mimeTypeJSON = "application/json"

// receiptResourceTemplate is the URI template of the receipt read-model
// resource (Agentic Standard v2, AS-3; docs/adr/0018): a SCOPED view this
// context owns — exactly one receipt with its counted lines and typed
// discrepancies — never a table dump. It is backed by the same GetReceipt
// read use case the get_receipt tool and REST GET /receipts/{id} use; the
// concrete receiptId is read from the request URI at call time.
const receiptResourceTemplate = "warehouse://inbound/receipts/{receiptId}"

// registerResources adds the scoped read-model resources. Resources are L1 by
// nature (pure reads) and, per the charter, need no tool Meta.
func (d Deps) registerResources(server *mcp.Server) {
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: receiptResourceTemplate,
		Name:        "receipt",
		Description: "One counted receipt by id: state (Open or Closed), per-line expected, received-good and " +
			"received-damaged quantities, opened_at, closed_at (omitted while open) and the discrepancies " +
			"(Short, Over or Damaged; provisional while Open, final once Closed).",
		MIMEType: mimeTypeJSON,
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		receiptID, err := parseReceiptResourceURI(req.Params.URI)
		if err != nil {
			return nil, err
		}
		r, err := d.GetReceipt.Handle(ctx, receiptID)
		if err != nil {
			return nil, mapError(err)
		}
		body, err := json.Marshal(toReceipt(r))
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      req.Params.URI,
				MIMEType: mimeTypeJSON,
				Text:     string(body),
			}},
		}, nil
	})
}

// parseReceiptResourceURI extracts the receiptId from a
// warehouse://inbound/receipts/{receiptId} URI. The segment is
// model-supplied and therefore untrusted; a malformed URI or an empty
// segment is rejected rather than silently defaulted.
func parseReceiptResourceURI(uri string) (string, error) {
	const scheme = "warehouse://inbound/receipts/"
	if !strings.HasPrefix(uri, scheme) {
		return "", fmt.Errorf("resource uri %q must match %s", uri, receiptResourceTemplate)
	}
	id := strings.TrimPrefix(uri, scheme)
	if id == "" || strings.Contains(id, "/") {
		return "", fmt.Errorf("resource uri %q must match %s", uri, receiptResourceTemplate)
	}
	return id, nil
}
