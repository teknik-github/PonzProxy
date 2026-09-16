package domain

import "strings"

// Mode decides what a per-host protection does when a request matches it.
//
// Detect exists because on a reverse proxy a false positive is an outage: a
// legitimate request refused is visible to a customer, while a probe or an
// over-eager client that slips through usually is not. An operator should be
// able to watch what *would* be refused for a week before enforcing anything.
//
// The guardian and the traffic limiter share this vocabulary because they
// share that reasoning, and because an operator should not have to learn two
// meanings of the word "detect".
type Mode string

const (
	// ModeOff does no checking at all.
	ModeOff Mode = "off"
	// ModeDetect records what would have been refused and lets it through.
	ModeDetect Mode = "detect"
	// ModeBlock refuses.
	ModeBlock Mode = "block"
)

func (m Mode) Valid() bool {
	switch m {
	case ModeOff, ModeDetect, ModeBlock:
		return true
	}
	return false
}

// Enabled reports whether any checking happens, in either mode.
func (m Mode) Enabled() bool { return m == ModeDetect || m == ModeBlock }

// NormalizeMode canonicalises operator input, defaulting to off. A protection
// that cannot parse its own setting must not guess its way into enforcing.
func NormalizeMode(m Mode) Mode {
	m = Mode(strings.ToLower(strings.TrimSpace(string(m))))
	if m == "" {
		return ModeOff
	}
	return m
}
