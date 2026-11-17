package main_test

// The receiving tolerance policy scenarios of ADR 0026: the three bands at
// the close and at the scan, the reason codes, the exact-match (owner,
// supplier) key and the admin route. Everything runs over the REAL router
// with the in-memory adapters, like every feature here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/cucumber/godog"
)

// toleranceEachGTIN is the each GTIN the background's packaging view knows.
const toleranceEachGTIN = "10001234567892"

func (w *world) registerTolerance(sc *godog.ScenarioContext) {
	sc.Step(`^the ASN "([^"]*)" of supplier "([^"]*)" is registered with (\d+) units of "([^"]*)"$`, w.asnOfSupplierRegistered)
	sc.Step(`^the ASN "([^"]*)" of owner "([^"]*)" and supplier "([^"]*)" is registered with (\d+) units of "([^"]*)"$`, w.ownerAsnRegistered)
	sc.Step(`^a walk-in receipt is open for the owner's ASN "([^"]*)"$`, w.ownerWalkInReceiptIsOpen)
	sc.Step(`^the house declares a tolerance policy for supplier "([^"]*)" of (\d+)% over, (\d+)% over hard stop, (\d+)% under, (\d+)% under hard stop$`, w.houseDeclaresPolicy)
	sc.Step(`^the owner "([^"]*)" declares a tolerance policy for supplier "([^"]*)" of (\d+)% over, (\d+)% over hard stop, (\d+)% under, (\d+)% under hard stop$`, w.ownerDeclaresPolicy)
	sc.Step(`^no tolerance policy is declared for supplier "([^"]*)"$`, w.noPolicyDeclared)
	sc.Step(`^(\d+) units are received good on line (\d+)$`, w.unitsReceivedGood)
	sc.Step(`^the close is attempted$`, w.closeIsAttempted)
	sc.Step(`^the receipt is closed with override reason "([^"]*)"$`, w.receiptIsClosedWithReason)
	sc.Step(`^the receipt of the ASN "([^"]*)" is closed$`, w.asnReceiptIsClosed)
	sc.Step(`^the receipt of the ASN "([^"]*)" is closed with override reason "([^"]*)"$`, w.asnReceiptIsClosedWithReason)
	sc.Step(`^the (?:close|scan|policy) answers (\d+)$`, w.answerStatusIs)
	sc.Step(`^the (?:close|scan|policy) answers (\d+) ([a-z-]+)$`, w.answerIs)
	sc.Step(`^the closed receipt reports no discrepancy findings$`, w.noDiscrepancyFindings)
	sc.Step(`^the closed receipt reports the discrepancy finding "([^"]*)" with reason "([^"]*)"$`, w.discrepancyFindingWithReason)
	sc.Step(`^the closed receipt reports the discrepancy finding "([^"]*)" with no reason$`, w.discrepancyFindingWithoutReason)
	sc.Step(`^(\d+) units are scanned good by each GTIN with override reason "([^"]*)"$`, w.unitsScannedWithReason)
	sc.Step(`^(\d+) units are scanned good by each GTIN$`, w.unitsScanned)
	sc.Step(`^the receipt has received (\d+) units of line (\d+)$`, w.receiptHasReceived)
	sc.Step(`^the receipt is still open$`, w.receiptIsStillOpen)
	sc.Step(`^the policy of supplier "([^"]*)" is fetched$`, w.policyIsFetched)
	sc.Step(`^the policy answers (\d+) with over tolerance ([0-9.]+) and over hard stop ([0-9.]+)$`, w.thePolicyAnswers)
}

// declarePolicy POSTs the admin declaration of ADR 0026.
func (w *world) declarePolicy(ownerRef, supplier string, over, overStop, under, underStop int) error {
	body := fmt.Sprintf(`{"ownerRef":%q,"supplierRef":%q,"overTolerance":%f,"overHardStop":%f,"underTolerance":%f,"underHardStop":%f}`,
		ownerRef, supplier, float64(over)/100, float64(overStop)/100, float64(under)/100, float64(underStop)/100)
	return w.mustCall(http.MethodPost, "/receiving-policies", body)
}

func (w *world) houseDeclaresPolicy(supplier string, over, overStop, under, underStop int) error {
	return w.declarePolicy("", supplier, over, overStop, under, underStop)
}

func (w *world) ownerDeclaresPolicy(ownerRef, supplier string, over, overStop, under, underStop int) error {
	return w.declarePolicy(ownerRef, supplier, over, overStop, under, underStop)
}

// noPolicyDeclared removes the background's declaration: the exact key has no
// row, which is "no policy", today's behaviour.
func (w *world) noPolicyDeclared(supplier string) error {
	return w.policies.Delete(context.Background(), "", supplier)
}

func (w *world) asnOfSupplierRegistered(number, supplier string, qty int, sku string) error {
	body := fmt.Sprintf(`{"asnNumber":%q,"supplierRef":%q,"lines":[{"lineNo":1,"sku":%q,"expectedQty":%d}]}`, number, supplier, sku, qty)
	return w.mustCall(http.MethodPost, "/asns", body)
}

func (w *world) ownerAsnRegistered(number, ownerRef, supplier string, qty int, sku string) error {
	body := fmt.Sprintf(`{"asnNumber":%q,"supplierRef":%q,"ownerRef":%q,"lines":[{"lineNo":1,"sku":%q,"expectedQty":%d}]}`, number, supplier, ownerRef, sku, qty)
	return w.mustCall(http.MethodPost, "/asns", body)
}

// ownerWalkInReceiptIsOpen opens the owner-keyed receipt and makes it the
// scenario's receipt: every Then reads w.receipt.
func (w *world) ownerWalkInReceiptIsOpen(number string) error {
	if err := w.mustCall(http.MethodPost, "/receipts", `{"asnNumber":"`+number+`"}`); err != nil {
		return err
	}
	if err := w.rememberID("receiptId", &w.receipt); err != nil {
		return err
	}
	w.ownReceipt = w.receipt
	return nil
}

func (w *world) unitsReceivedGood(qty, line int) error {
	body := fmt.Sprintf(`{"lineNo":%d,"quantity":%d,"condition":"Good"}`, line, qty)
	return w.mustCall(http.MethodPost, "/receipts/{receipt}/lines", body)
}

// closeIsAttempted closes and lets the scenario assert the refusal.
func (w *world) closeIsAttempted() error {
	return w.call(http.MethodPost, "/receipts/{receipt}/closures", w.freshKey(), "", nil)
}

func (w *world) receiptIsClosedWithReason(reason string) error {
	// The status is the scenario's assertion: an unknown reason answers 400.
	return w.call(http.MethodPost, "/receipts/{receipt}/closures", w.freshKey(), `{"overrideReason":"`+reason+`"}`, nil)
}

func (w *world) asnReceiptIsClosed(string) error {
	return w.mustCall(http.MethodPost, "/receipts/"+w.ownReceipt+"/closures", "")
}

func (w *world) asnReceiptIsClosedWithReason(_ string, reason string) error {
	return w.mustCall(http.MethodPost, "/receipts/"+w.ownReceipt+"/closures", `{"overrideReason":"`+reason+`"}`)
}

// answerStatusIs checks only the last answer's status.
func (w *world) answerStatusIs(status int) error {
	if w.status != status {
		return fmt.Errorf("status = %d %s, want %d", w.status, w.body, status)
	}
	return nil
}

// answerIs checks the last answer's status and, for a problem, its slug.
func (w *world) answerIs(status int, slug string) error {
	if w.status != status {
		return fmt.Errorf("status = %d %s, want %d", w.status, w.body, status)
	}
	if slug == "ok" {
		return nil
	}
	return w.theProblemTypeIs(slug)
}

// receiptView is the GET /receipts/{id} answer the tolerance steps read.
type receiptView struct {
	State         string `json:"state"`
	Discrepancies []struct {
		Kind   string `json:"kind"`
		Reason string `json:"reason"`
	} `json:"discrepancies"`
	Lines []struct {
		ReceivedGood    int64 `json:"receivedGood"`
		ReceivedDamaged int64 `json:"receivedDamaged"`
	} `json:"lines"`
}

func (w *world) receiptView() (receiptView, error) {
	var out receiptView
	if err := w.mustCall(http.MethodGet, "/receipts/{receipt}", ""); err != nil {
		return out, err
	}
	return out, json.Unmarshal(w.body, &out)
}

// closedDiscrepancies reads the ReceiptClosed event of the last close: the
// authoritative tolerance record (ADR 0026) -- the REST receipt read keeps
// its raw live discrepancies, so the finding kinds are asserted where they
// are raised.
func (w *world) closedDiscrepancies() ([]closedDiscrepancy, error) {
	var last []byte
	for _, m := range w.outbox.Messages() {
		if strings.HasSuffix(m.EventType, ".ReceiptClosed") {
			last = m.Value
		}
	}
	if last == nil {
		return nil, fmt.Errorf("no ReceiptClosed in the outbox")
	}
	var event struct {
		Data struct {
			Discrepancies []closedDiscrepancy `json:"discrepancies"`
			Tolerance     *struct {
				OverTolerance float64 `json:"over_tolerance"`
			} `json:"tolerance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(last, &event); err != nil {
		return nil, err
	}
	w.lastTolerance = event.Data.Tolerance
	return event.Data.Discrepancies, nil
}

type closedDiscrepancy struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason_code"`
	W13Code string `json:"w13_code"`
}

func (w *world) noDiscrepancyFindings() error {
	ds, err := w.closedDiscrepancies()
	if err != nil {
		return err
	}
	if len(ds) != 0 {
		return fmt.Errorf("ReceiptClosed discrepancies = %+v, want none (the variance was inside the band)", ds)
	}
	return nil
}

func (w *world) discrepancyFindingWithReason(kind, reason string) error {
	return w.discrepancyFinding(kind, &reason)
}

func (w *world) discrepancyFindingWithoutReason(kind string) error {
	return w.discrepancyFinding(kind, nil)
}

func (w *world) discrepancyFinding(kind string, reason *string) error {
	ds, err := w.closedDiscrepancies()
	if err != nil {
		return err
	}
	if len(ds) != 1 || ds[0].Kind != kind {
		return fmt.Errorf("ReceiptClosed discrepancies = %+v, want one %s", ds, kind)
	}
	got := ds[0].Reason
	if reason == nil && got != "" {
		return fmt.Errorf("reason_code = %q, want none", got)
	}
	if reason != nil && got != *reason {
		return fmt.Errorf("reason_code = %q, want %q", got, *reason)
	}
	return nil
}

func (w *world) unitsScanned(qty int) error {
	return w.scanEaches(qty, "")
}

func (w *world) unitsScannedWithReason(qty int, reason string) error {
	return w.scanEaches(qty, reason)
}

func (w *world) scanEaches(qty int, reason string) error {
	body := fmt.Sprintf(`{"kind":"EACH_GTIN","id":%q,"qty":%d,"condition":"Good"`, toleranceEachGTIN, qty)
	if reason != "" {
		body += `,"overrideReason":"` + reason + `"`
	}
	body += `}`
	// The status is the scenario's assertion: an accepted scan answers 201,
	// a tolerance refusal answers 409.
	return w.call(http.MethodPost, "/receipts/{receipt}/scans", w.freshKey(), body, nil)
}

func (w *world) receiptHasReceived(qty int64, line int) error {
	view, err := w.receiptView()
	if err != nil {
		return err
	}
	if len(view.Lines) < line {
		return fmt.Errorf("the receipt has %d lines", len(view.Lines))
	}
	got := view.Lines[line-1].ReceivedGood + view.Lines[line-1].ReceivedDamaged
	if got != qty {
		return fmt.Errorf("line %d received %d, want %d", line, got, qty)
	}
	return nil
}

func (w *world) receiptIsStillOpen() error {
	view, err := w.receiptView()
	if err != nil {
		return err
	}
	if view.State != "Open" {
		return fmt.Errorf("state = %s, want Open (a refused close leaves the receipt open)", view.State)
	}
	return nil
}

func (w *world) policyIsFetched(supplier string) error {
	return w.call(http.MethodGet, "/receiving-policies/"+supplier, "", "", nil)
}

func (w *world) thePolicyAnswers(status int, over, overStop float64) error {
	if w.status != status {
		return fmt.Errorf("status = %d %s, want %d", w.status, w.body, status)
	}
	var policy struct {
		OverTolerance float64 `json:"overTolerance"`
		OverHardStop  float64 `json:"overHardStop"`
	}
	if err := json.Unmarshal(w.body, &policy); err != nil {
		return err
	}
	if policy.OverTolerance != over || policy.OverHardStop != overStop {
		return fmt.Errorf("band = %v/%v, want %v/%v", policy.OverTolerance, policy.OverHardStop, over, overStop)
	}
	return nil
}
