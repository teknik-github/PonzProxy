package domain

import "fmt"

// UsageAlert warns when a host's traffic passes a budget.
//
// It is about money rather than uptime. Hosting is billed by the byte, and the
// operator who wants this wants it while there is still a month left to act —
// not when the invoice arrives. That is also why it is a warning and not a
// critical: an alert that wakes someone for a cost is one they learn to mute,
// taking the outage alerts with it.
type UsageAlert struct {
	Enabled bool `json:"enabled"`
	// Bytes is the budget, counted in and out together, because that is
	// how transfer is usually billed.
	Bytes int64 `json:"bytes"`
	// PeriodDays is the rolling window it is measured over. Rolling rather
	// than calendar: ponzproxy does not know the billing day, and guessing
	// one would put the reset in the wrong place every month.
	PeriodDays int `json:"periodDays"`
}

// Usage alert bounds.
const (
	MinUsagePeriodDays = 1
	MaxUsagePeriodDays = 90
	MinUsageBytes      = 1 << 20 // 1 MiB; below this it is a typo
)

// DefaultUsageAlert is applied to new hosts: off, with a budget that is a
// round number rather than a guess at the operator's plan.
func DefaultUsageAlert() UsageAlert {
	return UsageAlert{Enabled: false, Bytes: 100 << 30, PeriodDays: 30}
}

func (u *UsageAlert) Normalize() {
	if u.PeriodDays == 0 {
		u.PeriodDays = 30
	}
	if u.Bytes < 0 {
		u.Bytes = 0
	}
}

func (u *UsageAlert) Validate() error {
	v := &ValidationError{}
	if u.PeriodDays < MinUsagePeriodDays || u.PeriodDays > MaxUsagePeriodDays {
		v.Add("usageAlert.periodDays", "must be between %d and %d",
			MinUsagePeriodDays, MaxUsagePeriodDays)
	}
	if u.Enabled && u.Bytes < MinUsageBytes {
		v.Add("usageAlert.bytes", "must be at least %d bytes", MinUsageBytes)
	}
	return v.Err()
}

// FormatBytes renders a byte count the way the console and the reports do, so
// an alert and the screen it came from do not disagree about the same figure.
func FormatBytes(n int64) string {
	units := []string{"B", "kB", "MB", "GB", "TB", "PB"}
	value := float64(n)
	unit := 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	if value < 10 && unit > 0 {
		return fmt.Sprintf("%.1f %s", value, units[unit])
	}
	return fmt.Sprintf("%.0f %s", value, units[unit])
}
