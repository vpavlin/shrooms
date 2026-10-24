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
//   - the event callback (legacy: one global; v0.40: one listener per event),
//     whose userData wraps *eventSink
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

	switch v := cgo.Handle(uintptr(userData)).Value().(type) {
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

// goReplyCallback is v0.40's reply to a request-style call, through
// reply_tramp in binding_ld040.go: the reply on success, errMsg on failure,
// each NULL when absent. Both are borrowed, so they are copied here.
//
//export goReplyCallback
func goReplyCallback(ret C.int, reply *C.char, errMsg *C.char, userData unsafe.Pointer) {
	if userData == nil {
		return
	}
	s := reply
	if ret != 0 {
		s = errMsg
	}
	var msg string
	if s != nil {
		msg = C.GoString(s)
	}
	if ch, ok := cgo.Handle(uintptr(userData)).Value().(chan result); ok {
		select {
		case ch <- result{ret: int(ret), msg: msg}:
		default:
		}
	}
}

// goCreateCallback is v0.40's constructor answer: the context on success,
// errMsg otherwise.
//
//export goCreateCallback
func goCreateCallback(ret C.int, ctx unsafe.Pointer, errMsg *C.char, userData unsafe.Pointer) {
	if userData == nil {
		return
	}
	var msg string
	if errMsg != nil {
		msg = C.GoString(errMsg)
	}
	if ch, ok := cgo.Handle(uintptr(userData)).Value().(chan createResult); ok {
		select {
		case ch <- createResult{ret: int(ret), ctx: ctx, msg: msg}:
		default:
		}
	}
}
