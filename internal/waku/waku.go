// Package waku is a cgo binding over liblogosdelivery (Logos Delivery / Waku).
//
// Two bindings, one API, chosen at build time (ADR-071):
//   - binding_legacy.go, the default: the July 2026 pin (7a3a064b) and the
//     prebuilt Android library, with one global event callback and a struct
//     ABI.
//   - binding_ld040.go, with -tags ld040: v0.40, whose calls go through
//     nim-ffi 0.3's typed helpers (CBOR underneath) and whose events come
//     through per-event listeners.
//
// Both take the same flat config, by WakuNodeConf field names. The legacy one
// goes when every node, phones included, runs v0.40.
//
// Supply the library and header via CGO_CFLAGS / CGO_LDFLAGS; see the Makefile.
package waku

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unsafe"
)

// callTimeout bounds how long we wait for an FFI callback. Request callbacks
// appear to fire synchronously, but nothing in the header promises that.
const callTimeout = 30 * time.Second

// eventBuffer is the depth of the event channel. The header requires the
// callback be "fast, non-blocking and potentially thread-safe", so we never
// block it — overflow is dropped and counted.
const eventBuffer = 256

// result is what an FFI callback delivers for a request-style call.
type result struct {
	ret int
	msg string
}

// createResult is what v0.40's constructor callback delivers.
type createResult struct {
	ret int
	ctx unsafe.Pointer
	msg string
}

// Event is an asynchronous notification. The packaged build has one global
// callback, so the event type is inside JSON rather than known from
// registration.
type Event struct {
	Ret  int
	JSON string
}

// Config is a flat node configuration using WakuNodeConf field names,
// e.g. {"mode":"Core","clusterId":2,"relay":true}.
type Config map[string]any

// eventSink is the handle target for the global event callback.
type eventSink struct{ node *Node }

// peerIDText is the peer id as libp2p writes it. The library built from
// source at the pin answers with the PeerId object's Nim repr,
// "(data: @[0, 37, 8, 2, …])", where Basecamp's build answered "16Uiu2…": the
// bytes are the multihash, and the text is their base58 (2026-10-20, found
// when the two-node test got an empty id on deps-v2; pi5's arm64 build had
// done it since 2026-09-18). Either form comes back as the text.
func peerIDText(s string) (string, error) {
	s = strings.TrimSpace(s)
	const pre = "(data: @["
	if !strings.HasPrefix(s, pre) {
		return s, nil
	}
	body, ok := strings.CutSuffix(strings.TrimPrefix(s, pre), "])")
	if !ok {
		return "", fmt.Errorf("unreadable peer id %.60q", s)
	}
	var b []byte
	for _, f := range strings.Split(body, ",") {
		v, err := strconv.ParseUint(strings.TrimSpace(f), 10, 8)
		if err != nil {
			return "", fmt.Errorf("unreadable peer id %.60q", s)
		}
		b = append(b, byte(v))
	}
	return base58(b), nil
}

// base58 is Bitcoin's alphabet, the one libp2p peer ids use.
func base58(b []byte) string {
	const alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	n := new(big.Int).SetBytes(b)
	var out []byte
	mod, base := new(big.Int), big.NewInt(58)
	for n.Sign() > 0 {
		n.DivMod(n, base, mod)
		out = append(out, alphabet[mod.Int64()])
	}
	for _, c := range b {
		if c != 0 {
			break
		}
		out = append(out, '1')
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// Events returns the channel asynchronous events are delivered on.
func (n *Node) Events() <-chan Event { return n.events }

// Dropped reports how many events were discarded because the buffer was full.
func (n *Node) Dropped() uint64 { return atomic.LoadUint64(&n.dropped) }
