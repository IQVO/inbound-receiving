// Package clock is the system-clock adapter of ports.Clock.
package clock

import "time"

// System returns the wall clock in UTC.
type System struct{}

// Now implements ports.Clock.
func (System) Now() time.Time { return time.Now().UTC() }
