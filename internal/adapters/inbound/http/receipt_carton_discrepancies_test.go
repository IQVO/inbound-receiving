package http_test

import (
	"net/http"
	"reflect"
	"testing"
	"time"
)

const scanUnexpected = `{"kind":"SSCC","id":"340123450000000017"}`

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
	id := f.seedReceipt(t, "ASN-CF")
	f.clock.set(startOfDay.Add(15 * time.Minute))
	scan := f.post(t, "/receipts/"+id+"/scans", scanUnexpected)
	expectStatus(t, scan, http.StatusOK)
	f.clock.set(startOfDay.Add(20 * time.Minute))
	expectStatus(t, f.post(t, "/receipts/"+id+"/scans", `{"kind":"EACH_GTIN","id":"10001234567892"}`), http.StatusOK)

	got := cartonDiscrepancies(t, f.get(t, "/receipts/"+id))
	want := []any{
		map[string]any{"kind": "UnexpectedCarton", "sscc": "340123450000000017", "at": "2026-10-09T07:15:00Z"},
		map[string]any{"kind": "UnknownGtin", "actual": "10001234567892", "at": "2026-10-09T07:20:00Z"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cartonDiscrepancies = %v, want %v", got, want)
	}

	// A close does not hide them, and the closed receipt keeps its three line kinds only.
	closed := f.post(t, "/receipts/"+id+"/closures", "")
	expectStatus(t, closed, http.StatusOK)
	if got := cartonDiscrepancies(t, f.get(t, "/receipts/"+id)); !reflect.DeepEqual(got, want) {
		t.Fatalf("after close cartonDiscrepancies = %v, want %v", got, want)
	}
	for _, d := range closed.body["discrepancies"].([]any) {
		if k := d.(map[string]any)["kind"]; k != "Short" && k != "Over" && k != "Damaged" {
			t.Fatalf("line discrepancy kind %v leaked into discrepancies", k)
		}
	}
}
