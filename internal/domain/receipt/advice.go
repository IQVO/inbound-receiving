package receipt

import (
	"time"

	"github.com/claudioed/inbound-receiving/internal/domain/shared"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// The bounds of the receiving advice payload (ADR 0023). A relay row the broker
// refuses blocks the outbox (ADR 0007) and the broker's default message limit
// is 1 MiB, so the advice never grows with the size of the receipt.
const (
	// MaxAdviceLines is the most lines one advice carries.
	MaxAdviceLines = 1000
	// MaxAdviceSSCCs is the most SSCCs one advice lists, over all its lines.
	MaxAdviceSSCCs = 1000
	// MaxAdviceCartonFindings is the most carton findings one advice lists.
	MaxAdviceCartonFindings = 200
)

type adviceBounds struct {
	lines, sscc, findings int
}

var defaultAdviceBounds = adviceBounds{lines: MaxAdviceLines, sscc: MaxAdviceSSCCs, findings: MaxAdviceCartonFindings}

// AdviceDiscrepancy is one exception of an advice line: its kind, the number of
// units it concerns and its standard coding (ADR 0023). Quantity is
// expected - received for Short, received - expected for Over, the Damaged
// units, the units refused as the wrong product, or the held units.
type AdviceDiscrepancy struct {
	Kind     Kind
	Quantity int64
	Coding   Coding
}

// AdviceLine is one line of the receiving advice. Accepted + Rejected + Held =
// Received: accepted is the Good units not held, rejected the Damaged units.
// SSCCCount is exact; SSCCs is the bounded list and SSCCsTruncated says it was
// cut. GTIN is the each GTIN of a carton of the line, zero when none is known.
type AdviceLine struct {
	LineNo         int
	SKU            shared.SKU
	GTIN           gs1.GTIN
	Expected       int64
	Received       int64
	Accepted       int64
	Rejected       int64
	Held           int64
	SSCCCount      int
	SSCCs          []gs1.SSCC
	SSCCsTruncated bool
	Discrepancies  []AdviceDiscrepancy
	// EarliestBestBefore and EarliestExpiry are the earliest dates of the accepted
	// cartons of the line, "" when none had one (ADR 0024).
	EarliestBestBefore string
	EarliestExpiry     string
}

// ReceivingAdviceIssued is raised once, when a receipt closes (ADR 0023): the
// domain fact a renderer turns into a RECADV, an 861 or a 944. DespatchAdviceRef
// is the ASN number; SupplierRef and OwnerRef are the supplier and the depositor
// as the ASN knows them. LastCartonAt starts the SLA clock; WithinSLA says
// whether the advice (At) is on time. Lines, SSCCs and CartonFindings are
// bounded; the totals are exact.
type ReceivingAdviceIssued struct {
	Header
	DespatchAdviceRef   string
	SupplierRef         string
	OwnerRef            shared.OwnerRef
	LastCartonAt        time.Time
	SLA                 time.Duration
	WithinSLA           bool
	Lines               []AdviceLine
	LinesTotal          int
	LinesTruncated      bool
	CartonFindings      []CartonFinding
	CartonFindingsTotal int
}

// EventName returns "ReceivingAdviceIssued".
func (ReceivingAdviceIssued) EventName() string { return "ReceivingAdviceIssued" }

// ReceivingAdviceOverdue is raised once for an Open receipt whose last unit was
// received more than the SLA ago (ADR 0023): the advice cannot be on time any
// more unless the receipt is closed. DueAt is LastCartonAt + SLA.
type ReceivingAdviceOverdue struct {
	Header
	LastCartonAt time.Time
	DueAt        time.Time
	SLA          time.Duration
}

// EventName returns "ReceivingAdviceOverdue".
func (ReceivingAdviceOverdue) EventName() string { return "ReceivingAdviceOverdue" }

// AdviceOverdue reports the overdue event when the receipt is Open and now is
// strictly past its advice clock plus sla. It is pure: the caller's marker makes
// it once per receipt.
func (r *Receipt) AdviceOverdue(sla time.Duration, now time.Time) (ReceivingAdviceOverdue, bool) {
	if r.state != StateOpen {
		return ReceivingAdviceOverdue{}, false
	}
	start := r.AdviceClock()
	due := start.Add(sla)
	if !now.After(due) {
		return ReceivingAdviceOverdue{}, false
	}
	return ReceivingAdviceOverdue{Header: r.header(now), LastCartonAt: start, DueAt: due, SLA: sla}, true
}

// IssueAdvice builds the receiving advice of a Closed receipt. supplierRef comes
// from the ASN (the receipt does not keep it), at is the issue time and sla the
// committed window. It is pure and changes nothing: raising it twice is
// prevented by the caller's marker, not here.
func (r *Receipt) IssueAdvice(supplierRef string, sla time.Duration, at time.Time) (ReceivingAdviceIssued, error) {
	return r.issueAdvice(supplierRef, sla, at, defaultAdviceBounds)
}

func (r *Receipt) issueAdvice(supplierRef string, sla time.Duration, at time.Time, b adviceBounds) (ReceivingAdviceIssued, error) {
	if r.state != StateClosed {
		return ReceivingAdviceIssued{}, ErrReceiptNotClosed
	}
	clock := r.AdviceClock()
	incorrect := r.incorrectUnits()
	all := make([]AdviceLine, 0, len(r.lines))
	for _, l := range r.lines {
		all = append(all, r.adviceLine(l, incorrect[l.lineNo]))
	}
	lines := capAdviceLines(all, b.lines)
	budget := b.sscc
	for i := range lines {
		budget = r.listSSCCs(&lines[i], budget)
	}
	findings := make([]CartonFinding, 0, min(len(r.findings), b.findings))
	for i, f := range r.findings {
		if i < b.findings {
			findings = append(findings, f)
		}
	}
	return ReceivingAdviceIssued{
		Header:            r.header(at),
		DespatchAdviceRef: string(r.asnNumber), SupplierRef: supplierRef, OwnerRef: r.ownerRef,
		LastCartonAt: clock, SLA: sla, WithinSLA: !at.After(clock.Add(sla)),
		Lines: lines, LinesTotal: len(all), LinesTruncated: len(lines) < len(all),
		CartonFindings: findings, CartonFindingsTotal: len(r.findings),
	}, nil
}

// adviceLine is the quantities and the coded exceptions of one line.
func (r *Receipt) adviceLine(l Line, incorrect int64) AdviceLine {
	out := AdviceLine{
		LineNo: l.lineNo, SKU: l.sku, GTIN: r.lineGTIN(l.lineNo),
		Expected: l.expectedQty, Received: l.Received(),
		Accepted: l.receivedGood - l.held, Rejected: l.receivedDamaged, Held: l.held,
		Discrepancies: make([]AdviceDiscrepancy, 0, 5),
	}
	dates := r.LineDates(l.lineNo)
	out.EarliestBestBefore, out.EarliestExpiry = dates.BestBefore, dates.Expiry
	add := func(kind Kind, qty int64) {
		coding, _ := CodingOf(kind)
		out.Discrepancies = append(out.Discrepancies, AdviceDiscrepancy{Kind: kind, Quantity: qty, Coding: coding})
	}
	if out.Received < l.expectedQty {
		add(KindShort, l.expectedQty-out.Received)
	}
	if out.Received > l.expectedQty {
		add(KindOver, out.Received-l.expectedQty)
	}
	if l.receivedDamaged > 0 {
		add(KindDamaged, l.receivedDamaged)
	}
	if incorrect > 0 {
		add(KindIncorrectProduct, incorrect)
	}
	if l.held > 0 {
		add(KindHold, l.held)
	}
	return out
}

// incorrectUnits derives the IncorrectProduct quantity per line (ADR 0023): the
// manifest quantity of every carton that is still not received and that a scan
// refused for a wrong product (its label names another SKU, or a GTIN that does
// not resolve), counted once per carton however often it was scanned.
func (r *Receipt) incorrectUnits() map[int]int64 {
	refused := map[gs1.SSCC]bool{}
	for _, f := range r.findings {
		if (f.Kind == KindMixedCaseDeclaredAsCasePack || f.Kind == KindUnknownGtin) && !f.SSCC.IsZero() {
			refused[f.SSCC] = true
		}
	}
	out := map[int]int64{}
	for _, c := range r.cartons {
		if c.status != CartonExpected || c.sscc.IsZero() || !refused[c.sscc] {
			continue
		}
		for _, x := range c.contents {
			out[x.LineNo] += x.Qty
		}
	}
	return out
}

// lineGTIN is the first each GTIN the carton ledger knows for the line.
func (r *Receipt) lineGTIN(lineNo int) gs1.GTIN {
	for _, c := range r.cartons {
		for _, x := range c.contents {
			if x.LineNo == lineNo && !x.GTIN.IsZero() {
				return x.GTIN
			}
		}
	}
	return gs1.GTIN{}
}

// listSSCCs sets the SSCC count and the bounded list of a line from the
// accepted cartons that hold it, spending from budget (shared by all lines, in
// line order), and returns what is left of the budget.
func (r *Receipt) listSSCCs(l *AdviceLine, budget int) int {
	var all []gs1.SSCC
	for _, c := range r.cartons {
		if c.status != CartonAccepted || c.sscc.IsZero() {
			continue
		}
		for _, x := range c.contents {
			if x.LineNo == l.LineNo {
				all = append(all, c.sscc)
				break
			}
		}
	}
	l.SSCCCount = len(all)
	take := min(len(all), budget)
	if take > 0 {
		l.SSCCs = all[:take]
	}
	l.SSCCsTruncated = take < len(all)
	return budget - take
}

// capAdviceLines keeps at most limit lines, the ones with an exception first and
// then the others, always in line order; every line is kept when they fit.
func capAdviceLines(lines []AdviceLine, limit int) []AdviceLine {
	keep := make([]bool, len(lines))
	kept := 0
	for i, l := range lines {
		if len(l.Discrepancies) > 0 && kept < limit {
			keep[i] = true
			kept++
		}
	}
	for i := range lines {
		if !keep[i] && kept < limit {
			keep[i] = true
			kept++
		}
	}
	out := make([]AdviceLine, 0, kept)
	for i, l := range lines {
		if keep[i] {
			out = append(out, l)
		}
	}
	return out
}
