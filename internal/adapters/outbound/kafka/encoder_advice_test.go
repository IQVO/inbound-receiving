package kafka

import (
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func adviceIssued(t *testing.T) receipt.ReceivingAdviceIssued {
	t.Helper()
	sscc := mustSSCC(t, testSSCC)
	return receipt.ReceivingAdviceIssued{
		Header:            receiptHeader(at(9, 9, 30)),
		DespatchAdviceRef: "ASN-1001", SupplierRef: "ACME", OwnerRef: "tenant-7f3a",
		LastCartonAt: at(9, 9, 10), SLA: 24 * time.Hour, WithinSLA: true,
		Lines: []receipt.AdviceLine{
			{
				LineNo: 1, SKU: "SKU-1", GTIN: mustGTIN(t, testEach), Expected: 40, Received: 34, Accepted: 25, Rejected: 4, Held: 5,
				SSCCCount: 2, SSCCs: []gs1.SSCC{sscc}, SSCCsTruncated: true,
				Discrepancies: []receipt.AdviceDiscrepancy{
					{Kind: receipt.KindShort, Quantity: 6, Coding: receipt.Coding{W13: "02", CBV: "mismatch_quantity"}},
					{Kind: receipt.KindHold, Quantity: 5, Coding: receipt.Coding{W13: "09", CBV: "non_conformant"}},
				},
			},
			{LineNo: 2, SKU: "SKU-2", Expected: 5, Received: 5, Accepted: 5, Discrepancies: []receipt.AdviceDiscrepancy{}},
		},
		LinesTotal: 3, LinesTruncated: true,
		CartonFindings: []receipt.CartonFinding{
			{CartonDiscrepancy: receipt.CartonDiscrepancy{Kind: receipt.KindDuplicateSscc, SSCC: sscc}, At: at(9, 9, 5)},
			{CartonDiscrepancy: receipt.CartonDiscrepancy{Kind: receipt.KindMissingCarton, Expected: testPallet}, At: at(9, 9, 30)},
		},
		CartonFindingsTotal: 5,
	}
}

const adviceData = `{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","despatch_advice_ref":"ASN-1001","supplier_ref":"ACME","owner_ref":"tenant-7f3a",` +
	`"issued_at":"2026-10-09T09:30:00Z","last_carton_at":"2026-10-09T09:10:00Z","sla_seconds":86400,"within_sla":true,` +
	`"lines":[` +
	`{"line_no":1,"sku":"SKU-1","gtin":"` + testEach + `","expected_qty":40,"received_qty":34,"accepted_qty":25,"rejected_qty":4,"held_qty":5,` +
	`"sscc_count":2,"ssccs":["` + testSSCC + `"],"ssccs_truncated":true,` +
	`"discrepancies":[{"kind":"Short","quantity":6,"w13_code":"02","cbv_disposition":"mismatch_quantity"},` +
	`{"kind":"Hold","quantity":5,"w13_code":"09","cbv_disposition":"non_conformant"}]},` +
	`{"line_no":2,"sku":"SKU-2","expected_qty":5,"received_qty":5,"accepted_qty":5,"rejected_qty":0,"held_qty":0,"sscc_count":0,"discrepancies":[]}],` +
	`"lines_total":3,"lines_truncated":true,` +
	`"carton_findings":[` +
	`{"kind":"DuplicateSscc","sscc":"` + testSSCC + `","at":"2026-10-09T09:05:00Z","cbv_disposition":"mismatch_instance"},` +
	`{"kind":"MissingCarton","expected":"` + testPallet + `","at":"2026-10-09T09:30:00Z","w13_code":"02","cbv_disposition":"mismatch_quantity"}],` +
	`"carton_findings_total":5}`

func TestEncoderGoldenReceivingAdviceIssued(t *testing.T) {
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(adviceIssued(t))
	if err != nil {
		t.Fatal(err)
	}
	// subject = receipt id, Kafka key = ASN number, like the other receipt events
	assertRow(t, msgs[0], "receipt.ReceivingAdviceIssued", rcptID, "ASN-1001", envelope("receipt", "ReceivingAdviceIssued", rcptID, "2026-10-09T09:30:00Z", adviceData))
}

func TestEncoderGoldenReceivingAdviceIssuedMinimal(t *testing.T) {
	ev := receipt.ReceivingAdviceIssued{
		Header: receiptHeader(at(9, 9, 30)), DespatchAdviceRef: "ASN-1001", SupplierRef: "ACME",
		LastCartonAt: at(7, 9, 10), SLA: 24 * time.Hour, WithinSLA: false,
		Lines: []receipt.AdviceLine{}, CartonFindings: []receipt.CartonFinding{},
	}
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(ev)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","despatch_advice_ref":"ASN-1001","supplier_ref":"ACME",` +
		`"issued_at":"2026-10-09T09:30:00Z","last_carton_at":"2026-10-07T09:10:00Z","sla_seconds":86400,"within_sla":false,` +
		`"lines":[],"lines_total":0,"carton_findings":[],"carton_findings_total":0}`
	assertRow(t, msgs[0], "receipt.ReceivingAdviceIssued", rcptID, "ASN-1001", envelope("receipt", "ReceivingAdviceIssued", rcptID, "2026-10-09T09:30:00Z", want))
}

func adviceOverdue() receipt.ReceivingAdviceOverdue {
	return receipt.ReceivingAdviceOverdue{
		Header:       receiptHeader(at(10, 10, 0)),
		LastCartonAt: at(9, 9, 10), DueAt: at(10, 9, 10), SLA: 24 * time.Hour,
	}
}

const overdueData = `{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","last_carton_at":"2026-10-09T09:10:00Z",` +
	`"due_at":"2026-10-10T09:10:00Z","sla_seconds":86400,"overdue_at":"2026-10-10T10:00:00Z"}`

func TestEncoderGoldenReceivingAdviceOverdue(t *testing.T) {
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(adviceOverdue())
	if err != nil {
		t.Fatal(err)
	}
	assertRow(t, msgs[0], "receipt.ReceivingAdviceOverdue", rcptID, "ASN-1001", envelope("receipt", "ReceivingAdviceOverdue", rcptID, "2026-10-10T10:00:00Z", overdueData))
}

func TestAdviceEventsFanOutToBothStreamsWithOneId(t *testing.T) {
	for name, ev := range map[string]receipt.Event{
		"ReceivingAdviceIssued":  adviceIssued(t),
		"ReceivingAdviceOverdue": adviceOverdue(),
	} {
		fan, err := (&FanoutEncoder{Integration: &Encoder{}, Analytics: &AnalyticsEncoder{}, NewID: fixedID}).EncodeReceipt(ev)
		if err != nil {
			t.Fatal(err)
		}
		wantType := "com.warehouse.wms.inbound-receiving.receipt." + name
		if len(fan) != 2 || fan[0].Topic != Topic || fan[1].Topic != AnalyticsTopic || fan[0].EventID != fan[1].EventID ||
			fan[0].EventType != wantType || fan[1].EventType != wantType || string(fan[0].Key) != "ASN-1001" {
			t.Fatalf("%s fanout = %+v", name, fan)
		}
		if !strings.Contains(string(fan[1].Value), ":analytics:"+name+":v1") {
			t.Fatalf("%s analytics dataschema: %s", name, fan[1].Value)
		}
	}
}
