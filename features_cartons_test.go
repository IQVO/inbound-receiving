package main_test

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/application/usecases"
)

// The carton scenarios announce SKU-1 as 20 eaches per case: ASN-C holds two
// homogeneous cases (two SSCCs, one line of 40).
const (
	cartonAsnBody = `{"asnNumber":"%s","supplierRef":"ACME","ownerRef":"tenant-7f3a","expectedArrival":"2026-10-10T08:00:00Z",` +
		`"lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40}],"cartons":[` +
		`{"sscc":"003456789012345670","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]},` +
		`{"sscc":"003456789012345687","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}`
)

func (w *world) packagingViewKnows(sku, each, caseGTIN string, perCase int) error {
	from := bddStart.Add(-24 * time.Hour)
	declare := &usecases.ApplyPackLevelDeclared{UoW: w.uow, ProcessedEvents: w.processed, Packaging: w.packaging}
	levels := []usecases.PackLevelDeclared{
		{SKU: sku, Level: "EACH", GTIN: each, UnitsPerPack: 1, EffectiveFrom: from, Version: 1},
		{SKU: sku, Level: "CASE", GTIN: caseGTIN, ChildGTIN: each, QtyOfChild: int64(perCase), UnitsPerPack: int64(perCase), EffectiveFrom: from, Version: 1},
	}
	for _, l := range levels {
		w.events++
		if _, err := declare.Handle(context.Background(), "bdd-pack-"+strconv.Itoa(w.events), l); err != nil {
			return err
		}
	}
	return nil
}

func (w *world) cartonAsnIsRegistered(number string) error {
	return w.mustCall(http.MethodPost, "/asns", fmt.Sprintf(cartonAsnBody, number))
}

func (w *world) iScannedTheCarton(sscc string) error {
	return w.mustCall(http.MethodPost, "/receipts/{receipt}/scans", `{"kind":"SSCC","id":"`+sscc+`"}`)
}

func (w *world) registerCartons(sc *godog.ScenarioContext) {
	sc.Step(`^the packaging view knows SKU "([^"]*)" with each GTIN "([^"]*)" and case GTIN "([^"]*)" of (\d+)$`, w.packagingViewKnows)
	sc.Step(`^the carton ASN "([^"]*)" is registered$`, w.cartonAsnIsRegistered)
	sc.Step(`^I scanned the carton "([^"]*)"$`, w.iScannedTheCarton)
}
