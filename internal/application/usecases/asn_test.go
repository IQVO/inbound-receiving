package usecases_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/application/usecases"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

func oneLine(number string) usecases.RegisterAsnCommand {
	return usecases.RegisterAsnCommand{AsnNumber: number, SupplierRef: "X", Lines: []usecases.AsnLineInput{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 1}}}
}

func TestRegisterAsnEnqueuesASNRegistered(t *testing.T) {
	w := newWorld()
	a := w.registerAsn("ASN-1", "SKU-1", "SKU-2")
	if a.Version() != 1 || a.State() != asn.Registered {
		t.Fatalf("asn = v%d %s", a.Version(), a.State())
	}
	if got := w.eventTypes(); !reflect.DeepEqual(got, []string{"asn.ASNRegistered"}) {
		t.Fatalf("events = %v", got)
	}
}

func TestRegisterAsnDuplicateNumber(t *testing.T) {
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), oneLine("ASN-1"))
	wantErr(t, err, usecases.ErrAsnAlreadyExists)
}

func TestRegisterAsnLostInsertRaceIsAlreadyExists(t *testing.T) {
	w := newWorld()
	w.asns.saveErr = repository.ErrConcurrentModification
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), oneLine("ASN-1"))
	wantErr(t, err, usecases.ErrAsnAlreadyExists)
}

func TestRegisterAsnDomainErrorsPassThrough(t *testing.T) {
	w := newWorld()
	uc := &usecases.RegisterAsn{Writer: w.writer}
	_, err := uc.Handle(context.Background(), usecases.RegisterAsnCommand{AsnNumber: "bad number", SupplierRef: "X"})
	wantErr(t, err, asn.ErrInvalidNumber)
	_, err = uc.Handle(context.Background(), usecases.RegisterAsnCommand{AsnNumber: "A", SupplierRef: "X"})
	wantErr(t, err, asn.ErrNoLines)
	if w.uow.calls != 0 {
		t.Fatal("a rejected command must not open a unit of work")
	}
}

func TestRegisterAsnKafkaModeRejectsUnknownSku(t *testing.T) {
	w := newWorld()
	w.skus.known["SKU-1"] = true
	uc := &usecases.RegisterAsn{Writer: w.writer, SkuMode: usecases.ModeKafka, Skus: w.skus}
	cmd := oneLine("A")
	cmd.Lines = append(cmd.Lines, usecases.AsnLineInput{LineNo: 2, SKU: "SKU-TYPO", ExpectedQty: 1})
	_, err := uc.Handle(context.Background(), cmd)
	wantErr(t, err, usecases.ErrUnknownSKU)
	if err.Error() != "sku SKU-TYPO is not known to product-master" {
		t.Fatalf("detail = %q", err.Error())
	}
	_, err = uc.Handle(context.Background(), oneLine("A"))
	wantNoErr(t, err)
}

func TestRegisterAsnKafkaModeSurfacesDirectoryFailure(t *testing.T) {
	w := newWorld()
	w.skus.err = errors.New("db down")
	uc := &usecases.RegisterAsn{Writer: w.writer, SkuMode: usecases.ModeKafka, Skus: w.skus}
	_, err := uc.Handle(context.Background(), oneLine("A"))
	wantErr(t, err, w.skus.err)
}

func TestRegisterAsnPermissiveModeNeverAsksTheDirectory(t *testing.T) {
	w := newWorld()
	w.skus.err = errors.New("must not be called")
	uc := &usecases.RegisterAsn{Writer: w.writer, SkuMode: usecases.ModePermissive, Skus: w.skus}
	_, err := uc.Handle(context.Background(), oneLine("A"))
	wantNoErr(t, err)
}

func TestRegisterAsnRepositoryFailuresSurface(t *testing.T) {
	w := newWorld()
	uc := &usecases.RegisterAsn{Writer: w.writer}
	w.asns.getErr = errors.New("get failed")
	_, err := uc.Handle(context.Background(), oneLine("A"))
	wantErr(t, err, w.asns.getErr)
	w.asns.getErr = nil
	w.asns.saveErr = errors.New("save failed")
	_, err = uc.Handle(context.Background(), oneLine("A"))
	wantErr(t, err, w.asns.saveErr)
}

func TestRegisterAsnOutboxFailureSurfaces(t *testing.T) {
	w := newWorld()
	w.outbox.err = errors.New("outbox down")
	_, err := (&usecases.RegisterAsn{Writer: w.writer}).Handle(context.Background(), oneLine("A"))
	wantErr(t, err, w.outbox.err)
}

func TestCancelAsn(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.registerAsn("ASN-1", "SKU-1")
	uc := &usecases.CancelAsn{Writer: w.writer}

	_, err := uc.Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1", ExpectedVersion: 7})
	wantErr(t, err, usecases.ErrVersionMismatch)
	_, err = uc.Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "nope!"})
	wantErr(t, err, asn.ErrInvalidNumber)
	_, err = uc.Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-9"})
	wantErr(t, err, repository.ErrAsnNotFound)

	a, err := uc.Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1", Reason: "supplier", ExpectedVersion: 1})
	wantNoErr(t, err)
	if a.State() != asn.Cancelled || a.Version() != 2 {
		t.Fatalf("asn = %s v%d", a.State(), a.Version())
	}
	if got := w.eventTypes(); !reflect.DeepEqual(got, []string{"asn.ASNRegistered", "asn.ASNCancelled"}) {
		t.Fatalf("events = %v", got)
	}
	_, err = uc.Handle(ctx, usecases.CancelAsnCommand{AsnNumber: "ASN-1"})
	wantErr(t, err, asn.ErrAsnTerminal)
}
