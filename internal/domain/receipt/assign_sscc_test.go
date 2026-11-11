package receipt

import (
	"errors"
	"reflect"
	"testing"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func TestParseCompanyPrefixBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
		unset   bool
	}{
		{"unset", "", false, true},
		{"6 digits is too short", "061414", true, false},
		{"7 digits is the minimum", "0614141", false, false},
		{"8 digits", "06141412", false, false},
		{"10 digits is the maximum", "0614141123", false, false},
		{"11 digits is too long", "06141411234", true, false},
		{"a letter", "061414A", true, false},
		{"a leading space", " 0614141", true, false},
		{"a trailing newline", "0614141\n", true, false},
		{"a sign", "+0614141", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ParseCompanyPrefix(tt.raw)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidCompanyPrefix) {
					t.Fatalf("ParseCompanyPrefix(%q) = %v, want ErrInvalidCompanyPrefix", tt.raw, err)
				}
				return
			}
			if err != nil || p.IsZero() != tt.unset || p.String() != tt.raw {
				t.Fatalf("ParseCompanyPrefix(%q) = %+v, %v", tt.raw, p, err)
			}
		})
	}
}

func TestCompanyPrefixBuildsTheSSCCOfASerial(t *testing.T) {
	seven, _ := ParseCompanyPrefix("0614141")
	ten, _ := ParseCompanyPrefix("0614141123")
	tests := []struct {
		name   string
		prefix CompanyPrefix
		serial int64
		want   string
	}{
		{"7 digit prefix, serial 1", seven, 1, "006141410000000012"},
		{"7 digit prefix, serial 2", seven, 2, "006141410000000029"},
		{"7 digit prefix, the largest serial", seven, 999999999, "006141419999999994"},
		{"10 digit prefix, serial 1", ten, 1, "006141411230000018"},
		{"10 digit prefix, serial 42", ten, 42, "006141411230000421"},
		{"10 digit prefix, the largest serial", ten, 999999, "006141411239999993"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.prefix.Build(tt.serial)
			if err != nil || got.String() != tt.want {
				t.Fatalf("Build(%d) = %q, %v; want %q", tt.serial, got.String(), err, tt.want)
			}
			if got.ExtensionDigit() != 0 {
				t.Fatalf("extension digit = %d, want 0", got.ExtensionDigit())
			}
			if _, err := gs1.ParseSSCC(got.String()); err != nil {
				t.Fatalf("the SSCC must have a valid check digit: %v", err)
			}
		})
	}
}

func TestCompanyPrefixSerialBounds(t *testing.T) {
	seven, _ := ParseCompanyPrefix("0614141")
	ten, _ := ParseCompanyPrefix("0614141123")
	if seven.MaxSerial() != 999999999 || ten.MaxSerial() != 999999 {
		t.Fatalf("MaxSerial = %d / %d, want 999999999 / 999999", seven.MaxSerial(), ten.MaxSerial())
	}
	for _, tt := range []struct {
		p      CompanyPrefix
		serial int64
		ok     bool
	}{
		{seven, 0, false}, {seven, -1, false}, {seven, 1, true}, {seven, 999999999, true}, {seven, 1000000000, false},
		{ten, 0, false}, {ten, 999999, true}, {ten, 1000000, false},
	} {
		if _, err := tt.p.Build(tt.serial); (err == nil) != tt.ok {
			t.Errorf("%s.Build(%d) error = %v, want ok=%v", tt.p, tt.serial, err, tt.ok)
		}
	}
}

func TestSerialsGiveDistinctMonotonicSSCCs(t *testing.T) {
	p, _ := ParseCompanyPrefix("0614141")
	seen := map[string]int64{}
	prev := ""
	for n := int64(1); n <= 50; n++ {
		s, err := p.Build(n)
		if err != nil {
			t.Fatal(err)
		}
		if other, dup := seen[s.String()]; dup {
			t.Fatalf("serials %d and %d give the same SSCC %s", other, n, s)
		}
		seen[s.String()] = n
		if prev != "" && s.String()[:17] <= prev[:17] {
			t.Fatalf("serial %d: the SSCC payload %s is not above %s", n, s.String()[:17], prev[:17])
		}
		prev = s.String()
	}
}

func TestUnsetPrefixCannotBuild(t *testing.T) {
	var p CompanyPrefix
	if !p.IsZero() || p.MaxSerial() != 0 {
		t.Fatalf("zero prefix: IsZero=%v MaxSerial=%d", p.IsZero(), p.MaxSerial())
	}
	if _, err := p.Build(1); !errors.Is(err, ErrCompanyPrefixNotConfigured) {
		t.Fatalf("Build with no prefix = %v, want ErrCompanyPrefixNotConfigured", err)
	}
}

func assignScan(t *testing.T, sscc string) CaseScan {
	t.Helper()
	s, err := gs1.ParseSSCC(sscc)
	if err != nil {
		t.Fatal(err)
	}
	in := caseScan(t, 1, sscc)
	in.AssignedSSCC = s
	return in
}

const assigned = "006141410000000012"

func TestAssignSsccReceivesOneCaseUnderTheReceiversSSCC(t *testing.T) {
	r := openedCartons(t)
	res, err := r.AssignSscc(assignScan(t, assigned), at(4))
	if err != nil {
		t.Fatal(err)
	}
	h := Header{ID: rid, Asn: asnNum, At: at(4)}
	sscc, _ := gs1.ParseSSCC(assigned)
	content := CartonContent{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t), Qty: 12, Lot: "L9", Expiry: "2028-01-31", Condition: ConditionGood}
	wantEvents := []Event{
		ReceiptLineReceived{Header: h, LineNo: 1, SKU: "SKU-1", Quantity: 12, Condition: ConditionGood, OwnerRef: owner, PackGTIN: caseGTIN1(t), Cases: 1, SSCC: sscc},
		ReceiptCartonReceived{Header: h, OwnerRef: owner, SSCC: sscc, SSCCSource: SsccReceiver, Sealed: true, LPN: assigned, Contents: []CartonContent{content}},
		CartonDiscrepancyRaised{Header: h, CartonDiscrepancy: CartonDiscrepancy{Kind: KindUnlabelledCarton, Actual: assigned}},
	}
	if !res.Accepted || !reflect.DeepEqual(res.Events, wantEvents) {
		t.Fatalf("result = %+v\nwant events %+v", res, wantEvents)
	}
	c, ok := r.Carton(sscc)
	if !ok || c.LPN() != assigned || c.SSCCSource() != SsccReceiver || c.Status() != CartonAccepted || !c.Sealed() || c.Kind() != asn.CartonHomogeneousCase {
		t.Fatalf("carton = %+v, %v", c, ok)
	}
	if r.Lines()[0].ReceivedGood() != 12 || r.Version() != 2 {
		t.Fatalf("good %d version %d", r.Lines()[0].ReceivedGood(), r.Version())
	}
	if f := r.CartonDiscrepancies(); len(f) != 1 || f[0].Kind != KindUnlabelledCarton {
		t.Fatalf("findings = %+v", f)
	}
}

func TestAssignSsccOnAFlatReceiptRaisesNoFinding(t *testing.T) {
	r := opened(t)
	res, err := r.AssignSscc(assignScan(t, assigned), at(1))
	if err != nil || !res.Accepted || len(res.Discrepancies) != 0 || len(res.Events) != 2 {
		t.Fatalf("result = %+v, %v", res, err)
	}
}

func TestAssignSsccIsAcceptedOnlyForOneCaseWithItsOwnLPN(t *testing.T) {
	tests := map[string]func(in *CaseScan){
		"no assigned sscc":        func(in *CaseScan) { in.AssignedSSCC = gs1.SSCC{} },
		"two cases":               func(in *CaseScan) { in.Cases, in.LPNs = 2, []string{assigned, "LPN-B"} },
		"the lpn is not the sscc": func(in *CaseScan) { in.LPNs = []string{"LPN-A"} },
		"no lpn":                  func(in *CaseScan) { in.LPNs = nil },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := openedCartons(t)
			in := assignScan(t, assigned)
			mutate(&in)
			if _, err := r.AssignSscc(in, at(1)); !errors.Is(err, ErrScanMismatch) {
				t.Fatalf("err = %v, want ErrScanMismatch", err)
			}
			if r.Version() != 1 || len(r.Cartons()) != 3 {
				t.Fatal("a refused assignment changed the receipt")
			}
		})
	}
}

func TestAssignSsccRefusals(t *testing.T) {
	r := openedCartons(t)
	in := assignScan(t, assigned)
	in.AssignedSSCC = ssccA(t)
	in.LPNs = []string{ssccA(t).String()}
	if _, err := r.AssignSscc(in, at(1)); !errors.Is(err, ErrScanMismatch) {
		t.Fatalf("an SSCC already on the ledger: %v", err)
	}
	in = assignScan(t, assigned)
	in.SKU = "SKU-9"
	if _, err := r.AssignSscc(in, at(1)); !errors.Is(err, ErrLineNotOnAsn) {
		t.Fatalf("unknown sku: %v", err)
	}
	in = assignScan(t, assigned)
	in.Condition = "Broken"
	if _, err := r.AssignSscc(in, at(1)); !errors.Is(err, ErrInvalidCondition) {
		t.Fatalf("condition: %v", err)
	}
	closed := openedCartons(t)
	if _, err := closed.Close(at(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := closed.AssignSscc(assignScan(t, assigned), at(2)); !errors.Is(err, ErrReceiptClosed) {
		t.Fatalf("closed: %v", err)
	}
	over := openedCartons(t)
	receive(t, over, 1, maxQty, ConditionGood)
	if _, err := over.AssignSscc(assignScan(t, assigned), at(2)); !errors.Is(err, shared.ErrInvalidQuantity) {
		t.Fatalf("overflow: %v", err)
	}
	if over.Version() != 2 || len(over.Cartons()) != 3 {
		t.Fatal("a refused assignment changed the receipt")
	}
}

func TestAssignedSsccIsADuplicateWhenScannedLater(t *testing.T) {
	r := openedCartons(t)
	if _, err := r.AssignSscc(assignScan(t, assigned), at(1)); err != nil {
		t.Fatal(err)
	}
	sscc, _ := gs1.ParseSSCC(assigned)
	res, err := r.AcceptCarton(CartonScan{SSCC: sscc, Condition: ConditionGood}, at(2))
	if err != nil || res.Accepted || len(res.Discrepancies) != 1 || res.Discrepancies[0].Kind != KindDuplicateSscc {
		t.Fatalf("re-scanning the printed label = %+v, %v; want DuplicateSscc", res, err)
	}
}
