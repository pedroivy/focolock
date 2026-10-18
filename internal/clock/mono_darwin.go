package clock

import (
	"time"

	"golang.org/x/sys/unix"
)

// No macOS, CLOCK_MONOTONIC conta o tempo em que o Mac está dormindo
// (mach_continuous_time), então acordar não parece "mudança de hora".
func monotonic() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return fallbackMono()
	}
	return time.Duration(ts.Nano())
}
