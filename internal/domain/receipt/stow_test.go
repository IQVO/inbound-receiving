package receipt

import (
	"testing"
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// closedWithCartons opens a receipt from the appointment with the carton
// manifest of cartonAsn, accepts the given cartons in the given condition and
// closes it at at(60).
func closedWithCartons(t *testing.T, appt string, c Condition, accept ...string) *Receipt {
	t.Helper()
	r, _, err := Open(rid, cartonAsn(t).Snapshot(), appt, "", t0)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, s := range accept {
		if _, err := r.AcceptCarton(CartonScan{SSCC: mkSSCC(t, s), Condition: c}, at(10)); err != nil {
			t.Fatalf("AcceptCarton(%s): %v", s, err)
		}
	}
	if _, err := r.Close(at(60)); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return r
}

const (
	payloadA = "00345678901234567"
	payloadB = "00345678901234568"
	payloadM = "00345678901234569"
)

func lpnOf(t *testing.T, payload string) string { return mkSSCC(t, payload).String() }

func stowsOf(t *testing.T, a, b int) map[string]time.Time {
	t.Helper()
	m := map[string]time.Time{}
	if a != 0 {
		m[lpnOf(t, payloadA)] = at(a)
	}
	if b != 0 {
		m[lpnOf(t, payloadB)] = at(b)
	}
	return m
}

func TestCompleteStowWhenEveryGoodCartonIsStowed(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA, payloadB)
	got, ok := r.CompleteStow(at(-120), stowsOf(t, 70, 90))
	if !ok {
		t.Fatal("every Good carton is stowed: want a completion")
	}
	want := StowCompletion{ArrivedAt: at(-120), ClosedAt: at(60), StowedAt: at(90), DockToStockSeconds: 12600, CartonCount: 2}
	if got != want {
		t.Fatalf("completion = %+v, want %+v", got, want)
	}
}

func TestCompleteStowTakesTheLatestStowWhateverTheOrder(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA, payloadB)
	got, ok := r.CompleteStow(at(0), stowsOf(t, 90, 70))
	if !ok || !got.StowedAt.Equal(at(90)) || got.DockToStockSeconds != 5400 {
		t.Fatalf("completion = %+v, %v: the stop is the LATEST stow (90 min)", got, ok)
	}
}

func TestCompleteStowIsHeldBackUntilTheSetIsComplete(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA, payloadB)
	cases := map[string]map[string]time.Time{
		"no stow at all":         {},
		"only the first":         stowsOf(t, 70, 0),
		"only the second":        stowsOf(t, 0, 70),
		"a zero stow time":       {lpnOf(t, payloadA): at(70), lpnOf(t, payloadB): {}},
		"stows of other cartons": {lpnOf(t, payloadM): at(70), "LPN-other": at(80)},
	}
	for name, stows := range cases {
		if got, ok := r.CompleteStow(at(0), stows); ok {
			t.Fatalf("%s: completion = %+v, want none", name, got)
		}
	}
}

func TestCompleteStowIgnoresStowsOfCartonsThatAreNotOnTheReceipt(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA)
	stows := map[string]time.Time{lpnOf(t, payloadA): at(70), "LPN-elsewhere": at(500)}
	got, ok := r.CompleteStow(at(0), stows)
	if !ok || !got.StowedAt.Equal(at(70)) || got.CartonCount != 1 {
		t.Fatalf("completion = %+v, %v", got, ok)
	}
}

func TestCompleteStowNeedsAClosedReceipt(t *testing.T) {
	r, _, err := Open(rid, cartonAsn(t).Snapshot(), apptID, "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(CartonScan{SSCC: mkSSCC(t, payloadA), Condition: ConditionGood}, at(10)); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.CompleteStow(at(0), stowsOf(t, 70, 0)); ok {
		t.Fatalf("an open receipt completed: %+v", got)
	}
}

func TestCompleteStowNeedsAnAppointmentAndAKnownArrival(t *testing.T) {
	walkIn := closedWithCartons(t, "", ConditionGood, payloadA)
	if got, ok := walkIn.CompleteStow(at(0), stowsOf(t, 70, 0)); ok {
		t.Fatalf("a walk-in receipt has no arrival: %+v", got)
	}
	r := closedWithCartons(t, apptID, ConditionGood, payloadA)
	if got, ok := r.CompleteStow(time.Time{}, stowsOf(t, 70, 0)); ok {
		t.Fatalf("an unknown arrival completed: %+v", got)
	}
}

func TestCompleteStowOnlyCountsGoodAcceptedCartons(t *testing.T) {
	none := closedWithCartons(t, apptID, ConditionGood)
	if got, ok := none.CompleteStow(at(0), stowsOf(t, 70, 90)); ok {
		t.Fatalf("a receipt with no accepted carton completed: %+v", got)
	}
	damaged := closedWithCartons(t, apptID, ConditionDamaged, payloadA, payloadB)
	if got, ok := damaged.CompleteStow(at(0), stowsOf(t, 70, 90)); ok {
		t.Fatalf("a receipt of Damaged cartons completed: %+v", got)
	}
	// B is accepted Damaged and never stowed; A is Good and stowed.
	r, _, err := Open(rid, cartonAsn(t).Snapshot(), apptID, "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(CartonScan{SSCC: mkSSCC(t, payloadA), Condition: ConditionGood}, at(10)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcceptCarton(CartonScan{SSCC: mkSSCC(t, payloadB), Condition: ConditionDamaged}, at(11)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Close(at(60)); err != nil {
		t.Fatal(err)
	}
	got, ok := r.CompleteStow(at(0), stowsOf(t, 70, 0))
	if !ok || got.CartonCount != 1 {
		t.Fatalf("completion = %+v, %v: the Damaged and the never-accepted cartons do not count", got, ok)
	}
}

func TestCompleteStowDurationIsNeverNegative(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA)
	for name, tc := range map[string]struct {
		arrived time.Time
		want    int64
	}{
		"stowed before the arrival (clock skew)": {at(71), 0},
		"stowed at the arrival":                  {at(70), 0},
		"stowed one second after the arrival":    {at(70).Add(-time.Second), 1},
	} {
		got, ok := r.CompleteStow(tc.arrived, stowsOf(t, 70, 0))
		if !ok || got.DockToStockSeconds != tc.want {
			t.Fatalf("%s: seconds = %d (ok %v), want %d", name, got.DockToStockSeconds, ok, tc.want)
		}
	}
}

func TestCompleteStowDropsSubSecondFractions(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA)
	got, ok := r.CompleteStow(at(70).Add(-1900*time.Millisecond), stowsOf(t, 70, 0))
	if !ok || got.DockToStockSeconds != 1 {
		t.Fatalf("seconds = %d, want 1 (whole seconds)", got.DockToStockSeconds)
	}
}

func TestStowCompletedEventCarriesTheFactAndTheInjectedTime(t *testing.T) {
	r := closedWithCartons(t, apptID, ConditionGood, payloadA, payloadB)
	ev, ok := r.StowCompleted(at(0), stowsOf(t, 70, 90), at(100))
	if !ok {
		t.Fatal("want an event")
	}
	assertHeader(t, ev, "ReceiptStowCompleted", at(100))
	if ev.AppointmentID != apptID || !ev.ArrivedAt.Equal(at(0)) || !ev.ClosedAt.Equal(at(60)) || !ev.StowedAt.Equal(at(90)) ||
		ev.DockToStockSeconds != 5400 || ev.CartonCount != 2 {
		t.Fatalf("event = %+v", ev)
	}
	if _, ok := r.StowCompleted(at(0), stowsOf(t, 70, 0), at(100)); ok {
		t.Fatal("an incomplete set must not raise the event")
	}
}

func TestCartonIsGoodOnlyWhenAcceptedAndEveryContentIsGood(t *testing.T) {
	mixed := func(second string) Carton {
		r, err := Rehydrate(Persisted{
			ID: rid, AsnNumber: asnNum, State: StateOpen, OpenedAt: t0, Version: 2,
			Lines: []LineState{{LineNo: 1, SKU: "SKU-1", ExpectedQty: 30}},
			Cartons: []CartonState{{
				LPN: "LPN-1", Kind: string(asn.CartonMixed), Status: CartonAccepted, Sealed: true, ReceivedAt: t0,
				Contents: []CartonContentState{
					{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 10, Condition: string(ConditionGood)},
					{LineNo: 1, SKU: "SKU-1", GTIN: eachGTIN1(t).String(), Qty: 20, Condition: second},
				},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return r.Cartons()[0]
	}
	if !mixed(string(ConditionGood)).IsGood() {
		t.Fatal("an accepted carton of Good contents is Good")
	}
	if mixed(string(ConditionDamaged)).IsGood() {
		t.Fatal("a carton with a Damaged content is not Good")
	}
	expected := openedCartons(t).Cartons()[0]
	if expected.IsGood() {
		t.Fatal("an Expected carton is not Good")
	}
}
