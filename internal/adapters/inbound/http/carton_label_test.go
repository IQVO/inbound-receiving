package http_test

import (
	"net/http"
	"testing"
)

func TestCartonLabelOfACartonTheReceiptNeverAcceptedIs404(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-LBL")
	expectProblem(t, f.get(t, "/receipts/"+id+"/cartons/LPN-NOPE/label"), http.StatusNotFound, "carton-not-found")
}

func TestCartonLabelOfAnUnknownOrMalformedReceipt(t *testing.T) {
	f := newFixture(t)
	expectProblem(t, f.get(t, "/receipts/"+missingReceipt+"/cartons/LPN-X/label"), http.StatusNotFound, "receipt-not-found")
	expectProblem(t, f.get(t, "/receipts/not-an-id/cartons/LPN-X/label"), http.StatusBadRequest, "invalid-receipt-id")
}
