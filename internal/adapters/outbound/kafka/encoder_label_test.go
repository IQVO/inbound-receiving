package kafka

import (
	"strings"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func TestEncodeReceiptCartonReceivedCarriesTheSourceAndTheLabelData(t *testing.T) {
	ev := receipt.ReceiptCartonReceived{
		Header: receiptHeader(at(23, 14, 3)), SSCC: mustSSCC(t, testSSCC), Sealed: true, LPN: testSSCC, SSCCSource: receipt.SsccReceiver,
		Contents: []receipt.CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: mustGTIN(t, testEach), Qty: 12, Condition: receipt.ConditionGood}},
		Label: receipt.LabelData{
			BestBefore: "2027-03-31", Expiry: "2027-06-30", ProductionDate: "2026-09-01", PackagingDate: "2026-09-02",
			Serial: "SER1", PurchaseOrder: "PO77", ShipToGLN: "4012345000009", ShipForGLN: "7612345000008",
		},
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("receipt", "ReceiptCartonReceived", rcptID, "2026-10-23T14:03:00Z",
		`{"receipt_id":"`+rcptID+`","asn_number":"ASN-1001","sscc":"`+testSSCC+`","sealed":true,"lpn":"`+testSSCC+`",`+
			`"contents":[{"line_no":1,"sku":"SKU-1","gtin":"`+testEach+`","qty":12,"condition":"Good"}],"received_at":"2026-10-23T14:03:00Z",`+
			`"sscc_source":"receiver","best_before":"2027-03-31","expiry":"2027-06-30","production_date":"2026-09-01","packaging_date":"2026-09-02",`+
			`"serial":"SER1","purchase_order":"PO77","ship_to_gln":"4012345000009","ship_for_gln":"7612345000008"}`)
	assertRow(t, msgs[0], "receipt.ReceiptCartonReceived", rcptID, "ASN-1001", want)
}

func TestEncodeLabelFindingsCarryTheSupplierAndHaveNoW13Code(t *testing.T) {
	cases := map[string]struct {
		d    receipt.CartonDiscrepancy
		data string
	}{
		"label non compliant": {
			receipt.CartonDiscrepancy{Kind: receipt.KindLabelNonCompliant, Expected: "conflicting_ais", Actual: "]C10113041090004824", SupplierRef: "ACME"},
			`{"receipt_id":"` + rcptID + `","kind":"LabelNonCompliant","expected":"conflicting_ais","actual":"]C10113041090004824","at":"2026-10-23T14:05:00Z","cbv_disposition":"non_conformant","supplier_ref":"ACME"}`,
		},
		"wrong destination": {
			receipt.CartonDiscrepancy{Kind: receipt.KindWrongDestination, SSCC: mustSSCC(t, testSSCC), Actual: "7612345000008", SupplierRef: "ACME"},
			`{"receipt_id":"` + rcptID + `","sscc":"` + testSSCC + `","kind":"WrongDestination","actual":"7612345000008","at":"2026-10-23T14:05:00Z","cbv_disposition":"mismatch_instance","supplier_ref":"ACME"}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ev := receipt.CartonDiscrepancyRaised{Header: receiptHeader(at(23, 14, 5)), CartonDiscrepancy: tc.d}
			msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
			if err != nil {
				t.Fatal(err)
			}
			assertRow(t, msgs[0], "receipt.CartonDiscrepancyRaised", rcptID, "ASN-1001", envelope("receipt", "CartonDiscrepancyRaised", rcptID, "2026-10-23T14:05:00Z", tc.data))
		})
	}
}

func TestEncodeAdviceLinesCarryTheEarliestDates(t *testing.T) {
	ev := adviceIssued(t)
	ev.Lines[0].EarliestBestBefore, ev.Lines[0].EarliestExpiry = "2027-03-31", "2027-06-30"
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := adviceDataWith(`"discrepancies":[{"kind":"Short","quantity":6,"w13_code":"02","cbv_disposition":"mismatch_quantity"},`+
		`{"kind":"Hold","quantity":5,"w13_code":"09","cbv_disposition":"non_conformant"}]},`,
		`"discrepancies":[{"kind":"Short","quantity":6,"w13_code":"02","cbv_disposition":"mismatch_quantity"},`+
			`{"kind":"Hold","quantity":5,"w13_code":"09","cbv_disposition":"non_conformant"}],`+
			`"earliest_best_before":"2027-03-31","earliest_expiry":"2027-06-30"},`)
	assertRow(t, msgs[0], "receipt.ReceivingAdviceIssued", rcptID, "ASN-1001", envelope("receipt", "ReceivingAdviceIssued", rcptID, "2026-10-09T09:30:00Z", want))
}

// adviceDataWith replaces one fragment of the golden advice payload.
func adviceDataWith(old, replacement string) string {
	if !strings.Contains(adviceData, old) {
		panic("fragment not in the golden advice")
	}
	return strings.Replace(adviceData, old, replacement, 1)
}
