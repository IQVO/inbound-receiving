package receipt

import "testing"

// TestCodingTable pins the vocabulary of ADR 0023: every kind the aggregate
// can raise, its W13 receiving condition code (empty = unmapped) and its CBV
// disposition. The W13 codes and the CBV names are the ones of the standards
// research (W13 01 damaged, 02 short, 03 over, 05 incorrect product, 09 hold).
func TestCodingTable(t *testing.T) {
	want := map[Kind]Coding{
		KindShort:                       {W13: "02", CBV: "mismatch_quantity"},
		KindOver:                        {W13: "03", CBV: "mismatch_quantity"},
		KindDamaged:                     {W13: "01", CBV: "damaged"},
		KindIncorrectProduct:            {W13: "05", CBV: "mismatch_class"},
		KindHold:                        {W13: "09", CBV: "non_conformant"},
		KindUnexpectedCarton:            {W13: "05", CBV: "mismatch_instance"},
		KindUnknownGtin:                 {W13: "05", CBV: "mismatch_class"},
		KindMixedCaseDeclaredAsCasePack: {W13: "05", CBV: "mismatch_class"},
		KindMissingCarton:               {W13: "02", CBV: "mismatch_quantity"},
		KindDuplicateSscc:               {W13: "", CBV: "mismatch_instance"},
		KindCaseQtyMismatch:             {W13: "", CBV: "mismatch_quantity"},
		KindMixedCartonContentMismatch:  {W13: "", CBV: "mismatch_quantity"},
		KindUnlabelledCarton:            {W13: "", CBV: "non_conformant"},
		KindCartonOverLimit:             {W13: "", CBV: "non_conformant"},
	}
	for kind, coding := range want {
		got, ok := CodingOf(kind)
		if !ok || got != coding {
			t.Errorf("CodingOf(%s) = %+v, %v; want %+v", kind, got, ok, coding)
		}
		if got.Mapped() != (coding.W13 != "") {
			t.Errorf("%s: Mapped() = %v, want %v", kind, got.Mapped(), coding.W13 != "")
		}
	}
	if len(want) != 14 {
		t.Fatalf("the table under test has %d rows, ADR 0023 has 14", len(want))
	}
}

func TestEveryKindIsCoded(t *testing.T) {
	kinds := AllKinds()
	if len(kinds) != 14 {
		t.Fatalf("AllKinds() = %d kinds, want 14: %v", len(kinds), kinds)
	}
	seen := map[Kind]bool{}
	for _, k := range kinds {
		if seen[k] {
			t.Errorf("AllKinds() lists %s twice", k)
		}
		seen[k] = true
		c, ok := CodingOf(k)
		if !ok || c.CBV == "" {
			t.Errorf("kind %s has no CBV disposition: %+v, %v", k, c, ok)
		}
	}
	// A kind the carton layer accepts from storage must be coded too.
	for k := range cartonKinds {
		if !seen[k] {
			t.Errorf("carton kind %s is accepted from storage but missing from AllKinds()", k)
		}
	}
}

func TestWorkedCodesAreOnlyTheW13SetOfTheStandard(t *testing.T) {
	allowed := map[W13Code]bool{"01": true, "02": true, "03": true, "05": true, "09": true}
	mapped := 0
	for _, k := range AllKinds() {
		c, _ := CodingOf(k)
		if !c.Mapped() {
			continue
		}
		mapped++
		if !allowed[c.W13] {
			t.Errorf("%s maps to %q, which is not a W13 code of the research (01 02 03 05 09)", k, c.W13)
		}
	}
	if mapped != 9 {
		t.Fatalf("%d kinds are mapped, ADR 0023 maps 9 and leaves 5 unmapped", mapped)
	}
}

func TestUnknownKindHasNoCoding(t *testing.T) {
	if c, ok := CodingOf(Kind("Quarantined")); ok || c != (Coding{}) {
		t.Fatalf("CodingOf(unknown) = %+v, %v; want the zero coding and false (there is no quarantined disposition)", c, ok)
	}
	if (Coding{}).Mapped() {
		t.Fatal("the zero coding must not be mapped")
	}
}
