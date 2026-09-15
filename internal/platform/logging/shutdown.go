package logging

import (
	"context"
	"errors"
)

// IsShutdown reports whether an error is only the surrounding context being
// cancelled.
//
// Background loops all take the process context, so every one of them sees a
// cancellation when the proxy stops. Logging that at error level makes a clean
// shutdown look like a fault, and trains an operator to ignore the level that
// should mean something is wrong.
func IsShutdown(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
