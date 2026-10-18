//go:build !darwin && !linux

package clock

import "time"

func monotonic() time.Duration { return fallbackMono() }
