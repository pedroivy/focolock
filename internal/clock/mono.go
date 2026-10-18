package clock

import "time"

var processStart = time.Now()

// fallbackMono usa o monotônico do Go (não conta o tempo dormindo).
func fallbackMono() time.Duration { return time.Since(processStart) }
