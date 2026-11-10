package repository

import "time"

// StowEvidence is what the two local copies of the dock-to-stock KPI hold
// about one receipt (ADR 0021): the gate arrival of its appointment (the zero
// time when none was seen or the receipt is a walk-in) and the stow time of
// each of its Good cartons by license plate (an LPN with no stow is absent).
type StowEvidence struct {
	ArrivedAt time.Time
	Stows     map[string]time.Time
}
