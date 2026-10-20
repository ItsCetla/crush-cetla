package claudecode

import "time"

// SetSteerWait shortens how long a turn waits for a steered message.
func SetSteerWait(d time.Duration) (restore func()) {
	old := steerWait
	steerWait = d
	return func() { steerWait = old }
}
