package http_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	cartonA  = "003456789012345670"
	cartonB  = "003456789012345687"
	palletP  = "003456789099999995"
	eachGTIN = "10001234567892"
	caseGTIN = "20001234567851"
)

// palletAsnBody announces two homogeneous cases of 20 SKU-1 on ONE pallet.
const palletAsnBody = `{"asnNumber":"ASN-C","supplierRef":"ACME","expectedArrival":"2026-10-10T08:00:00Z",` +
	`"lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40}],"cartons":[` +
	`{"sscc":"` + cartonA + `","parentSscc":"` + palletP + `","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"` + eachGTIN + `","qty":20}]},` +
	`{"sscc":"` + cartonB + `","parentSscc":"` + palletP + `","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"` + eachGTIN + `","qty":20}]}]}`

func (f *fixture) seedPackaging(t *testing.T) {
	t.Helper()
	from := startOfDay.Add(-24 * time.Hour)
	for _, l := range []repository.PackLevel{
		{GTIN: gtinOf(t, eachGTIN), SKU: "SKU-1", Level: shared.PackEach, UnitsPerPack: 1, EffectiveFrom: from, Version: 1},
		{GTIN: gtinOf(t, caseGTIN), SKU: "SKU-1", Level: shared.PackCase, ChildGTIN: gtinOf(t, eachGTIN), QtyOfChild: 20, UnitsPerPack: 20, EffectiveFrom: from, Version: 1},
	} {
		if _, err := f.packaging.ApplyDeclared(context.Background(), l); err != nil {
			t.Fatal(err)
		}
	}
}

func gtinOf(t *testing.T, s string) gs1.GTIN {
	t.Helper()
	g, err := gs1.ParseGTIN(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// seedPalletReceipt registers ASN-C (two cases on one pallet), seeds the
// PackagingView and opens a receipt on it.
func (f *fixture) seedPalletReceipt(t *testing.T) string {
	t.Helper()
	f.seedPackaging(t)
	f.mustPost(t, "/asns", palletAsnBody, http.StatusCreated)
	return str(f.mustPost(t, "/receipts", `{"asnNumber":"ASN-C"}`, http.StatusCreated), "receiptId")
}

func (f *fixture) scanRaw(t *testing.T, id, raw string) response {
	t.Helper()
	return f.post(t, "/receipts/"+id+"/scans", `{"scan":"`+raw+`"}`)
}

func TestScanOfANonCompliantLabelIsRefusedAndRecorded(t *testing.T) {
	f := newFixture(t)
	id := f.seedPalletReceipt(t)
	r := f.scanRaw(t, id, "]C101"+eachGTIN+"02"+eachGTIN+"3720")
	expectStatus(t, r, http.StatusOK)
	d := r.body["discrepancies"].([]any)[0].(map[string]any)
	if r.body["accepted"] != false || d["kind"] != "LabelNonCompliant" || d["expected"] != "conflicting_ais" ||
		d["supplierRef"] != "ACME" || d["cbvDisposition"] != "non_conformant" || d["w13Code"] != nil {
		t.Fatalf("scan = %s", r.raw)
	}
	got := f.get(t, "/receipts/"+id)
	kept := got.body["cartonDiscrepancies"].([]any)
	if len(kept) != 1 || kept[0].(map[string]any)["kind"] != "LabelNonCompliant" || kept[0].(map[string]any)["supplierRef"] != "ACME" {
		t.Fatalf("receipt = %s", got.raw)
	}
}

func TestScanOfJunkAMissingOrAClosedReceiptKeepsItsAnswer(t *testing.T) {
	f := newFixture(t)
	id := f.seedPalletReceipt(t)
	expectProblem(t, f.scanRaw(t, id, "hello"), http.StatusBadRequest, "invalid-scan")
	expectProblem(t, f.scanRaw(t, missingReceipt, "]C101"+eachGTIN+"02"+eachGTIN+"3720"), http.StatusNotFound, "receipt-not-found")
	f.mustPost(t, "/receipts/"+id+"/closures", "", http.StatusOK)
	expectProblem(t, f.scanRaw(t, id, "]C101"+eachGTIN+"02"+eachGTIN+"3720"), http.StatusConflict, "receipt-closed")
}

func TestScanOfALabelForAnotherGLNIsRefusedAsWrongDestination(t *testing.T) {
	f := newFixture(t)
	sites, err := receipt.ParseSiteGLNs("4012345000009")
	if err != nil {
		t.Fatal(err)
	}
	f.scan.SiteGLNs = sites
	id := f.seedPalletReceipt(t)
	r := f.scanRaw(t, id, "]C100"+cartonA+"4107612345000008")
	expectStatus(t, r, http.StatusOK)
	d := r.body["discrepancies"].([]any)[0].(map[string]any)
	if r.body["accepted"] != false || d["kind"] != "WrongDestination" || d["actual"] != "7612345000008" || d["sscc"] != cartonA ||
		d["cbvDisposition"] != "mismatch_instance" {
		t.Fatalf("scan = %s", r.raw)
	}
	ok := f.scanRaw(t, id, "]C100"+cartonA+"4104012345000009")
	expectStatus(t, ok, http.StatusCreated)
}

func TestTheEarliestBestBeforeOfAMixedPalletIsReported(t *testing.T) {
	f := newFixture(t)
	id := f.seedPalletReceipt(t)
	a := f.scanRaw(t, id, "]C100"+cartonA+"15270300"+"17270630")
	expectStatus(t, a, http.StatusCreated)
	c := a.body["cartons"].([]any)[0].(map[string]any)
	if c["ssccSource"] != "supplier" || c["bestBefore"] != "2027-03-31" || c["expiry"] != "2027-06-30" {
		t.Fatalf("carton = %v", c)
	}
	expectStatus(t, f.scanRaw(t, id, "]C100"+cartonB+"15270215"), http.StatusCreated)

	got := f.get(t, "/receipts/"+id)
	line := got.body["lines"].([]any)[0].(map[string]any)
	if line["earliestBestBefore"] != "2027-02-15" || line["earliestExpiry"] != "2027-06-30" {
		t.Fatalf("line = %v", line)
	}
	pallets := got.body["pallets"].([]any)
	p := pallets[0].(map[string]any)
	if len(pallets) != 1 || p["parentSscc"] != palletP || p["cartonCount"] != float64(2) || p["earliestBestBefore"] != "2027-02-15" || p["earliestExpiry"] != "2027-06-30" {
		t.Fatalf("pallets = %v", pallets)
	}
}

func TestAReceiptWithNothingReceivedHasNoDatesAndNoPallets(t *testing.T) {
	f := newFixture(t)
	id := f.seedPalletReceipt(t)
	got := f.get(t, "/receipts/"+id)
	line := got.body["lines"].([]any)[0].(map[string]any)
	if _, ok := line["earliestBestBefore"]; ok {
		t.Fatalf("line = %v", line)
	}
	if _, ok := got.body["pallets"]; ok {
		t.Fatalf("pallets = %v", got.body["pallets"])
	}
}

func TestAssignSsccIsRefusedWith409UntilAPrefixIsConfigured(t *testing.T) {
	f := newFixture(t)
	id := f.seedPalletReceipt(t)
	r := f.post(t, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`)
	expectProblem(t, r, http.StatusConflict, "sscc-prefix-not-configured")
	if got := f.get(t, "/receipts/"+id); got.body["version"] != float64(1) {
		t.Fatalf("a refused assignment changed the receipt: %s", got.raw)
	}
}

func (f *fixture) configurePrefix(t *testing.T, prefix string) {
	t.Helper()
	p, err := receipt.ParseCompanyPrefix(prefix)
	if err != nil {
		t.Fatal(err)
	}
	f.assign.Prefix = p
}

func TestAssignSsccAllocatesAnSSCCAndReturnsWhatAPrinterNeeds(t *testing.T) {
	f := newFixture(t)
	f.configurePrefix(t, "0614141")
	id := f.seedPalletReceipt(t)
	r := f.post(t, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`)
	expectStatus(t, r, http.StatusCreated)
	want := map[string]any{
		"sscc": "006141410000000012", "lpn": "006141410000000012", "humanReadable": "(00)006141410000000012",
		"elementString": "00006141410000000012", "symbology": "GS1-128", "ssccSource": "receiver", "sku": "SKU-1", "units": float64(20),
	}
	for k, v := range want {
		if r.body[k] != v {
			t.Errorf("%s = %v, want %v (%s)", k, r.body[k], v, r.raw)
		}
	}
	if r.header.Get("ETag") == "" {
		t.Error("the answer carries the receipt's ETag")
	}
	if d := r.body["discrepancies"].([]any); len(d) != 1 || d[0].(map[string]any)["kind"] != "UnlabelledCarton" {
		t.Fatalf("discrepancies = %v", r.body["discrepancies"])
	}
	label := f.get(t, "/receipts/"+id+"/cartons/006141410000000012/label")
	expectStatus(t, label, http.StatusOK)
	if label.body["humanReadable"] != "(00)006141410000000012" {
		t.Fatalf("label = %s", label.raw)
	}
	second := f.post(t, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`)
	if second.body["sscc"] != "006141410000000029" {
		t.Fatalf("second sscc = %v", second.body["sscc"])
	}
}

func TestAssignSsccIsIdempotentOnTheKey(t *testing.T) {
	f := newFixture(t)
	f.configurePrefix(t, "0614141")
	id := f.seedPalletReceipt(t)
	h := map[string]string{"Idempotency-Key": "assign-1"}
	first := f.request(t, http.MethodPost, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`, h)
	again := f.request(t, http.MethodPost, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`, h)
	expectStatus(t, first, http.StatusCreated)
	expectStatus(t, again, http.StatusCreated)
	if again.header.Get("Idempotency-Replayed") != "true" || again.body["sscc"] != first.body["sscc"] {
		t.Fatalf("replay = %d %s", again.status, again.raw)
	}
	line := f.get(t, "/receipts/"+id).body["lines"].([]any)[0].(map[string]any)
	if line["receivedGood"] != float64(20) {
		t.Fatalf("a replay must not receive twice: %v", line)
	}
	other := f.request(t, http.MethodPost, "/receipts/"+id+"/sscc-assignments", `{"gtin":"`+caseGTIN+`","lot":"L1"}`, h)
	expectProblem(t, other, http.StatusConflict, "idempotency-key-reused")
}

func TestAssignSsccRefusals(t *testing.T) {
	f := newFixture(t)
	f.configurePrefix(t, "0614141")
	id := f.seedPalletReceipt(t)
	path := "/receipts/" + id + "/sscc-assignments"
	expectProblem(t, f.request(t, http.MethodPost, path, `{"gtin":"`+caseGTIN+`"}`, nil), http.StatusBadRequest, "idempotency-key-required")
	expectProblem(t, f.post(t, path, `{"gtin":"`+caseGTIN+`","extra":1}`), http.StatusBadRequest, "malformed-request")
	expectProblem(t, f.post(t, path, `{"gtin":"123"}`), http.StatusBadRequest, "invalid-gtin")
	expectProblem(t, f.post(t, path, `{"gtin":"06291041500459"}`), http.StatusUnprocessableEntity, "unknown-gtin")
	expectProblem(t, f.post(t, path, `{"gtin":"`+eachGTIN+`"}`), http.StatusUnprocessableEntity, "scan-kind-mismatch")
	expectProblem(t, f.post(t, "/receipts/"+missingReceipt+"/sscc-assignments", `{"gtin":"`+caseGTIN+`"}`), http.StatusNotFound, "receipt-not-found")
	f.mustPost(t, "/receipts/"+id+"/closures", "", http.StatusOK)
	expectProblem(t, f.post(t, path, `{"gtin":"`+caseGTIN+`"}`), http.StatusConflict, "receipt-closed")
}
