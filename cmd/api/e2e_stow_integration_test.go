//go:build integration

package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

const (
	topicYard      = "warehouse.yard-management.events"
	topicInventory = "warehouse.inventory.events"

	ssccD = "003456789012345694"
)

// Phase 3c: the dock-to-stock KPI through the real binary (ADR 0021). A carton
// ASN is received on an appointment through REST and closed; THEN yard-
// management's VisitCheckedIn (the gate arrival) and inventory-storage's
// ContainerStowed (the stow of the carton's LPN, 2 h later) arrive over Kafka,
// each delivered twice. Exactly one ReceiptStowCompleted reaches both output
// topics, carrying 7200 seconds, and the copies hold one row each.
func phaseDockToStock(t *testing.T, s *service, w *world) {
	t.Helper()
	const (
		asnNumber = "ASN-D"
		arrived   = "2026-11-09T05:00:00Z"
		stowed    = "2026-11-09T07:00:00Z"
	)
	apptID := "appt-00000000-0000-4000-8000-0000000000e3"
	start := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Hour)
	produce(t, w.brokers, topicDockSched, kafkago.Message{Key: []byte(apptID), Value: inputEvent(t, "appt-evt-d", "/warehouse/dock-scheduling",
		"com.warehouse.wms.dock-scheduling.dockappointment.DockAppointmentBooked", apptID, map[string]any{
			"appointment_id": apptID, "site_id": "WH1", "direction": "INBOUND", "door_code": doorCode, "carrier": "ACME Freight",
			"window_start": start.Format(time.RFC3339), "window_end": start.Add(2 * time.Hour).Format(time.RFC3339),
			"asn_numbers": []string{asnNumber}, "provenance": "native", "version": 1})})
	waitFor(t, 60*time.Second, "the second appointment to be copied", func() bool {
		return w.db.count(`SELECT count(*) FROM appointments WHERE id = $1`, apptID) == 1
	})

	asnD := fmt.Sprintf(`{"asnNumber":%q,"supplierRef":"ACME","lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":20}],"cartons":[`+
		`{"sscc":%q,"kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":%q,"qty":20}]}]}`, asnNumber, ssccD, eachGTIN)
	s.call(http.MethodPost, "/asns", asnD, "reg-d", http.StatusCreated)
	var opened struct {
		ReceiptID string `json:"receiptId"`
	}
	mustDecode(t, s.call(http.MethodPost, "/receipts", fmt.Sprintf(`{"asnNumber":%q,"appointmentId":%q}`, asnNumber, apptID), "open-d", http.StatusCreated), &opened)
	s.call(http.MethodPost, "/receipts/"+opened.ReceiptID+"/scans", `{"kind":"SSCC","id":"`+ssccD+`"}`, "scan-d", http.StatusCreated)
	s.call(http.MethodPost, "/receipts/"+opened.ReceiptID+"/closures", "", "close-d", http.StatusOK)
	if n := w.db.count(`SELECT count(*) FROM outbox_events WHERE event_type LIKE '%ReceiptStowCompleted'`); n != 0 {
		t.Fatalf("ReceiptStowCompleted rows before any arrival or stow = %d, want 0", n)
	}

	visit := inputEvent(t, "visit-evt-d", "/warehouse/yard-management", "com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn",
		"visit-d", map[string]any{"visit_id": "visit-d", "site_id": "WH1", "carrier": "ACME Freight", "carrier_scac": "ACME", "purpose": "LIVE_UNLOAD",
			"appointment_id": apptID, "trailers": []map[string]any{}, "arrived_at": arrived, "version": 1})
	produce(t, w.brokers, topicYard, kafkago.Message{Key: []byte("visit-d"), Value: visit}, kafkago.Message{Key: []byte("visit-d"), Value: visit})
	// A walk-in visit and an unrelated yard type are committed past.
	produce(t, w.brokers, topicYard,
		kafkago.Message{Key: []byte("visit-w"), Value: inputEvent(t, "visit-evt-w", "/warehouse/yard-management", "com.warehouse.wms.yard-management.yardvisit.VisitCheckedIn",
			"visit-w", map[string]any{"visit_id": "visit-w", "site_id": "WH1", "purpose": "BOBTAIL", "appointment_id": nil, "arrived_at": arrived, "version": 1})},
		kafkago.Message{Key: []byte("visit-d"), Value: inputEvent(t, "visit-evt-r", "/warehouse/yard-management", "com.warehouse.wms.yard-management.yardvisit.VisitRejected",
			"visit-d", map[string]any{"visit_id": "visit-d"})})
	stow := inputEvent(t, "stow-evt-d", "/warehouse/inventory-storage", "com.warehouse.wms.inventory-storage.container.ContainerStowed",
		ssccD, map[string]any{"lpn": ssccD, "sku": "SKU-1", "qty_eaches": 20, "site_id": "WH1", "bin": "WH1-STOR-A01", "at": stowed})
	produce(t, w.brokers, topicInventory, kafkago.Message{Key: []byte(ssccD), Value: stow}, kafkago.Message{Key: []byte(ssccD), Value: stow})

	waitFor(t, 60*time.Second, "the dock-to-stock fact to complete", func() bool {
		return w.db.count(`SELECT count(*) FROM receipt_stow_completions WHERE receipt_id = $1`, opened.ReceiptID) == 1
	})
	if n := w.db.count(`SELECT count(*) FROM visit_arrivals WHERE appointment_id = $1 AND arrived_at = $2`, apptID, mustTime(t, arrived)); n != 1 {
		t.Fatalf("visit_arrivals rows = %d, want 1 (the duplicate delivery was deduped, the walk-in kept out)", n)
	}
	if n := w.db.count(`SELECT count(*) FROM visit_arrivals`); n != 1 {
		t.Fatalf("visit_arrivals holds %d rows, want only the visit with an appointment", n)
	}
	if n := w.db.count(`SELECT count(*) FROM carton_stows WHERE lpn = $1 AND stowed_at = $2`, ssccD, mustTime(t, stowed)); n != 1 {
		t.Fatalf("carton_stows rows = %d, want 1", n)
	}
	for _, c := range []string{"visit-arrival-copy", "carton-stow-copy"} {
		if n := w.db.count(`SELECT count(*) FROM processed_events WHERE consumer = $1`, c); n != map[string]int{"visit-arrival-copy": 1, "carton-stow-copy": 1}[c] {
			t.Fatalf("%s claims = %d (the walk-in visit claims nothing, the rejection is another type)", c, n)
		}
	}
	if n := w.db.count(`SELECT count(*) FROM outbox_events WHERE event_type LIKE '%ReceiptStowCompleted'`); n != 2 {
		t.Fatalf("ReceiptStowCompleted outbox rows = %d, want 2 (one occurrence on both topics)", n)
	}
	w.stowReceipt = opened.ReceiptID
	w.expected = append(w.expected,
		expectedEvent{"asn", "ASNRegistered", asnNumber, asnNumber},
		expectedEvent{"receipt", "ReceiptOpened", opened.ReceiptID, asnNumber},
		expectedEvent{"receipt", "ReceiptLineReceived", opened.ReceiptID, asnNumber},
		expectedEvent{"receipt", "ReceiptCartonReceived", opened.ReceiptID, asnNumber},
		expectedEvent{"receipt", "ReceiptClosed", opened.ReceiptID, asnNumber},
		expectedEvent{"receipt", "ReceiptStowCompleted", opened.ReceiptID, asnNumber},
	)
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

// assertStowPayload pins the ReceiptStowCompleted that reached the integration
// topic: the dock-to-stock of the journey is 7200 seconds.
func assertStowPayload(t *testing.T, raw string, w *world) {
	t.Helper()
	var got struct {
		ReceiptID          string `json:"receipt_id"`
		AsnNumber          string `json:"asn_number"`
		AppointmentID      string `json:"appointment_id"`
		ArrivedAt          string `json:"arrived_at"`
		StowedAt           string `json:"stowed_at"`
		DockToStockSeconds int64  `json:"dock_to_stock_seconds"`
		CartonCount        int    `json:"carton_count"`
	}
	mustDecode(t, raw, &got)
	if got.ReceiptID != w.stowReceipt || got.AsnNumber != "ASN-D" || !strings.HasPrefix(got.AppointmentID, "appt-") ||
		got.ArrivedAt != "2026-11-09T05:00:00Z" || got.StowedAt != "2026-11-09T07:00:00Z" || got.DockToStockSeconds != 7200 || got.CartonCount != 1 {
		t.Fatalf("ReceiptStowCompleted data = %+v", got)
	}
}
