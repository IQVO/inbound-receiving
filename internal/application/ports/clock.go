package ports

import "time"

// Clock is the service's notion of "now". Use cases stamp events with it
// and check-in, booking and receiving use it for their timestamps.
type Clock interface {
	Now() time.Time
}
