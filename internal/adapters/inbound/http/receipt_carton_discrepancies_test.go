package http_test

import (
	"fmt"
	"net/http"
	"reflect"
	"testing"
	"time"
)

const scanUnexpected = `{"kind":"SSCC","id":"340123450000000017"}`

// scanUnexpectedCartonAsnBody announces one carton of 20 SKU-1: the scan above
// hits a HIERARCHICAL ASN (ADR 0025 keeps its unknown SSCC an
// UnexpectedCarton) with an SSCC the manifest does not carry.
const scanUnexpectedCartonAsnBody = `{"asnNumber":"%s","supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],` +
	`"cartons":[{"sscc":"340123450000000024","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}`

// cartonDiscrepancies reads the field of a Receipt response, failing when it is
// absent, null or not an array (the contract: always an array, [] when none).
func cartonDiscrepancies(t *testing.T, r response) []any {
	t.Helper()
	v, present := r.body["cartonDiscrepancies"]
	list, isList := v.([]any)
	if !present || !isList {
		t.Fatalf("cartonDiscrepancies = %#v (present=%v), want an array in %s", v, present, r.raw)
	}
	return list
}

func TestGetReceiptWithoutCartonFindingsAnswersAnEmptyArray(t *testing.T) {
	f := newFixture(t)
	id := f.seedReceipt(t, "ASN-NF")
	for name, r := range map[string]response{
		"get":  f.get(t, "/receipts/"+id),
		"list": f.get(t, "/receipts?asnNumber=ASN-NF"),
	} {
		got := r
		if name == "list" {
			items, _ := r.body["items"].([]any)
			if len(items) != 1 {
				t.Fatalf("list = %s", r.raw)
			}
			got = response{body: items[0].(map[string]any), raw: r.raw}
		}
		if list := cartonDiscrepancies(t, got); len(list) != 0 {
			t.Fatalf("%s: cartonDiscrepancies = %v, want none", name, list)
		}
	}
}

func TestGetReceiptListsTheCartonFindingsRaisedByScans(t *testing.T) {
	f := newFixture(t)
	f.mustPost(t, "/asns", fmt.Sprintf(scanUnexpectedCartonAsnBody, "ASN-CF"), http.StatusCreated)
	id := str(f.mustPost(t, "/receipts", `{"asnNumber":"ASN-CF"}`, http.StatusCreated), "receiptId")
	f.clock.set(startOfDay.Add(15 * time.Minute))
	scan := f.post(t, "/receipts/"+id+"/scans", scanUnexpected)
	expectStatus(t, scan, http.StatusOK)
	f.clock.set(startOfDay.Add(20 * time.Minute))
	expectStatus(t, f.post(t, "/receipts/"+id+"/scans", `{"kind":"EACH_GTIN","id":"10001234567892"}`), http.StatusOK)

	got := cartonDiscrepancies(t, f.get(t, "/receipts/"+id))
	want := []any{
		map[string]any{"kind": "UnexpectedCarton", "sscc": "340123450000000017", "at": "2026-10-09T07:15:00Z", "w13Code": "05", "cbvDisposition": "mismatch_instance"},
		map[string]any{"kind": "UnknownGtin", "actual": "10001234567892", "at": "2026-10-09T07:20:00Z", "w13Code": "05", "cbvDisposition": "mismatch_class"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cartonDiscrepancies = %v, want %v", got, want)
	}

	// A close does not hide them (and raises the MissingCarton of the
	// manifest carton that was never accepted), and the closed receipt keeps
	// its three line kinds only.
	closed := f.post(t, "/receipts/"+id+"/closures", "")
	expectStatus(t, closed, http.StatusOK)
	afterClose := append(append([]any{}, want...), map[string]any{
		"kind": "MissingCarton", "expected": "340123450000000024", "at": "2026-10-09T07:20:00Z", "w13Code": "02", "cbvDisposition": "mismatch_quantity",
	})
	if got := cartonDiscrepancies(t, f.get(t, "/receipts/"+id)); !reflect.DeepEqual(got, afterClose) {
		t.Fatalf("after close cartonDiscrepancies = %v, want %v", got, afterClose)
	}
	for _, d := range closed.body["discrepancies"].([]any) {
		if k := d.(map[string]any)["kind"]; k != "Short" && k != "Over" && k != "Damaged" {
			t.Fatalf("line discrepancy kind %v leaked into discrepancies", k)
		}
	}
}
