package waku

// This file holds the //export'd callback. cgo forbids C *definitions* in the
// preamble of a file containing //export, so the preamble here is declarations
// only — the bridge functions live in waku.go.

/*
#include <stdlib.h>
*/
import "C"

import (
	"runtime/cgo"
	"sync/atomic"
	"unsafe"
)

// goFFICallback is invoked by liblogosdelivery. Two callers reach it:
//
//   - request-style calls, whose userData is a cgo.Handle wrapping chan result
//   - the single global event callback, whose userData wraps *eventSink
//
// The header requires this be "fast, non-blocking and potentially thread-safe".
// The invoking thread is not a Go thread, so cgo attaches it to the runtime on
// entry. Do no real work here: copy the payload out of C memory and hand off.
//
//export goFFICallback
func goFFICallback(ret C.int, msg *C.char, length C.size_t, userData unsafe.Pointer) {
	if userData == nil {
		return
	}

	// Copy out of C memory immediately — the buffer is not ours to keep.
	var s string
	if msg != nil && length > 0 {
		s = C.GoStringN(msg, C.int(length))
	}

	switch v := cgo.Handle(userData).Value().(type) {
	case chan result:
		// Buffered size 1, so this cannot block even if the caller has
		// already timed out and stopped reading.
		select {
		case v <- result{ret: int(ret), msg: s}:
		default:
		}

	case *eventSink:
		select {
		case v.node.events <- Event{Ret: int(ret), JSON: s}:
		default:
			// Never block the C event thread. Drop and count.
			atomic.AddUint64(&v.node.dropped, 1)
		}
	}
}
