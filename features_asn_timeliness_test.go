package main_test

// ASN-timeliness and flat-ASN steps (ADR 0025). The arrival is fed through
// the same visit-arrival consumer use case the Kafka adapter calls (see
// features_stow_test.go); the effects are asserted on the outbox, where
// AsnLateForArrival lands, and on the REST scan answers.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/cucumber/godog"
)

const asnLateEventType = eventTypePrefix + "asn.AsnLateForArrival"

// theFlatAsnIsRegistered registers a FLAT ASN: lines, no carton manifest
// (the S-O-I form, ADR 0025).
func (w *world) theFlatAsnIsRegistered(number string) error {
	return w.mustCall(http.MethodPost, "/asns", asnBody(number))
}

// theOutboxHoldsAsnLate counts the AsnLateForArrival events on the outbox.
func (w *world) theOutboxHoldsAsnLate(n int) error {
	got := 0
	for _, m := range w.outbox.Messages() {
		if m.EventType == asnLateEventType {
			got++
		}
	}
	if got != n {
		return fmt.Errorf("AsnLateForArrival events = %d, want %d (outbox %v)", got, n, w.outboxTypes())
	}
	return nil
}

// iScanTheRawLabel posts a raw GS1-128 scan string.
func (w *world) iScanTheRawLabel(raw string) error {
	body, err := json.Marshal(map[string]string{"scan": raw})
	if err != nil {
		return err
	}
	return w.call(http.MethodPost, "/receipts/{receipt}/scans", w.freshKey(), string(body), nil)
}

// theScanIsAccepted asserts a 201 with accepted true.
func (w *world) theScanIsAccepted() error {
	if w.status != http.StatusCreated {
		return fmt.Errorf("scan status = %d body %s", w.status, w.body)
	}
	var out struct {
		Accepted bool `json:"accepted"`
	}
	if err := json.Unmarshal(w.body, &out); err != nil {
		return err
	}
	if !out.Accepted {
		return fmt.Errorf("accepted = false, body %s", w.body)
	}
	return nil
}

// theScanIsRefusedWith asserts a 200 with accepted false and exactly the one
// finding of the kind.
func (w *world) theScanIsRefusedWith(kind string) error {
	if w.status != http.StatusOK {
		return fmt.Errorf("scan status = %d body %s", w.status, w.body)
	}
	var out struct {
		Accepted      bool `json:"accepted"`
		Discrepancies []struct {
			Kind string `json:"kind"`
		} `json:"discrepancies"`
	}
	if err := json.Unmarshal(w.body, &out); err != nil {
		return err
	}
	if out.Accepted {
		return fmt.Errorf("accepted = true, want the refused scan, body %s", w.body)
	}
	if len(out.Discrepancies) != 1 || out.Discrepancies[0].Kind != kind {
		return fmt.Errorf("discrepancies = %+v, want exactly one %s", out.Discrepancies, kind)
	}
	return nil
}

// theLinesHave reads the receipt and asserts the good units of a line.
func (w *world) theLinesHave(line, good int) error {
	if err := w.call(http.MethodGet, "/receipts/{receipt}", "", "", nil); err != nil {
		return err
	}
	if w.status != http.StatusOK {
		return fmt.Errorf("GET receipt = %d %s", w.status, w.body)
	}
	var r struct {
		Lines []struct {
			LineNo       int   `json:"lineNo"`
			ReceivedGood int64 `json:"receivedGood"`
		} `json:"lines"`
	}
	if err := json.Unmarshal(w.body, &r); err != nil {
		return err
	}
	for _, l := range r.Lines {
		if l.LineNo == line {
			if l.ReceivedGood != int64(good) {
				return fmt.Errorf("line %d good = %s, want %d", line, strconv.FormatInt(l.ReceivedGood, 10), good)
			}
			return nil
		}
	}
	return fmt.Errorf("line %d not found in %s", line, w.body)
}

func (w *world) registerAsnTimeliness(sc *godog.ScenarioContext) {
	sc.Step(`^the flat ASN "([^"]*)" with lines but no cartons is registered$`, w.theFlatAsnIsRegistered)
	sc.Step(`^the outbox holds (\d+) AsnLateForArrival events?$`, w.theOutboxHoldsAsnLate)
	sc.Step(`^I scan "([^"]*)"$`, w.iScanTheRawLabel)
	sc.Step(`^the scan is accepted$`, w.theScanIsAccepted)
	sc.Step(`^the scan is refused with the finding "([^"]*)"$`, w.theScanIsRefusedWith)
	sc.Step(`^the receipt's line (\d+) has (\d+) good units$`, w.theLinesHave)
}
