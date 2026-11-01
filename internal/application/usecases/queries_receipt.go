package usecases

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
	"github.com/claudioed/inbound-receiving/internal/domain/receipt"
)

// GetReceipt returns one receipt.
type GetReceipt struct {
	Receipts ports.ReceiptRepository
}

// Handle validates the id and loads the receipt.
func (uc *GetReceipt) Handle(ctx context.Context, rawID string) (*receipt.Receipt, error) {
	id, err := receipt.NewID(rawID)
	if err != nil {
		return nil, err
	}
	return uc.Receipts.Get(ctx, id)
}

// ListReceiptsQuery is the input of ListReceipts. AsnNumber and State ""
// mean no filter.
type ListReceiptsQuery struct {
	Limit     int
	Cursor    string
	AsnNumber string
	State     string
}

// ReceiptPage is one page of receipts in ascending id order. NextCursor is
// empty on the last page.
type ReceiptPage struct {
	Items      []*receipt.Receipt
	NextCursor string
}

// ListReceipts lists receipts a page at a time (cursor = base64url of the
// last id of the previous page).
type ListReceipts struct {
	Receipts ports.ReceiptRepository
}

// Handle runs the query.
func (uc *ListReceipts) Handle(ctx context.Context, q ListReceiptsQuery) (ReceiptPage, error) {
	limit, err := listLimit(q.Limit)
	if err != nil {
		return ReceiptPage{}, err
	}
	var after receipt.ID
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return ReceiptPage{}, invalidCursor()
		}
		if after, err = receipt.NewID(string(raw)); err != nil {
			return ReceiptPage{}, invalidCursor()
		}
	}
	var filter repository.ReceiptFilter
	if q.AsnNumber != "" {
		if filter.AsnNumber, err = asn.NewNumber(q.AsnNumber); err != nil {
			return ReceiptPage{}, invalidQuery("asnNumber", err)
		}
	}
	if q.State != "" {
		state := receipt.State(q.State)
		if state != receipt.StateOpen && state != receipt.StateClosed {
			return ReceiptPage{}, fmt.Errorf("%w: state must be Open or Closed", ErrInvalidListQuery)
		}
		filter.State = state
	}
	items, err := uc.Receipts.List(ctx, filter, after, limit+1)
	if err != nil {
		return ReceiptPage{}, err
	}
	page := ReceiptPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Items[limit-1].ID()))
	}
	return page, nil
}
