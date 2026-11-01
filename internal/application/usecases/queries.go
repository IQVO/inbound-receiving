package usecases

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/claudioed/inbound-receiving/internal/application/ports"
	"github.com/claudioed/inbound-receiving/internal/application/repository"
	"github.com/claudioed/inbound-receiving/internal/domain/appointment"
	"github.com/claudioed/inbound-receiving/internal/domain/asn"
)

// List page-size bounds (apis/openapi.yaml).
const (
	DefaultListLimit = 100
	MaxListLimit     = 500
)

func listLimit(limit int) (int, error) {
	if limit == 0 {
		return DefaultListLimit, nil
	}
	if limit < 1 || limit > MaxListLimit {
		return 0, fmt.Errorf("%w: limit must be 1..%d", ErrInvalidListQuery, MaxListLimit)
	}
	return limit, nil
}

func invalidQuery(field string, err error) error {
	return fmt.Errorf("%w: %s: %w", ErrInvalidListQuery, field, err)
}

func invalidCursor() error {
	return fmt.Errorf("%w: cursor is not a valid cursor", ErrInvalidListQuery)
}

// GetAsn returns one ASN.
type GetAsn struct {
	Asns ports.AsnRepository
}

// Handle validates the number and loads the ASN
// (repository.ErrAsnNotFound when unknown).
func (uc *GetAsn) Handle(ctx context.Context, rawNumber string) (*asn.Asn, error) {
	number, err := asn.NewNumber(rawNumber)
	if err != nil {
		return nil, err
	}
	return uc.Asns.Get(ctx, number)
}

// ListAsnsQuery is the input of ListAsns. Limit 0 means the default; State
// "" means no filter.
type ListAsnsQuery struct {
	Limit  int
	Cursor string
	State  string
}

// AsnPage is one page of ASNs in ascending number order. NextCursor is empty
// on the last page.
type AsnPage struct {
	Items      []*asn.Asn
	NextCursor string
}

// ListAsns lists ASNs a page at a time (cursor = base64url of the last
// number of the previous page).
type ListAsns struct {
	Asns ports.AsnRepository
}

// Handle runs the query.
func (uc *ListAsns) Handle(ctx context.Context, q ListAsnsQuery) (AsnPage, error) {
	limit, err := listLimit(q.Limit)
	if err != nil {
		return AsnPage{}, err
	}
	var after asn.Number
	if q.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil {
			return AsnPage{}, invalidCursor()
		}
		if after, err = asn.NewNumber(string(raw)); err != nil {
			return AsnPage{}, invalidCursor()
		}
	}
	var filter repository.AsnFilter
	if q.State != "" {
		state := asn.State(q.State)
		if state != asn.Registered && state != asn.Receiving && state != asn.Closed && state != asn.Cancelled {
			return AsnPage{}, fmt.Errorf("%w: state must be Registered, Receiving, Closed or Cancelled", ErrInvalidListQuery)
		}
		filter.State = state
	}
	items, err := uc.Asns.List(ctx, filter, after, limit+1)
	if err != nil {
		return AsnPage{}, err
	}
	page := AsnPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.Items[limit-1].Number()))
	}
	return page, nil
}

// GetAppointment returns one appointment.
type GetAppointment struct {
	Appointments ports.AppointmentRepository
}

// Handle validates the id and loads the appointment.
func (uc *GetAppointment) Handle(ctx context.Context, rawID string) (*appointment.DockAppointment, error) {
	id, err := appointment.NewID(rawID)
	if err != nil {
		return nil, err
	}
	return uc.Appointments.Get(ctx, id)
}

// ListAppointmentsQuery is the input of ListAppointments. Door and State ""
// and zero From/To mean no filter.
type ListAppointmentsQuery struct {
	Limit  int
	Cursor string
	Door   string
	State  string
	From   time.Time
	To     time.Time
}

// AppointmentPage is one page of appointments in ascending (window start,
// id) order. NextCursor is empty on the last page.
type AppointmentPage struct {
	Items      []*appointment.DockAppointment
	NextCursor string
}

// ListAppointments lists appointments a page at a time.
type ListAppointments struct {
	Appointments ports.AppointmentRepository
}

// Handle runs the query.
func (uc *ListAppointments) Handle(ctx context.Context, q ListAppointmentsQuery) (AppointmentPage, error) {
	limit, err := listLimit(q.Limit)
	if err != nil {
		return AppointmentPage{}, err
	}
	after, err := decodeAppointmentCursor(q.Cursor)
	if err != nil {
		return AppointmentPage{}, err
	}
	filter := repository.AppointmentFilter{From: q.From, To: q.To}
	if q.Door != "" {
		if filter.Door, err = appointment.NewDoorCode(q.Door); err != nil {
			return AppointmentPage{}, invalidQuery("door", err)
		}
	}
	if q.State != "" {
		state := appointment.State(q.State)
		if state != appointment.Booked && state != appointment.CheckedIn && state != appointment.Completed && state != appointment.Cancelled {
			return AppointmentPage{}, fmt.Errorf("%w: state must be Booked, CheckedIn, Completed or Cancelled", ErrInvalidListQuery)
		}
		filter.State = state
	}
	items, err := uc.Appointments.List(ctx, filter, after, limit+1)
	if err != nil {
		return AppointmentPage{}, err
	}
	page := AppointmentPage{Items: items}
	if len(items) > limit {
		page.Items = items[:limit]
		page.NextCursor = encodeAppointmentCursor(page.Items[limit-1])
	}
	return page, nil
}

func encodeAppointmentCursor(d *appointment.DockAppointment) string {
	raw := d.Window().Start().Format(time.RFC3339Nano) + "|" + string(d.ID())
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeAppointmentCursor(cursor string) (repository.AppointmentCursor, error) {
	if cursor == "" {
		return repository.AppointmentCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return repository.AppointmentCursor{}, invalidCursor()
	}
	start, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return repository.AppointmentCursor{}, invalidCursor()
	}
	at, err := time.Parse(time.RFC3339Nano, start)
	if err != nil {
		return repository.AppointmentCursor{}, invalidCursor()
	}
	apptID, err := appointment.NewID(id)
	if err != nil {
		return repository.AppointmentCursor{}, invalidCursor()
	}
	return repository.AppointmentCursor{WindowStart: at.UTC(), ID: apptID}, nil
}

// DockList is the result of ListDocks.
type DockList struct {
	Mode  Mode
	Items []repository.DockDoor
}

// ListDocks returns the dock_doors local copy together with the current
// DOCK_DOOR_MODE.
type ListDocks struct {
	Doors ports.DockDoorDirectory
	Mode  Mode
}

// Handle runs the query. Items is never nil.
func (uc *ListDocks) Handle(ctx context.Context) (DockList, error) {
	items, err := uc.Doors.List(ctx)
	if err != nil {
		return DockList{}, err
	}
	if items == nil {
		items = []repository.DockDoor{}
	}
	return DockList{Mode: uc.Mode, Items: items}, nil
}
