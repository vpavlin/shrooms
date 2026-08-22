package main

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// What this process is costing the machine it was lent.
//
// Worth reporting because a relay is something a stranger agreed to host, and
// the reasonable first question about a container somebody else wrote is what it
// is going to do to their box. A relay should be boring — a few megabytes and
// almost no processor — and the way to make that claim credible is to publish it
// continuously rather than assert it in a README.
//
// It is also the number that catches a leak. A forwarding table is bounded and
// registrations expire, so memory should sit flat forever; a figure that climbs
// across a day means something is not being released, and nothing else here
// would reveal it.

// usage is one sample of what the process is using.
type usage struct {
	// RSS is resident memory, which is what a container limit is enforced
	// against — not Go's heap, which omits stacks and the runtime itself and so
	// reads lower than the number that gets a process killed.
	RSS uint64
	// CPU is total processor time consumed since start.
	CPU time.Duration
	// Goroutines catches the other leak worth catching: one per connection or
	// per timer that is never released shows up here long before it shows up in
	// memory.
	Goroutines int
}

// sample reads current usage. Linux-only in practice, which is where this runs;
// elsewhere the memory and CPU figures are simply absent rather than wrong.
func sample() usage {
	u := usage{Goroutines: runtime.NumGoroutine()}
	pageSize := uint64(os.Getpagesize())

	// /proc/self/statm: size, resident, shared, ... in pages.
	if b, err := os.ReadFile("/proc/self/statm"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 2 {
			if n, err := strconv.ParseUint(f[1], 10, 64); err == nil {
				u.RSS = n * pageSize
			}
		}
	}

	// /proc/self/stat fields 14 and 15 are utime and stime, in clock ticks.
	//
	// The tick rate is a compile-time constant of the kernel and is 100 on
	// every platform this will meet. Reading it properly needs cgo, which this
	// binary deliberately does not have — being pure Go is what lets it ship on
	// scratch — so it is assumed, and the cost of being wrong is a CPU figure
	// off by a constant factor rather than anything breaking.
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		// The second field is the executable name in parentheses and may itself
		// contain spaces, so fields are counted from after the closing one.
		if i := strings.LastIndexByte(string(b), ')'); i >= 0 {
			f := strings.Fields(string(b)[i+1:])
			// After ')' the next field is state, so utime is index 11.
			if len(f) > 12 {
				ut, err1 := strconv.ParseInt(f[11], 10, 64)
				st, err2 := strconv.ParseInt(f[12], 10, 64)
				if err1 == nil && err2 == nil {
					u.CPU = time.Duration(ut+st) * 10 * time.Millisecond
				}
			}
		}
	}
	return u
}

// percent is processor use across a window, as a percentage of one core.
func (u usage) percent(prev usage, window time.Duration) float64 {
	if window <= 0 || u.CPU == 0 {
		return 0
	}
	return float64(u.CPU-prev.CPU) / float64(window) * 100
}
