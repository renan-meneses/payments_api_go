// Package clock isolates time acquisition so use cases are deterministically
// testable (inject a fake clock in tests instead of sleeping).
package clock

import "time"

type Clock interface {
	Now() time.Time
}

// System is the production clock. It always returns UTC to keep persistence and
// event timestamps unambiguous.
type System struct{}

func (System) Now() time.Time { return time.Now().UTC() }

// Fixed is a test clock.
type Fixed struct{ T time.Time }

func NewFixed(t time.Time) *Fixed { return &Fixed{T: t} }

func (f *Fixed) Now() time.Time          { return f.T }
func (f *Fixed) Advance(d time.Duration) { f.T = f.T.Add(d) }
