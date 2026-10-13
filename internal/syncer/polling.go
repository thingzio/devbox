package syncer

import (
	"fmt"
	"strings"
	"time"
)

// Polling interval bounds and default (spec D5).
const (
	MinPollInterval     = time.Minute
	MaxPollInterval     = 15 * time.Minute
	DefaultPollInterval = 2 * time.Minute
)

// Polling is how often repositories are checked for changes. Manual checks
// only when Refresh is called.
type Polling struct {
	Manual   bool
	Interval time.Duration
}

// DefaultPolling applies when neither --poll-interval nor a saved setting chooses.
func DefaultPolling() Polling { return Polling{Interval: DefaultPollInterval} }

// ParsePolling accepts "manual" or a duration from 1m to 15m.
func ParsePolling(s string) (Polling, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "manual" {
		return Polling{Manual: true}, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return Polling{}, fmt.Errorf("poll interval %q: want manual or a duration such as 2m", s)
	}
	p := Polling{Interval: d}
	if err := p.Validate(); err != nil {
		return Polling{}, err
	}
	return p, nil
}

// Validate checks the interval bounds.
func (p Polling) Validate() error {
	if p.Manual || (p.Interval >= MinPollInterval && p.Interval <= MaxPollInterval) {
		return nil
	}
	return fmt.Errorf("poll interval %s must be between %s and %s, or manual",
		p.Interval, Polling{Interval: MinPollInterval}, Polling{Interval: MaxPollInterval})
}

// String renders "manual" or a compact duration such as "2m" or "1m30s".
func (p Polling) String() string {
	if p.Manual {
		return "manual"
	}
	if p.Interval%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(p.Interval/time.Minute))
	}
	return p.Interval.String()
}
