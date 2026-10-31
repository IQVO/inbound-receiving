package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// cartonContentJSON is the stored shape of one carton content in
// receipt_cartons.contents.
type cartonContentJSON struct {
	LineNo    int    `json:"line_no"`
	SKU       string `json:"sku"`
	GTIN      string `json:"gtin"`
	Qty       int64  `json:"qty"`
	Lot       string `json:"lot,omitempty"`
	Expiry    string `json:"expiry,omitempty"`
	Condition string `json:"condition,omitempty"`
}

// loadReceiptCartons reads the carton ledgers of the given receipts, in
// ledger order.
func loadReceiptCartons(ctx context.Context, q querier, ids []string) (map[string][]receipt.CartonState, error) {
	rows, err := q.Query(ctx, `SELECT receipt_id, lpn, sscc, parent_sscc, kind, status, sealed, contents, received_at
		FROM receipt_cartons WHERE receipt_id = ANY($1) ORDER BY receipt_id, position`, ids)
	if err != nil {
		return nil, fmt.Errorf("load receipt cartons: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]receipt.CartonState, len(ids))
	for rows.Next() {
		var id string
		var sscc, parent *string
		var raw []byte
		var receivedAt *time.Time
		var c receipt.CartonState
		if err := rows.Scan(&id, &c.LPN, &sscc, &parent, &c.Kind, &c.Status, &c.Sealed, &raw, &receivedAt); err != nil {
			return nil, fmt.Errorf("scan receipt carton: %w", err)
		}
		c.SSCC, c.ParentSSCC = deref(sscc), deref(parent)
		if receivedAt != nil {
			c.ReceivedAt = receivedAt.UTC()
		}
		var contents []cartonContentJSON
		if err := json.Unmarshal(raw, &contents); err != nil {
			return nil, fmt.Errorf("receipt carton %s contents: %w", c.LPN, err)
		}
		for _, x := range contents {
			c.Contents = append(c.Contents, receipt.CartonContentState{
				LineNo: x.LineNo, SKU: x.SKU, GTIN: x.GTIN, Qty: x.Qty, Lot: x.Lot, Expiry: x.Expiry, Condition: x.Condition,
			})
		}
		out[id] = append(out[id], c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load receipt cartons: %w", err)
	}
	return out, nil
}

// upsertReceiptCartons writes the whole carton ledger of x in ONE statement:
// new entries are inserted, a changed one (Expected to Accepted) is updated
// and an unchanged one is left alone.
func upsertReceiptCartons(ctx context.Context, q querier, x *receipt.Receipt) error {
	cartons := x.Persisted().Cartons
	if len(cartons) == 0 {
		return nil
	}
	n := len(cartons)
	positions, lpns, ssccs, parents := make([]int32, n), make([]string, n), make([]string, n), make([]string, n)
	kinds, statuses, contents, receivedAt := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	sealed := make([]bool, n)
	for i, c := range cartons {
		raw, err := json.Marshal(contentsJSON(c.Contents))
		if err != nil {
			return fmt.Errorf("marshal carton %s contents: %w", c.LPN, err)
		}
		positions[i], lpns[i], ssccs[i], parents[i] = int32(i+1), c.LPN, c.SSCC, c.ParentSSCC
		kinds[i], statuses[i], contents[i], sealed[i] = c.Kind, string(c.Status), string(raw), c.Sealed
		if !c.ReceivedAt.IsZero() {
			receivedAt[i] = c.ReceivedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	_, err := q.Exec(ctx, `
		INSERT INTO receipt_cartons (receipt_id, position, lpn, sscc, parent_sscc, kind, status, sealed, contents, received_at)
		SELECT $1, t.position, t.lpn, NULLIF(t.sscc, ''), NULLIF(t.parent_sscc, ''), t.kind, t.status, t.sealed, t.contents::jsonb,
		       NULLIF(t.received_at, '')::timestamptz
		FROM unnest($2::int[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::boolean[], $9::text[], $10::text[])
		     AS t(position, lpn, sscc, parent_sscc, kind, status, sealed, contents, received_at)
		ON CONFLICT (receipt_id, position) DO UPDATE SET
		    status = EXCLUDED.status, sealed = EXCLUDED.sealed, contents = EXCLUDED.contents, received_at = EXCLUDED.received_at
		WHERE receipt_cartons.status IS DISTINCT FROM EXCLUDED.status`,
		string(x.ID()), positions, lpns, ssccs, parents, kinds, statuses, sealed, contents, receivedAt)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == acceptedSsccIndex {
		// Another receipt accepted the same SSCC between the use case's check
		// and this write: the retry sees the duplicate and raises it.
		return repository.ErrConcurrentModification
	}
	if err != nil {
		return fmt.Errorf("upsert cartons of receipt %s: %w", x.ID(), err)
	}
	return nil
}

// acceptedSsccIndex is the partial unique index that makes "an SSCC is
// accepted once per site" a database fact.
const acceptedSsccIndex = "receipt_cartons_accepted_sscc"

func contentsJSON(in []receipt.CartonContentState) []cartonContentJSON {
	out := make([]cartonContentJSON, 0, len(in))
	for _, c := range in {
		out = append(out, cartonContentJSON{LineNo: c.LineNo, SKU: c.SKU, GTIN: c.GTIN, Qty: c.Qty, Lot: c.Lot, Expiry: c.Expiry, Condition: c.Condition})
	}
	return out
}
