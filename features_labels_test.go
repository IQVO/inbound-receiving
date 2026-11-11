package main_test

import (
	"fmt"
	"net/http"

	"github.com/cucumber/godog"

	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// palletAsnBody announces two homogeneous cases of 20 SKU-1 on ONE pallet.
const palletAsnBody = `{"asnNumber":"%s","supplierRef":"ACME","expectedArrival":"2026-10-10T08:00:00Z",` +
	`"lines":[{"lineNo":1,"sku":"SKU-1","expectedQty":40}],"cartons":[` +
	`{"sscc":"003456789012345670","parentSscc":"003456789099999995","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]},` +
	`{"sscc":"003456789012345687","parentSscc":"003456789099999995","kind":"HOMOGENEOUS_CASE","contents":[{"lineNo":1,"sku":"SKU-1","gtin":"10001234567892","qty":20}]}]}`

func (w *world) palletAsnIsRegistered(number string) error {
	return w.mustCall(http.MethodPost, "/asns", fmt.Sprintf(palletAsnBody, number))
}

func (w *world) theSiteGLNsAre(list string) error {
	sites, err := receipt.ParseSiteGLNs(list)
	if err != nil {
		return err
	}
	w.siteGLNs = sites
	w.stop() // the server is rebuilt on the next request, with the rule; the repositories live on
	return nil
}

func (w *world) theCompanyPrefixIs(prefix string) error {
	p, err := receipt.ParseCompanyPrefix(prefix)
	if err != nil {
		return err
	}
	w.prefix = p
	w.stop()
	return nil
}

func (w *world) registerLabels(sc *godog.ScenarioContext) {
	sc.Step(`^the pallet carton ASN "([^"]*)" is registered$`, w.palletAsnIsRegistered)
	sc.Step(`^the receiving site GLNs are "([^"]*)"$`, w.theSiteGLNsAre)
	sc.Step(`^the receiver GS1 company prefix is "([^"]*)"$`, w.theCompanyPrefixIs)
}
