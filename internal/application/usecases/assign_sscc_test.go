package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// fakeSerials is a per-prefix counter: 1, 2, 3, ... and never the same twice.
type fakeSerials struct {
	next  map[string]int64
	calls int
	err   error
}

func newFakeSerials() *fakeSerials { return &fakeSerials{next: map[string]int64{}} }

func (f *fakeSerials) NextSerial(_ context.Context, p receipt.CompanyPrefix) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	f.next[p.String()]++
	return f.next[p.String()], nil
}

const testPrefix = "0614141"

func (w *scanWorld) assignUC(t *testing.T, prefix string) (*usecases.AssignSscc, *fakeSerials) {
	t.Helper()
	p, err := receipt.ParseCompanyPrefix(prefix)
	wantNoErr(t, err)
	serials := newFakeSerials()
	return &usecases.AssignSscc{Writer: w.writer, Packaging: w.pack, Prefix: p, Serials: serials}, serials
}

func (w *scanWorld) assign(uc *usecases.AssignSscc, gtin string) (usecases.AssignOutcome, error) {
	return uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: w.receipt, GTIN: gtin})
}

func TestAssignSsccReceivesTheCaseUnderAnSSCCOfTheReceiversPrefix(t *testing.T) {
	w := newScanWorld(t)
	uc, _ := w.assignUC(t, testPrefix)
	out, err := w.assign(uc, w.case1.String())
	wantNoErr(t, err)
	if out.SSCC.String() != "006141410000000012" || out.Carton.LPN() != "006141410000000012" {
		t.Fatalf("sscc = %s lpn = %s", out.SSCC, out.Carton.LPN())
	}
	if out.Carton.SSCCSource() != receipt.SsccReceiver || !out.Result.Accepted || out.Receipt.Lines()[0].ReceivedGood() != 20 {
		t.Fatalf("outcome = %+v", out)
	}
	want := []string{"receipt.ReceiptLineReceived", "receipt.ReceiptCartonReceived", "receipt.CartonDiscrepancyRaised"}
	if got := w.newTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("outbox = %v, want %v", got, want)
	}
	ev := out.Result.Events[1].(receipt.ReceiptCartonReceived)
	if ev.SSCC != out.SSCC || ev.SSCCSource != receipt.SsccReceiver || ev.LPN != out.SSCC.String() {
		t.Fatalf("carton event = %+v", ev)
	}
	if d := out.Result.Discrepancies; len(d) != 1 || d[0].Kind != receipt.KindUnlabelledCarton || d[0].Actual != out.SSCC.String() {
		t.Fatalf("findings = %+v", d)
	}
	stored, ok := w.receiptNow(t).Carton(out.SSCC)
	if !ok || stored.SSCCSource() != receipt.SsccReceiver {
		t.Fatalf("stored carton = %+v, %v", stored, ok)
	}
}

func TestAssignSsccGivesDistinctIncreasingSerials(t *testing.T) {
	w := newScanWorld(t)
	uc, _ := w.assignUC(t, testPrefix)
	seen := map[string]bool{}
	for i, want := range []string{"006141410000000012", "006141410000000029"} {
		out, err := w.assign(uc, w.case1.String())
		wantNoErr(t, err)
		if out.SSCC.String() != want || seen[want] {
			t.Fatalf("call %d: sscc = %s, want %s", i, out.SSCC, want)
		}
		seen[want] = true
	}
	if got := w.receiptNow(t).Lines()[0].ReceivedGood(); got != 40 {
		t.Fatalf("received = %d, want two cases of 20", got)
	}
}

func TestAssignSsccWithoutAPrefixIsRefusedAndNothingChanges(t *testing.T) {
	w := newScanWorld(t)
	uc, serials := w.assignUC(t, "")
	before, version := len(w.outbox.msgs), w.receiptNow(t).Version()
	_, err := w.assign(uc, w.case1.String())
	wantErr(t, err, receipt.ErrCompanyPrefixNotConfigured)
	if serials.calls != 0 || len(w.outbox.msgs) != before || w.receiptNow(t).Version() != version {
		t.Fatalf("a refused assignment consumed a serial (%d) or changed the receipt", serials.calls)
	}
}

func TestAssignSsccRefusals(t *testing.T) {
	w := newScanWorld(t)
	uc, serials := w.assignUC(t, testPrefix)
	unknown := mustGTIN(t, "629104150045")
	otherSku := mustGTIN(t, "2000123456792")
	w.pack.add(level(otherSku, "SKU-9", shared.PackCase, w.each1, 6))
	tests := []struct {
		name string
		gtin string
		want error
	}{
		{"not a GTIN", "123", gs1.ErrInvalidGTIN},
		{"empty", "", gs1.ErrInvalidGTIN},
		{"a GTIN the packaging view does not know", unknown.String(), usecases.ErrUnknownGtin},
		{"an each GTIN", w.each1.String(), usecases.ErrScanKindMismatch},
		{"a case of a SKU that is not on the ASN", otherSku.String(), receipt.ErrLineNotOnAsn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before, version := len(w.outbox.msgs), w.receiptNow(t).Version()
			_, err := w.assign(uc, tt.gtin)
			wantErr(t, err, tt.want)
			if len(w.outbox.msgs) != before || w.receiptNow(t).Version() != version {
				t.Fatal("a refused assignment changed the receipt")
			}
		})
	}
	_ = serials
}

func TestAssignSsccDoesNotConsumeASerialForAClosedOrUnknownReceipt(t *testing.T) {
	w := newScanWorld(t)
	uc, serials := w.assignUC(t, testPrefix)
	_, err := uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: "rcpt-00000000-0000-0000-0000-000000000000", GTIN: w.case1.String()})
	wantErr(t, err, repository.ErrReceiptNotFound)
	_, err = (&usecases.CloseReceipt{Writer: w.writer}).Handle(context.Background(), usecases.CloseReceiptCommand{ReceiptID: w.receipt})
	wantNoErr(t, err)
	_, err = w.assign(uc, w.case1.String())
	wantErr(t, err, receipt.ErrReceiptClosed)
	_, err = uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: "bad", GTIN: w.case1.String()})
	wantErr(t, err, receipt.ErrInvalidID)
	if serials.calls != 0 {
		t.Fatalf("%d serials consumed by refused commands", serials.calls)
	}
}

func TestAssignSsccHonoursIfMatchAndTheSerialLimit(t *testing.T) {
	w := newScanWorld(t)
	uc, serials := w.assignUC(t, testPrefix)
	v := w.receiptNow(t).Version()
	_, err := uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: w.receipt, GTIN: w.case1.String(), ExpectedVersion: v + 3})
	wantErr(t, err, usecases.ErrVersionMismatch)
	serials.err = repository.ErrSsccSerialsExhausted
	_, err = w.assign(uc, w.case1.String())
	wantErr(t, err, repository.ErrSsccSerialsExhausted)
	if errors.Is(err, receipt.ErrCompanyPrefixNotConfigured) {
		t.Fatal("exhaustion is not a missing prefix")
	}
	serials.err = nil
	if _, err := uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: w.receipt, GTIN: w.case1.String(), ExpectedVersion: v}); err != nil {
		t.Fatal(err)
	}
}

func TestAssignSsccCarriesConditionLotAndExpiry(t *testing.T) {
	w := newScanWorld(t)
	uc, _ := w.assignUC(t, testPrefix)
	out, err := uc.Handle(context.Background(), usecases.AssignSsccCommand{
		ReceiptID: w.receipt, GTIN: w.case1.String(), Condition: "Damaged", Lot: "L7", Expiry: "2027-02-00",
	})
	wantNoErr(t, err)
	c := out.Carton.Contents()[0]
	if c.Condition != receipt.ConditionDamaged || c.Lot != "L7" || c.Expiry != "2027-02-00" || w.receiptNow(t).Lines()[0].ReceivedDamaged() != 20 {
		t.Fatalf("contents = %+v", c)
	}
	_, err = uc.Handle(context.Background(), usecases.AssignSsccCommand{ReceiptID: w.receipt, GTIN: w.case1.String(), Condition: "Broken"})
	wantErr(t, err, receipt.ErrInvalidCondition)
}
