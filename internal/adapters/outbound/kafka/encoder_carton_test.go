package kafka

import (
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

const (
	testSSCC   = "003456789012345670"
	testPallet = "003456789099999995"
	testEach   = "10001234567892"
	testCase   = "20001234567851"
)

func mustSSCC(t *testing.T, s string) gs1.SSCC {
	t.Helper()
	v, err := gs1.ParseSSCC(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func mustGTIN(t *testing.T, s string) gs1.GTIN {
	t.Helper()
	v, err := gs1.ParseGTIN(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestEncodeAsnRegisteredWithCartons(t *testing.T) {
	_, events, err := asn.RegisterDraft(asn.Draft{
		Number: "ASN-1001", SupplierRef: "ACME", OwnerRef: "tenant-7f3a",
		Lines: []asn.LineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 12}},
		Cartons: []asn.CartonInput{{
			SSCC: testSSCC, ParentSSCC: testPallet, Kind: "HOMOGENEOUS_CASE", GrossWeightG: 5000,
			Dims:     &asn.DimsInput{LengthMm: 400, WidthMm: 300, HeightMm: 200},
			Contents: []asn.ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: testEach, Qty: 12, Lot: "L2310", Expiry: "2027-10-31"}},
		}},
	}, at(8, 12, 0))
	if err != nil {
		t.Fatal(err)
	}
	e := &Encoder{NewID: fixedID}
	msgs, err := e.EncodeAsn(events...)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("EncodeAsn = %v, %v", msgs, err)
	}
	want := envelope("asn", "ASNRegistered", "ASN-1001", "2026-10-08T12:00:00Z",
		`{"asn_number":"ASN-1001","supplier_ref":"ACME","owner_ref":"tenant-7f3a",`+
			`"lines":[{"line_no":1,"sku":"SKU-1","expected_qty":12}],`+
			`"cartons":[{"sscc":"`+testSSCC+`","parent_sscc":"`+testPallet+`","kind":"HOMOGENEOUS_CASE",`+
			`"contents":[{"line_no":1,"sku":"SKU-1","gtin":"`+testEach+`","qty":12,"lot":"L2310","expiry":"2027-10-31"}],`+
			`"gross_weight_g":5000,"dims":{"length_mm":400,"width_mm":300,"height_mm":200}}]}`)
	assertRow(t, msgs[0], "asn.ASNRegistered", "ASN-1001", "ASN-1001", want)
}

func TestEncodeFlatAsnRegisteredIsUnchanged(t *testing.T) {
	msgs, err := (&Encoder{NewID: fixedID}).EncodeAsn(registeredAsn(t, at(9, 8, 0)))
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("asn", "ASNRegistered", "ASN-1001", "2026-10-08T12:00:00Z",
		`{"asn_number":"ASN-1001","supplier_ref":"ACME","expected_arrival":"2026-10-09T08:00:00Z",`+
			`"lines":[{"line_no":1,"sku":"SKU-1","expected_qty":40},{"line_no":2,"sku":"SKU-2","expected_qty":5}]}`)
	assertRow(t, msgs[0], "asn.ASNRegistered", "ASN-1001", "ASN-1001", want)
}

func TestEncodeReceiptLineReceivedWithPackFacts(t *testing.T) {
	h := receiptHeader(at(23, 14, 3))
	ev := receipt.ReceiptLineReceived{
		Header: h, LineNo: 1, SKU: "SKU-1", Quantity: 12, Condition: receipt.ConditionGood,
		OwnerRef: "tenant-7f3a", PackGTIN: mustGTIN(t, testCase), Cases: 1, SSCC: mustSSCC(t, testSSCC),
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("receipt", "ReceiptLineReceived", rcptID, "2026-10-23T14:03:00Z",
		`{"receipt_id":"`+rcptID+`","asn_number":"ASN-1001","line_no":1,"sku":"SKU-1","quantity":12,"condition":"Good",`+
			`"received_at":"2026-10-23T14:03:00Z","owner_ref":"tenant-7f3a","pack_gtin":"`+testCase+`","cases":1,"sscc":"`+testSSCC+`"}`)
	assertRow(t, msgs[0], "receipt.ReceiptLineReceived", rcptID, "ASN-1001", want)
}

func TestEncodeReceiptCartonReceived(t *testing.T) {
	ev := receipt.ReceiptCartonReceived{
		Header: receiptHeader(at(23, 14, 3)), OwnerRef: "tenant-7f3a", SSCC: mustSSCC(t, testSSCC), ParentSSCC: mustSSCC(t, testPallet),
		Sealed: true, LPN: testSSCC,
		Contents: []receipt.CartonContent{{
			LineNo: 1, SKU: "SKU-1", GTIN: mustGTIN(t, testEach), Qty: 12, Lot: "L2310", Expiry: "2027-10-31", Condition: receipt.ConditionGood,
		}},
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("receipt", "ReceiptCartonReceived", rcptID, "2026-10-23T14:03:00Z",
		`{"receipt_id":"`+rcptID+`","asn_number":"ASN-1001","owner_ref":"tenant-7f3a","sscc":"`+testSSCC+`","parent_sscc":"`+testPallet+`",`+
			`"sealed":true,"lpn":"`+testSSCC+`","contents":[{"line_no":1,"sku":"SKU-1","gtin":"`+testEach+`","qty":12,`+
			`"lot":"L2310","expiry":"2027-10-31","condition":"Good"}],"received_at":"2026-10-23T14:03:00Z"}`)
	assertRow(t, msgs[0], "receipt.ReceiptCartonReceived", rcptID, "ASN-1001", want)
}

func TestEncodeUnlabelledCartonHasNoSsccAndNoSite(t *testing.T) {
	ev := receipt.ReceiptCartonReceived{
		Header: receiptHeader(at(23, 14, 3)), Sealed: true, LPN: "LPN-01K8Z3Y2M6Q4W7E9R1T5V0XBNC",
		Contents: []receipt.CartonContent{{LineNo: 1, SKU: "SKU-1", GTIN: mustGTIN(t, testEach), Qty: 12, Condition: receipt.ConditionDamaged}},
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := envelope("receipt", "ReceiptCartonReceived", rcptID, "2026-10-23T14:03:00Z",
		`{"receipt_id":"`+rcptID+`","asn_number":"ASN-1001","sealed":true,"lpn":"LPN-01K8Z3Y2M6Q4W7E9R1T5V0XBNC",`+
			`"contents":[{"line_no":1,"sku":"SKU-1","gtin":"`+testEach+`","qty":12,"condition":"Damaged"}],"received_at":"2026-10-23T14:03:00Z"}`)
	assertRow(t, msgs[0], "receipt.ReceiptCartonReceived", rcptID, "ASN-1001", want)
}

func TestEncodeCartonDiscrepancyRaised(t *testing.T) {
	cases := map[string]struct {
		d    receipt.CartonDiscrepancy
		data string
	}{
		"quantity mismatch": {
			receipt.CartonDiscrepancy{Kind: receipt.KindCaseQtyMismatch, SSCC: mustSSCC(t, testSSCC), Expected: "12", Actual: "10"},
			`{"receipt_id":"` + rcptID + `","sscc":"` + testSSCC + `","kind":"CaseQtyMismatch","expected":"12","actual":"10","at":"2026-10-23T14:05:00Z"}`,
		},
		"missing carton": {
			receipt.CartonDiscrepancy{Kind: receipt.KindMissingCarton, Expected: testSSCC},
			`{"receipt_id":"` + rcptID + `","kind":"MissingCarton","expected":"` + testSSCC + `","at":"2026-10-23T14:05:00Z"}`,
		},
		"unknown gtin": {
			receipt.CartonDiscrepancy{Kind: receipt.KindUnknownGtin, Actual: testEach},
			`{"receipt_id":"` + rcptID + `","kind":"UnknownGtin","actual":"` + testEach + `","at":"2026-10-23T14:05:00Z"}`,
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

func TestAnalyticsStreamCarriesTheCartonEventsToo(t *testing.T) {
	ev := receipt.CartonDiscrepancyRaised{Header: receiptHeader(at(23, 14, 5)), CartonDiscrepancy: receipt.CartonDiscrepancy{Kind: receipt.KindMissingCarton, Expected: testSSCC}}
	msgs, err := (&FanoutEncoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("EncodeReceipt = %v, %v", msgs, err)
	}
	if msgs[0].Topic != Topic || msgs[1].Topic != AnalyticsTopic || msgs[0].EventType != msgs[1].EventType ||
		msgs[0].EventType != "com.warehouse.wms.inbound-receiving.receipt.CartonDiscrepancyRaised" || msgs[0].EventID != msgs[1].EventID {
		t.Fatalf("messages = %+v", msgs)
	}
}
