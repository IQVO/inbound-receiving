package receipt

import (
	"errors"
	"strings"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// A valid GTIN-14 and SSCC used to build element strings (plain literals).
const (
	labelGTIN = "13041090004824"
	labelSSCC = "003456789012345670"
)

func parseErr(t *testing.T, raw string) error {
	t.Helper()
	_, err := gs1.ParseScan(raw)
	if err == nil {
		t.Fatalf("ParseScan(%q) succeeded; the fixture must violate the standard", raw)
	}
	return err
}

func TestClassifyLabelErrorUsesTheReasonOfEveryGs1Error(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want LabelReason
	}{
		{"SSCC bad check digit", "]C100" + "003456789012345671", ReasonBadCheckDigit},
		{"GTIN bad check digit", "]C101" + "13041090004825", ReasonBadCheckDigit},
		{"GLN bad check digit", "]C101" + labelGTIN + "410" + "4012345000008", ReasonBadCheckDigit},
		{"SSCC with a letter", "]C100" + "00345678901234567A", ReasonInvalidIdentifier},
		{"01 together with 02", "]C101" + labelGTIN + "02" + labelGTIN + "3712", ReasonConflictingAIs},
		{"02 without 00", "]C102" + labelGTIN + "3712", ReasonMissingAI},
		{"37 without 02", "]C100" + labelSSCC + "3712", ReasonMissingAI},
		{"only a lot, nothing identifying", "]C110ABC", ReasonMissingAI},
		{"same AI twice", "]C101" + labelGTIN + "01" + labelGTIN, ReasonDuplicateAI},
		{"unknown AI 99", "]C199ABC", ReasonUnknownAI},
		{"month 13 in AI 17", "]C101" + labelGTIN + "17" + "261340", ReasonBadDate},
		{"day 31 of February in AI 15", "]C101" + labelGTIN + "15" + "270231", ReasonBadDate},
		{"letters in AI 11", "]C101" + labelGTIN + "11" + "26AB10", ReasonBadDate},
		{"space in a lot", "]C101" + labelGTIN + "10" + "AB CD", ReasonBadCharacter},
		{"hash in a serial", "]C101" + labelGTIN + "21" + "A#B", ReasonBadCharacter},
		{"count that is not a number", "]C100" + labelSSCC + "02" + labelGTIN + "37" + "abc", ReasonMalformedElement},
		{"element shorter than its fixed length", "]C100" + "0034567", ReasonMalformedElement},
		{"lot above 20 characters", "]C101" + labelGTIN + "10" + "123456789012345678901", ReasonMalformedElement},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyLabelError(parseErr(t, tt.raw)); got != tt.want {
				t.Fatalf("ClassifyLabelError(%q) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}

func TestClassifyLabelErrorFallsBackToMalformed(t *testing.T) {
	if got := ClassifyLabelError(errors.New("something else")); got != ReasonMalformedElement {
		t.Fatalf("an error that is not a gs1 one = %q, want malformed_element", got)
	}
}

func TestLabelReasonsAreTheClosedSetOfTheADR(t *testing.T) {
	want := []LabelReason{
		"bad_check_digit", "invalid_identifier", "conflicting_ais", "missing_ai",
		"duplicate_ai", "unknown_ai", "bad_date", "bad_character", "malformed_element",
	}
	got := AllLabelReasons()
	if len(got) != len(want) {
		t.Fatalf("AllLabelReasons() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("AllLabelReasons()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestIsElementString(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"a GS1-128 label", "]C100" + labelSSCC, true},
		{"a label with a GS separator", "]C110ABC\x1d21XYZ", true},
		{"an unknown AI is still an attempted label", "]C199ABC", true},
		{"empty", "", false},
		{"identifier only", "]C1", false},
		{"EAN data", "]E0" + "4006381333931", false},
		{"ITF-14 data", "]I1" + labelGTIN, false},
		{"junk without symbology", "hello", false},
		{"a GS1-128 element string without the identifier", "00" + labelSSCC, false},
		{"lowercase identifier", "]c1" + "00" + labelSSCC, false},
		{"a space in the body", "]C100 " + labelSSCC, false},
		{"a control character in the body", "]C100\x01" + labelSSCC, false},
		{"DEL in the body", "]C100\x7f" + labelSSCC, false},
		{"a non ASCII byte", "]C100é" + labelSSCC, false},
		{"exactly 512 characters", "]C1" + strings.Repeat("A", 509), true},
		{"513 characters", "]C1" + strings.Repeat("A", 510), false},
		{"one character after the identifier", "]C1" + "0", true},
		{"the first printable character", "]C1" + "!", true},
		{"the last printable character", "]C1" + "~", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsElementString(tt.raw); got != tt.want {
				t.Fatalf("IsElementString(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestSafeScanned(t *testing.T) {
	long := strings.Repeat("A", 200)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"a short label is kept whole", "]C100" + labelSSCC, "]C100" + labelSSCC},
		{"the GS separator is shown as <GS>", "]C110ABC\x1d21XYZ", "]C110ABC<GS>21XYZ"},
		{"exactly 128 characters are kept", long[:128], long[:128]},
		{"129 characters are cut at 128", long[:129], long[:128]},
		{"the token is dropped when it would not fit whole", long[:126] + "\x1d" + "X", long[:126]},
		{"the token is kept when it fits exactly", long[:124] + "\x1d" + "X", long[:124] + "<GS>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SafeScanned(tt.raw)
			if got != tt.want {
				t.Fatalf("SafeScanned(%q) = %q (%d), want %q (%d)", tt.raw, got, len(got), tt.want, len(tt.want))
			}
			if len(got) > MaxScannedLength {
				t.Fatalf("SafeScanned returned %d characters, the bound is %d", len(got), MaxScannedLength)
			}
		})
	}
	if MaxScannedLength != 128 {
		t.Fatalf("MaxScannedLength = %d, ADR 0024 says 128", MaxScannedLength)
	}
}

func TestNewLabelViolation(t *testing.T) {
	raw := "]C101" + labelGTIN + "02" + labelGTIN + "3712"
	v := NewLabelViolation(raw, parseErr(t, raw))
	if v.Reason != ReasonConflictingAIs || v.Scanned != raw {
		t.Fatalf("violation = %+v", v)
	}
}

func TestRejectLabelRecordsOneFindingAndBooksNothing(t *testing.T) {
	r := openedCartons(t)
	before, linesBefore := r.Version(), r.Lines()
	raw := "]C101" + labelGTIN + "02" + labelGTIN + "3712"
	res, err := r.RejectLabel(NewLabelViolation(raw, parseErr(t, raw)), "ACME", at(3))
	if err != nil {
		t.Fatal(err)
	}
	want := CartonDiscrepancy{Kind: KindLabelNonCompliant, Expected: "conflicting_ais", Actual: raw, SupplierRef: "ACME"}
	if res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0] != want {
		t.Fatalf("result = %+v, want one finding %+v", res, want)
	}
	if len(res.Events) != 1 {
		t.Fatalf("events = %+v, want exactly one CartonDiscrepancyRaised", res.Events)
	}
	ev, ok := res.Events[0].(CartonDiscrepancyRaised)
	if !ok || ev.CartonDiscrepancy != want || !ev.OccurredAt().Equal(at(3)) {
		t.Fatalf("event = %+v", res.Events[0])
	}
	if r.Version() != before+1 {
		t.Fatalf("version = %d, want %d: a recorded finding is history", r.Version(), before+1)
	}
	assertNothingBooked(t, r, linesBefore)
	if f := r.CartonDiscrepancies(); len(f) != 1 || f[0].CartonDiscrepancy != want || !f[0].At.Equal(at(3)) {
		t.Fatalf("kept findings = %+v", f)
	}
}

func assertNothingBooked(t *testing.T, r *Receipt, linesBefore []Line) {
	t.Helper()
	for i, l := range r.Lines() {
		if l != linesBefore[i] {
			t.Fatalf("line %d changed: %+v", i, l)
		}
	}
	got := viewsOf(r.Cartons())
	if len(got) != 3 {
		t.Fatalf("the ledger changed: %+v", got)
	}
	for _, c := range got {
		if c.Status != CartonExpected {
			t.Fatalf("the ledger changed: %+v", got)
		}
	}
}

func TestRejectLabelOnAClosedReceiptIsRefused(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.Close(at(1)); err != nil {
		t.Fatal(err)
	}
	before, kept := r.Version(), len(r.CartonDiscrepancies())
	if _, err := r.RejectLabel(LabelViolation{Reason: ReasonUnknownAI, Scanned: "]C199A"}, "ACME", at(2)); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("err = %v, want ErrReceiptClosed", err)
	}
	if r.Version() != before || len(r.CartonDiscrepancies()) != kept {
		t.Fatal("a refused operation changed the receipt")
	}
}

func TestRejectDestinationRecordsTheGlnAndBooksNothing(t *testing.T) {
	r := openedCartons(t)
	before := r.Version()
	gln, err := gs1.ParseGLN("7612345000008")
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.RejectDestination(ssccA(t), gln, "ACME", at(2))
	if err != nil {
		t.Fatal(err)
	}
	want := CartonDiscrepancy{Kind: KindWrongDestination, SSCC: ssccA(t), Actual: "7612345000008", SupplierRef: "ACME"}
	if res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0] != want || len(res.Events) != 1 {
		t.Fatalf("result = %+v, want one finding %+v", res, want)
	}
	if c, _ := r.Carton(ssccA(t)); c.Status() != CartonExpected {
		t.Fatal("a refused carton must stay Expected")
	}
	if r.Version() != before+1 || r.Lines()[0].ReceivedGood() != 0 {
		t.Fatalf("version %d, good %d", r.Version(), r.Lines()[0].ReceivedGood())
	}
	// A label with no SSCC (a GTIN label) is recorded without one.
	res, err = r.RejectDestination(gs1.SSCC{}, gln, "ACME", at(3))
	if err != nil || !res.Discrepancies[0].SSCC.IsZero() {
		t.Fatalf("no-SSCC finding = %+v, %v", res.Discrepancies, err)
	}
	closed := openedCartons(t)
	if _, err := closed.Close(at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.RejectDestination(ssccA(t), gln, "ACME", at(2)); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("closed: %v", err)
	}
}

func mustGLN(t *testing.T, s string) gs1.GLN {
	t.Helper()
	g, err := gs1.ParseGLN(s)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestSiteGLNsAcceptsBothSidesOfTheList(t *testing.T) {
	siteA, siteB, other := mustGLN(t, "4012345000009"), mustGLN(t, "6141410000014"), mustGLN(t, "7612345000008")
	list, err := ParseSiteGLNs("4012345000009,6141410000014")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		gln  gs1.GLN
		want bool
	}{
		{"first of the list", siteA, true},
		{"last of the list", siteB, true},
		{"not in the list", other, false},
		{"no AI 410 on the label", gs1.GLN{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := list.Accepts(tt.gln); got != tt.want {
				t.Fatalf("Accepts(%q) = %v, want %v", tt.gln.String(), got, tt.want)
			}
		})
	}
	if !list.Enabled() {
		t.Fatal("a non-empty list must enable the rule")
	}
}

func TestSiteGLNsEmptyListTurnsTheRuleOff(t *testing.T) {
	for _, raw := range []string{"", "   ", ",", " , ,"} {
		list, err := ParseSiteGLNs(raw)
		if err != nil {
			t.Fatalf("ParseSiteGLNs(%q): %v", raw, err)
		}
		if list.Enabled() {
			t.Fatalf("ParseSiteGLNs(%q) enabled the rule", raw)
		}
		if !list.Accepts(mustGLN(t, "7612345000008")) {
			t.Fatalf("an empty list must accept every GLN (rule off), ParseSiteGLNs(%q)", raw)
		}
	}
	var zero SiteGLNs
	if zero.Enabled() || !zero.Accepts(mustGLN(t, "7612345000008")) {
		t.Fatal("the zero value is the rule off")
	}
}

func TestParseSiteGLNsTrimsAndRefusesAnInvalidEntry(t *testing.T) {
	list, err := ParseSiteGLNs(" 4012345000009 , ,6141410000014,")
	if err != nil || !list.Accepts(mustGLN(t, "6141410000014")) || list.Accepts(mustGLN(t, "7612345000008")) {
		t.Fatalf("list = %+v, %v", list, err)
	}
	for _, raw := range []string{"4012345000008", "401234500000", "4012345000009,abc", "40123450000091"} {
		if _, err := ParseSiteGLNs(raw); !errors.Is(err, ErrInvalidSiteGLN) {
			t.Fatalf("ParseSiteGLNs(%q) = %v, want ErrInvalidSiteGLN", raw, err)
		}
	}
}
