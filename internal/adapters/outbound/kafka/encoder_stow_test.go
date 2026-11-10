package kafka

import (
	"strings"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

func stowCompleted() receipt.ReceiptStowCompleted {
	return receipt.ReceiptStowCompleted{
		Header: receiptHeader(at(9, 14, 0)), AppointmentID: apptID,
		ArrivedAt: at(9, 8, 0), ClosedAt: at(9, 9, 30), StowedAt: at(9, 10, 0),
		DockToStockSeconds: 7200, CartonCount: 3,
	}
}

const stowData = `{"receipt_id":"` + rcptID + `","asn_number":"ASN-1001","appointment_id":"` + apptID + `",` +
	`"arrived_at":"2026-10-09T08:00:00Z","closed_at":"2026-10-09T09:30:00Z","stowed_at":"2026-10-09T10:00:00Z",` +
	`"dock_to_stock_seconds":7200,"carton_count":3}`

func TestEncoderGoldenReceiptStowCompleted(t *testing.T) {
	msgs, err := (&Encoder{NewID: fixedID}).EncodeReceipt(stowCompleted())
	if err != nil {
		t.Fatal(err)
	}
	// subject = receipt id, Kafka key = ASN number, like the other receipt events
	assertRow(t, msgs[0], "receipt.ReceiptStowCompleted", rcptID, "ASN-1001", envelope("receipt", "ReceiptStowCompleted", rcptID, "2026-10-09T14:00:00Z", stowData))
}

func TestAnalyticsAndFanoutCarryReceiptStowCompletedWithOneId(t *testing.T) {
	analytics, err := (&AnalyticsEncoder{NewID: fixedID}).EncodeReceipt(stowCompleted())
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(envelope("receipt", "ReceiptStowCompleted", rcptID, "2026-10-09T14:00:00Z", stowData), ":events:", ":analytics:", 1)
	if string(analytics[0].Value) != want || analytics[0].Topic != AnalyticsTopic || string(analytics[0].Key) != "ASN-1001" {
		t.Fatalf("analytics row = %+v\nvalue %s\nwant  %s", analytics[0], analytics[0].Value, want)
	}
	fan, err := (&FanoutEncoder{Integration: &Encoder{}, Analytics: &AnalyticsEncoder{}, NewID: fixedID}).EncodeReceipt(stowCompleted())
	if err != nil {
		t.Fatal(err)
	}
	if len(fan) != 2 || fan[0].Topic != Topic || fan[1].Topic != AnalyticsTopic || fan[0].EventID != fan[1].EventID ||
		fan[0].EventType != "com.warehouse.wms.inbound-receiving.receipt.ReceiptStowCompleted" || fan[0].EventType != fan[1].EventType {
		t.Fatalf("fanout = %+v", fan)
	}
}
