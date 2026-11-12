package report

import "sort"

// MaxTimelinessSuppliers bounds the per-supplier asn_timeliness block of a
// day (ADR 0025): at most the day's top ten suppliers by ASNs registered,
// ties broken by supplier_ref ascending (deterministic). A supplier outside
// the bound is absent from that day's block; its facts stay in the tables.
const MaxTimelinessSuppliers = 10

// SupplierTimeliness is one supplier's ASN timeliness of a day (ADR 0025):
// how many of the day's ASNs were registered for it (registered, counted in
// the UTC day of the ASN's registered_at) and how many of those were
// registered after the visit they were received under had arrived (late).
// ShareOnTime is 1 - late/registered, nil while Registered is 0.
type SupplierTimeliness struct {
	SupplierRef string
	Registered  int
	Late        int
	ShareOnTime *float64
}

// AsnTimelinessOf ranks one day's suppliers: both sides of the ratio are
// given per supplier (a supplier with a late fact but no registered count yet
// -- the analytics lag -- is listed with its zero), the share is defined
// once, and the list is cut at MaxTimelinessSuppliers, biggest registered
// count first, ties by supplier_ref ascending.
func AsnTimelinessOf(registered, late map[string]int) []SupplierTimeliness {
	seen := make(map[string]bool, len(registered)+len(late))
	out := make([]SupplierTimeliness, 0, len(registered)+len(late))
	for s := range registered {
		seen[s] = true
	}
	for s := range late {
		seen[s] = true
	}
	for s := range seen {
		row := SupplierTimeliness{SupplierRef: s, Registered: registered[s], Late: late[s]}
		if row.Registered > 0 {
			share := 1 - float64(row.Late)/float64(row.Registered)
			row.ShareOnTime = &share
		}
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Registered != out[j].Registered {
			return out[i].Registered > out[j].Registered
		}
		return out[i].SupplierRef < out[j].SupplierRef
	})
	if len(out) > MaxTimelinessSuppliers {
		out = out[:MaxTimelinessSuppliers]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
