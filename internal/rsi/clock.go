package rsi

import "golang.org/x/sys/unix"

// MonotonicNS shares the kernel's CLOCK_MONOTONIC domain, including time spent
// in admission and device queues. It is independent of wall-clock adjustments.
func MonotonicNS() uint64 {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		panic(err)
	}
	return uint64(ts.Nano())
}
