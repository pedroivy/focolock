package clock

import (
	"time"

	"golang.org/x/sys/unix"
)

func monotonic() time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_BOOTTIME, &ts); err != nil {
		return fallbackMono()
	}
	return time.Duration(ts.Nano())
}
