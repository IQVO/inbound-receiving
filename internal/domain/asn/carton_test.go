package asn

import (
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

func sscc(t *testing.T, payload string) string {
	t.Helper()
	s, err := gs1.NewSSCC(payload)
	if err != nil {
		t.Fatalf("NewSSCC(%q): %v", payload, err)
	}
	return s.String()
}

func gtin(t *testing.T, payload string) string {
	t.Helper()
	g, err := gs1.NewGTIN(payload)
	if err != nil {
		t.Fatalf("NewGTIN(%q): %v", payload, err)
	}
	return g.String()
}

// The same payloads everywhere: three cartons, one pallet.
const (
	ssccPayloadA = "00345678901234567"
	ssccPayloadB = "00345678901234568"
	ssccPayloadC = "00345678901234569"
	ssccPayloadP = "00345678909999999"
	gtinPayload1 = "1000123456789"
	gtinPayload2 = "1000123456790"
)

// cartonLines are two lines: SKU-1 x 40 and SKU-2 x 5.
func cartonDraft(t *testing.T) Draft {
	t.Helper()
	g1, g2 := gtin(t, gtinPayload1), gtin(t, gtinPayload2)
	return Draft{
		Number: "ASN-1001", SupplierRef: "ACME", OwnerRef: "tenant-7f3a", ExpectedArrival: t0.Add(24 * time.Hour),
		Lines: twoLines(),
		Cartons: []CartonInput{
			{
				SSCC: sscc(t, ssccPayloadA), ParentSSCC: sscc(t, ssccPayloadP), Kind: "HOMOGENEOUS_CASE",
				Contents:     []ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20, Lot: "L2310", Expiry: "2027-10-31"}},
				GrossWeightG: 5000, Dims: &DimsInput{LengthMm: 400, WidthMm: 300, HeightMm: 200},
			},
			{
				SSCC: sscc(t, ssccPayloadB), ParentSSCC: sscc(t, ssccPayloadP), Kind: "HOMOGENEOUS_CASE",
				Contents: []ContentInput{{LineNo: 1, SKU: "SKU-1", GTIN: g1, Qty: 20}},
			},
			{
				SSCC: sscc(t, ssccPayloadC), Kind: "MIXED",
				Contents: []ContentInput{{LineNo: 2, SKU: "SKU-2", GTIN: g2, Qty: 5}},
			},
		},
	}
}

type contentView struct {
	LineNo int
	SKU    shared.SKU
	GTIN   string
	Qty    int64
	Lot    string
	Expiry string
}

type cartonView struct {
	SSCC     string
	Parent   string
	Kind     CartonKind
	Weight   int64
	Dims     string
	Contents []contentView
}

func viewOf(c Carton) cartonView {
	v := cartonView{SSCC: c.SSCC().String(), Parent: c.ParentSSCC().String(), Kind: c.Kind(), Weight: c.GrossWeightG()}
	if d, ok := c.Dims(); ok {
		v.Dims = fmt.Sprintf("%dx%dx%d", d.LengthMm(), d.WidthMm(), d.HeightMm())
	}
	for _, x := range c.Contents() {
		v.Contents = append(v.Contents, contentView{x.LineNo(), x.SKU(), x.GTIN().String(), x.Qty(), x.Lot(), x.Expiry()})
	}
	return v
}

func viewsOf(cs []Carton) []cartonView {
	out := make([]cartonView, 0, len(cs))
	for _, c := range cs {
		out = append(out, viewOf(c))
	}
	return out
}

func wantCartonViews(t *testing.T) []cartonView {
	g1, g2 := gtin(t, gtinPayload1), gtin(t, gtinPayload2)
	return []cartonView{
		{
			SSCC: sscc(t, ssccPayloadA), Parent: sscc(t, ssccPayloadP), Kind: CartonHomogeneousCase, Weight: 5000, Dims: "400x300x200",
			Contents: []contentView{{1, "SKU-1", g1, 20, "L2310", "2027-10-31"}},
		},
		{
			SSCC: sscc(t, ssccPayloadB), Parent: "", Kind: CartonHomogeneousCase,
			Contents: []contentView{{1, "SKU-1", g1, 20, "", ""}},
		},
		{
			SSCC: sscc(t, ssccPayloadC), Parent: "", Kind: CartonMixed,
			Contents: []contentView{{2, "SKU-2", g2, 5, "", ""}},
		},
	}
}

func TestRegisterDraftWithCartons(t *testing.T) {
	a, events, err := RegisterDraft(cartonDraft(t), t0)
	if err != nil {
		t.Fatal(err)
	}
	if a.OwnerRef() != "tenant-7f3a" || a.State() != Registered || a.Version() != 1 {
		t.Fatalf("asn = %+v", a)
	}
	if got, want := viewsOf(a.Cartons()), wantCartonViews(t); !reflect.DeepEqual(got, want) {
		t.Fatalf("cartons = %+v\nwant     %+v", got, want)
	}
	e := onlyEvent(t, events).(ASNRegistered)
	if e.OwnerRef != "tenant-7f3a" || !reflect.DeepEqual(viewsOf(e.Cartons), wantCartonViews(t)) || len(e.Lines) != 2 {
		t.Fatalf("event = %+v", e)
	}
	snap := a.Snapshot()
	if snap.OwnerRef != "tenant-7f3a" || !reflect.DeepEqual(viewsOf(snap.Cartons), wantCartonViews(t)) {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestCartonsAreCopiedOut(t *testing.T) {
	a, _, err := RegisterDraft(cartonDraft(t), t0)
	if err != nil {
		t.Fatal(err)
	}
	a.Cartons()[0] = Carton{}
	a.Cartons()[1].Contents()[0] = CartonContent{}
	if a.Cartons()[0].SSCC().IsZero() || a.Cartons()[1].Contents()[0].Qty() != 20 {
		t.Fatal("mutating a returned copy changed the aggregate")
	}
}

func TestFlatAsnHasNoCartonsAndNoOwner(t *testing.T) {
	a := registered(t)
	if len(a.Cartons()) != 0 || a.OwnerRef() != "" {
		t.Fatalf("flat asn has cartons %v owner %q", a.Cartons(), a.OwnerRef())
	}
	e := onlyEventOf(t, a)
	if len(e.Cartons) != 0 || e.OwnerRef != "" {
		t.Fatalf("flat event = %+v", e)
	}
}

func onlyEventOf(t *testing.T, a *Asn) ASNRegistered {
	t.Helper()
	_, events, err := RegisterDraft(Draft{Number: string(a.Number()), SupplierRef: a.SupplierRef(), Lines: twoLines()}, t0)
	if err != nil {
		t.Fatal(err)
	}
	return onlyEvent(t, events).(ASNRegistered)
}

func TestRegisterDraftOwnerRef(t *testing.T) {
	for _, owner := range []string{"", "a", strings.Repeat("o", shared.MaxOwnerRefLength), "t.1_x-2"} {
		d := cartonDraft(t)
		d.OwnerRef = owner
		if _, _, err := RegisterDraft(d, t0); err != nil {
			t.Errorf("owner %q: %v", owner, err)
		}
	}
	for _, owner := range []string{strings.Repeat("o", shared.MaxOwnerRefLength+1), "a/b", "a b"} {
		d := cartonDraft(t)
		d.OwnerRef = owner
		if _, _, err := RegisterDraft(d, t0); !errors.Is(err, shared.ErrInvalidOwnerRef) {
			t.Errorf("owner %q: err = %v", owner, err)
		}
	}
}

func mutateDraft(t *testing.T, mutate func(d *Draft)) Draft {
	t.Helper()
	d := cartonDraft(t)
	mutate(&d)
	return d
}

func TestRegisterDraftRejectsBadCartons(t *testing.T) {
	g1 := gtin(t, gtinPayload1)
	cases := []struct {
		name   string
		mutate func(d *Draft)
		want   error
	}{
		{"sscc too short", func(d *Draft) { d.Cartons[0].SSCC = "0034567890123456" }, ErrInvalidSscc},
		{"sscc bad check digit", func(d *Draft) { d.Cartons[0].SSCC = d.Cartons[0].SSCC[:17] + "0" }, ErrInvalidSscc},
		{"sscc not digits", func(d *Draft) { d.Cartons[0].SSCC = "00345678901234567x" }, ErrInvalidSscc},
		{"pallet sscc invalid", func(d *Draft) { d.Cartons[0].ParentSSCC = "123" }, ErrInvalidSscc},
		{"duplicate sscc", func(d *Draft) { d.Cartons[1].SSCC = d.Cartons[0].SSCC }, ErrDuplicateSscc},
		{"pallet sscc is a carton sscc", func(d *Draft) { d.Cartons[0].ParentSSCC = d.Cartons[2].SSCC }, ErrDuplicateSscc},
		{"carton is its own pallet", func(d *Draft) { d.Cartons[2].ParentSSCC = d.Cartons[2].SSCC }, ErrDuplicateSscc},
		{"unknown kind", func(d *Draft) { d.Cartons[0].Kind = "PALLET" }, ErrInvalidCartonKind},
		{"empty kind", func(d *Draft) { d.Cartons[0].Kind = "" }, ErrInvalidCartonKind},
		{"no contents", func(d *Draft) { d.Cartons[2].Contents = nil }, ErrInvalidCarton},
		{"negative weight", func(d *Draft) { d.Cartons[0].GrossWeightG = -1 }, ErrInvalidCarton},
		{"zero length", func(d *Draft) { d.Cartons[0].Dims.LengthMm = 0 }, ErrInvalidCarton},
		{"zero width", func(d *Draft) { d.Cartons[0].Dims.WidthMm = 0 }, ErrInvalidCarton},
		{"zero height", func(d *Draft) { d.Cartons[0].Dims.HeightMm = 0 }, ErrInvalidCarton},
		{"lot too long", func(d *Draft) { d.Cartons[0].Contents[0].Lot = strings.Repeat("L", MaxLotLength+1) }, ErrInvalidCarton},
		{"lot with space", func(d *Draft) { d.Cartons[0].Contents[0].Lot = "L 1" }, ErrInvalidCarton},
		{"lot with control", func(d *Draft) { d.Cartons[0].Contents[0].Lot = "L\t1" }, ErrInvalidCarton},
		{"lot with delete", func(d *Draft) { d.Cartons[0].Contents[0].Lot = "L\x7f1" }, ErrInvalidCarton},
		{"lot with non ascii", func(d *Draft) { d.Cartons[0].Contents[0].Lot = "Lé" }, ErrInvalidCarton},
		{"expiry not a date", func(d *Draft) { d.Cartons[0].Contents[0].Expiry = "2027-13-01" }, ErrInvalidCarton},
		{"expiry two digit year", func(d *Draft) { d.Cartons[0].Contents[0].Expiry = "27-10-31" }, ErrInvalidCarton},
		{"expiry with time", func(d *Draft) { d.Cartons[0].Contents[0].Expiry = "2027-10-31T00:00:00Z" }, ErrInvalidCarton},
		{"zero quantity", func(d *Draft) { d.Cartons[0].Contents[0].Qty = 0 }, shared.ErrInvalidQuantity},
		{"bad sku", func(d *Draft) { d.Cartons[0].Contents[0].SKU = "bad sku" }, shared.ErrInvalidSKU},
		{"gtin bad check digit", func(d *Draft) { d.Cartons[0].Contents[0].GTIN = g1[:13] + "0" }, gs1.ErrInvalidGTIN},
		{"gtin empty", func(d *Draft) { d.Cartons[0].Contents[0].GTIN = "" }, gs1.ErrInvalidGTIN},
		{"gtin variable measure", func(d *Draft) { d.Cartons[0].Contents[0].GTIN = gtin(t, "9000123456789") }, gs1.ErrVariableMeasure},
		{"homogeneous with two skus", func(d *Draft) {
			d.Cartons[0].Contents = append(d.Cartons[0].Contents, ContentInput{LineNo: 2, SKU: "SKU-2", GTIN: g1, Qty: 1})
			d.Cartons[2].Contents[0].Qty = 4
		}, ErrHomogeneousCartonMixed},
		{"content line not on the asn", func(d *Draft) { d.Cartons[2].Contents[0].LineNo = 3 }, ErrCartonContentsMismatch},
		{"content line zero", func(d *Draft) { d.Cartons[2].Contents[0].LineNo = 0 }, ErrCartonContentsMismatch},
		{"content sku differs from the line", func(d *Draft) { d.Cartons[2].Contents[0].SKU = "SKU-1" }, ErrCartonContentsMismatch},
		{"sum below expected", func(d *Draft) { d.Cartons[1].Contents[0].Qty = 19 }, ErrCartonContentsMismatch},
		{"sum above expected", func(d *Draft) { d.Cartons[1].Contents[0].Qty = 21 }, ErrCartonContentsMismatch},
		{"a line is not covered", func(d *Draft) { d.Cartons = d.Cartons[:2] }, ErrCartonContentsMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, events, err := RegisterDraft(mutateDraft(t, tc.mutate), t0)
			if !errors.Is(err, tc.want) || a != nil || events != nil {
				t.Fatalf("RegisterDraft = %v, %v, %v; want %v", a, events, err, tc.want)
			}
		})
	}
}

func TestRegisterDraftAcceptsBoundaries(t *testing.T) {
	cases := map[string]func(d *Draft){
		"lot of exactly 20":                   func(d *Draft) { d.Cartons[0].Contents[0].Lot = strings.Repeat("L", MaxLotLength) },
		"lot of one":                          func(d *Draft) { d.Cartons[0].Contents[0].Lot = "~" },
		"lot at both ends of printable ascii": func(d *Draft) { d.Cartons[0].Contents[0].Lot = "!~" },
		"weight of one gram":                  func(d *Draft) { d.Cartons[0].GrossWeightG = 1 },
		"dimensions of one":                   func(d *Draft) { d.Cartons[0].Dims = &DimsInput{LengthMm: 1, WidthMm: 1, HeightMm: 1} },
		"leap day expiry":                     func(d *Draft) { d.Cartons[0].Contents[0].Expiry = "2028-02-29" },
		"one content two lots":                func(d *Draft) { d.Cartons[0].Contents = splitContent(d.Cartons[0].Contents[0]) },
		"carton without pallet":               func(d *Draft) { d.Cartons[0].ParentSSCC = "" },
		"two cartons one pallet":              func(d *Draft) { d.Cartons[2].ParentSSCC = d.Cartons[0].ParentSSCC },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := RegisterDraft(mutateDraft(t, mutate), t0); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func splitContent(c ContentInput) []ContentInput {
	a, b := c, c
	a.Qty, b.Qty, b.Lot = 5, c.Qty-5, "L2311"
	return []ContentInput{a, b}
}

func TestRehydrateDraftKeepsCartons(t *testing.T) {
	a, _, err := RegisterDraft(cartonDraft(t), t0)
	if err != nil {
		t.Fatal(err)
	}
	back, err := RehydrateDraft(Persisted{Draft: cartonDraft(t), State: Receiving, Version: 3})
	if err != nil {
		t.Fatal(err)
	}
	if back.State() != Receiving || back.Version() != 3 || back.OwnerRef() != a.OwnerRef() || len(back.Cartons()) != 3 {
		t.Fatalf("rehydrated = %+v", back)
	}
	if _, err := RehydrateDraft(Persisted{Draft: cartonDraft(t), State: "Bogus", Version: 1}); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("bad state: %v", err)
	}
	if _, err := RehydrateDraft(Persisted{Draft: cartonDraft(t), State: Registered, Version: 0}); !errors.Is(err, ErrInvalidVersion) {
		t.Fatalf("bad version: %v", err)
	}
	bad := cartonDraft(t)
	bad.Cartons[0].Contents[0].Qty = 19
	if _, err := RehydrateDraft(Persisted{Draft: bad, State: Registered, Version: 1}); !errors.Is(err, ErrCartonContentsMismatch) {
		t.Fatalf("rehydrate must re-validate cartons: %v", err)
	}
}

func TestCartonGTINIsNormalisedToFourteenDigits(t *testing.T) {
	cd, err := gs1.Mod10CheckDigit("629104150021")
	if err != nil {
		t.Fatal(err)
	}
	d := cartonDraft(t)
	d.Cartons[0].Contents[0].GTIN = "629104150021" + strconv.Itoa(cd)
	a, _, err := RegisterDraft(d, t0)
	if err != nil {
		t.Fatal(err)
	}
	if got := a.Cartons()[0].Contents()[0].GTIN().String(); len(got) != 14 || got[0] != '0' || got[1:13] != "629104150021" {
		t.Fatalf("gtin = %q", got)
	}
}
