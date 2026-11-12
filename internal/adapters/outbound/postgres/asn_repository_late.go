package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/shared/gs1"
)

// The SSCC lookup of RegisterAsn and the exactly-once marker of the ASN's
// AsnLateForArrival (ADR 0025), split from asn_repository.go to keep every
// file inside the fleet's 300-code-line budget.

// ExistingSsccs implements ports.AsnRepository: the given SSCCs found as a
// carton or a pallet SSCC on an ASN that is not cancelled.
func (r *AsnRepo) ExistingSsccs(ctx context.Context, ssccs []gs1.SSCC) ([]gs1.SSCC, error) {
	raw := make([]string, 0, len(ssccs))
	for _, s := range ssccs {
		raw = append(raw, s.String())
	}
	rows, err := queryFor(ctx, r.pool).Query(ctx, `
		SELECT sscc FROM asn_cartons WHERE active AND sscc = ANY($1)
		UNION
		SELECT parent_sscc FROM asn_cartons WHERE active AND parent_sscc = ANY($1)
		ORDER BY 1`, raw)
	if err != nil {
		return nil, fmt.Errorf("existing ssccs: %w", err)
	}
	defer rows.Close()
	return scanSsccs(rows)
}

func scanSsccs(rows pgx.Rows) ([]gs1.SSCC, error) {
	var out []gs1.SSCC
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan sscc: %w", err)
		}
		s, err := gs1.ParseSSCC(raw)
		if err != nil {
			return nil, fmt.Errorf("stored sscc %q: %w", raw, err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan ssccs: %w", err)
	}
	return out, nil
}

// MarkLateArrival implements ports.AsnRepository (ADR 0025): the
// asn_late_arrivals marker, ON CONFLICT DO NOTHING, so only the inserter
// raises the event. Inside a unit of work it joins the transaction.
func (r *AsnRepo) MarkLateArrival(ctx context.Context, number asn.Number, appointmentID, visitID string, raisedAt time.Time) (bool, error) {
	tag, err := queryFor(ctx, r.pool).Exec(ctx, `INSERT INTO asn_late_arrivals (asn_number, appointment_id, visit_id, raised_at)
		VALUES ($1, $2, $3, $4) ON CONFLICT (asn_number) DO NOTHING`,
		string(number), appointmentID, visitID, raisedAt.UTC())
	if err != nil {
		return false, fmt.Errorf("mark asn late %s: %w", number, err)
	}
	return tag.RowsAffected() == 1, nil
}
