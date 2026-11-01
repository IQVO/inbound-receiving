package http

import (
	"net/http"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// ---- Receipts ---------------------------------------------------------------------

// handleOpenReceipt backs POST /receipts (201).
func (s *Server) handleOpenReceipt(w http.ResponseWriter, r *http.Request) {
	var req openReceiptRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.OpenReceipt.Handle(r.Context(), usecases.OpenReceiptCommand{AsnNumber: req.AsnNumber, AppointmentID: req.AppointmentID})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeCreated(w, "/receipts/"+string(x.ID()), x.Version(), toReceipt(x))
}

// handleGetReceipt backs GET /receipts/{receiptId}.
func (s *Server) handleGetReceipt(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.GetReceipt.Handle(r.Context(), id)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, x.Version(), toReceipt(x))
}

// handleListReceipts backs GET /receipts?limit=&cursor=&asnNumber=&state=.
func (s *Server) handleListReceipts(w http.ResponseWriter, r *http.Request) {
	limit, cursor, err := listQuery(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	q := r.URL.Query()
	page, err := s.ListReceipts.Handle(r.Context(), usecases.ListReceiptsQuery{Limit: limit, Cursor: cursor, AsnNumber: q.Get("asnNumber"), State: q.Get("state")})
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := receiptPageResponse{Items: make([]receiptResponse, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, x := range page.Items {
		out.Items = append(out.Items, toReceipt(x))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleReceiveLine backs POST /receipts/{receiptId}/lines (201).
func (s *Server) handleReceiveLine(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req receiveLineRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.ReceiveLine.Handle(r.Context(), usecases.ReceiveLineCommand{
		ReceiptID: id, LineNo: req.LineNo, Quantity: req.Quantity, Condition: req.Condition, ExpectedVersion: version,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusCreated, x.Version(), toReceipt(x))
}

// handleCloseReceipt backs POST /receipts/{receiptId}/closures (200).
func (s *Server) handleCloseReceipt(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	x, err := s.CloseReceipt.Handle(r.Context(), usecases.CloseReceiptCommand{ReceiptID: id, ExpectedVersion: version})
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeVersioned(w, http.StatusOK, x.Version(), toReceipt(x))
}

// ---- Docks ----------------------------------------------------------------------------

// handleListDocks backs GET /docks.
func (s *Server) handleListDocks(w http.ResponseWriter, r *http.Request) {
	docks, err := s.ListDocks.Handle(r.Context())
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toDockList(docks))
}

// handleReceiveScan backs POST /receipts/{receiptId}/scans: 201 when the scan
// was accepted, 200 when it was refused with findings (nothing booked).
func (s *Server) handleReceiveScan(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var req receiveScanRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, r, err)
		return
	}
	version, err := expectedVersion(r)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out, err := s.ReceiveScan.Handle(r.Context(), usecases.ReceiveScanCommand{
		ReceiptID: id, Scan: req.Scan, Kind: req.Kind, ID: req.ID, Qty: req.Qty, ExpectedKind: req.ExpectedKind,
		Condition: req.Condition, Lot: req.Lot, Expiry: req.Expiry, CountedContents: req.counted(), ExpectedVersion: version,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusOK
	if out.Result.Accepted {
		status = http.StatusCreated
	}
	writeVersioned(w, status, out.Receipt.Version(), toScan(out))
}

// handleGetCartonLabel backs GET /receipts/{receiptId}/cartons/{lpn}/label:
// the DATA of a license plate label (R11); the floor client prints it.
func (s *Server) handleGetCartonLabel(w http.ResponseWriter, r *http.Request) {
	id, err := pathParam(r, "receiptId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	lpn, err := pathParam(r, "lpn")
	if err != nil {
		writeError(w, r, err)
		return
	}
	label, err := s.GetCartonLabel.Handle(r.Context(), id, lpn)
	if err != nil {
		writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toCartonLabel(label))
}
