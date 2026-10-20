package main

import (
	"testing"

	"github.com/claudioed/inbound-receiving/internal/adapters/outbound/memory"
)

// testDoubleAdapters wires the in-memory TEST DOUBLES into the adapters set so
// the composition functions (buildServer, startConsumers) can be unit-tested
// without containers. The binary itself never does this (the sensor
// TestRuntimeBinaryHasNoInMemoryAdapters forbids the import outside tests);
// the real wiring is proven by TestRealEntryPoint in main_integration_test.go.
func testDoubleAdapters(t *testing.T) adapters {
	t.Helper()
	asns, appts, receipts := memory.NewAsnRepo(), memory.NewAppointmentRepo(), memory.NewReceiptRepo()
	skus, doors := memory.NewKnownSkus(), memory.NewDockDoors()
	ob, processed := memory.NewOutboxRepo(), memory.NewProcessedEventRepo()
	return adapters{
		asns: asns, appointments: appts, receipts: receipts, outbox: ob, outboxStore: ob, processed: processed,
		skus: skus, doors: doors, idempotency: memory.NewIdempotencyStore(),
		uow: memory.NewUnitOfWork(asns, appts, receipts, skus, doors, ob, processed),
	}
}
