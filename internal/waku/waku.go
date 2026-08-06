// Package waku is a cgo binding over liblogosdelivery (Logos Delivery / Waku).
//
// Targets the packaged liblogosdelivery shipped with Logos Basecamp, whose API
// differs from logos-delivery master:
//   - a single global event callback (logosdelivery_set_event_callback) rather
//     than per-event listeners (add_event_listener/remove_event_listener)
//   - a flat config JSON using WakuNodeConf field names rather than a nested
//     {mode, preset, messagingOverrides} object
//
// Supply the library and header via CGO_CFLAGS / CGO_LDFLAGS; see the Makefile.
package waku

/*
#include <stdlib.h>
#include "liblogosdelivery.h"

// Defined in callback.go via //export.
extern void goFFICallback(int ret, char *msg, size_t len, void *userData);

// Thin bridges so Go never casts a Go symbol to a C function pointer.
static void *bridge_create_node(const char *cfg, void *ud) {
    return logosdelivery_create_node(cfg, (FFICallBack)goFFICallback, ud);
}
static int bridge_start(void *ctx, void *ud) {
    return logosdelivery_start_node(ctx, (FFICallBack)goFFICallback, ud);
}
static int bridge_stop(void *ctx, void *ud) {
    return logosdelivery_stop_node(ctx, (FFICallBack)goFFICallback, ud);
}
static int bridge_destroy(void *ctx, void *ud) {
    return logosdelivery_destroy(ctx, (FFICallBack)goFFICallback, ud);
}
static int bridge_subscribe(void *ctx, void *ud, const char *topic) {
    return logosdelivery_subscribe(ctx, (FFICallBack)goFFICallback, ud, topic);
}
static int bridge_unsubscribe(void *ctx, void *ud, const char *topic) {
    return logosdelivery_unsubscribe(ctx, (FFICallBack)goFFICallback, ud, topic);
}
static int bridge_send(void *ctx, void *ud, const char *msgJSON) {
    return logosdelivery_send(ctx, (FFICallBack)goFFICallback, ud, msgJSON);
}
static int bridge_node_info(void *ctx, void *ud, const char *id) {
    return logosdelivery_get_node_info(ctx, (FFICallBack)goFFICallback, ud, id);
}
static int bridge_node_info_ids(void *ctx, void *ud) {
    return logosdelivery_get_available_node_info_ids(ctx, (FFICallBack)goFFICallback, ud);
}
static int bridge_available_configs(void *ctx, void *ud) {
    return logosdelivery_get_available_configs(ctx, (FFICallBack)goFFICallback, ud);
}
static void bridge_set_event_callback(void *ctx, void *ud) {
    logosdelivery_set_event_callback(ctx, (FFICallBack)goFFICallback, ud);
}

// Kernel API. Declared here rather than included: the packaged build exports
// these symbols but ships no matching kernel header alongside the .so.
extern int waku_pubsub_topic(void *ctx, FFICallBack callback, void *userData, const char *topicName);
extern int waku_relay_get_num_peers_in_mesh(void *ctx, FFICallBack callback, void *userData, const char *pubsubTopic);
extern int waku_get_my_peerid(void *ctx, FFICallBack callback, void *userData);

static int bridge_pubsub_topic(void *ctx, void *ud, const char *topicName) {
    return waku_pubsub_topic(ctx, (FFICallBack)goFFICallback, ud, topicName);
}
static int bridge_peers_in_mesh(void *ctx, void *ud, const char *pubsubTopic) {
    return waku_relay_get_num_peers_in_mesh(ctx, (FFICallBack)goFFICallback, ud, pubsubTopic);
}
static int bridge_my_peerid(void *ctx, void *ud) {
    return waku_get_my_peerid(ctx, (FFICallBack)goFFICallback, ud);
}
*/
import "C"

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/cgo"
	"sync"
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

// Node is a Logos Delivery node.
type Node struct {
	ctx unsafe.Pointer

	events  chan Event
	dropped uint64

	mu       sync.Mutex
	evHandle *cgo.Handle // event-callback handle, freed on Close
	closed   bool
}

// eventSink is the handle target for the global event callback.
type eventSink struct{ node *Node }

// call performs a request-style FFI call and waits for its callback.
func call(fn func(ud unsafe.Pointer) C.int) (string, error) {
	ch := make(chan result, 1)
	h := cgo.NewHandle(ch)
	defer h.Delete()

	rc := fn(unsafe.Pointer(h))

	select {
	case r := <-ch:
		if int(rc) != 0 || r.ret != 0 {
			return r.msg, fmt.Errorf("ffi call failed (rc=%d ret=%d): %s", int(rc), r.ret, r.msg)
		}
		return r.msg, nil
	case <-time.After(callTimeout):
		return "", errors.New("ffi callback timed out")
	}
}

// New creates a node. It is not started yet.
func New(cfg Config) (*Node, error) {
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}

	cCfg := C.CString(string(raw))
	defer C.free(unsafe.Pointer(cCfg))

	ch := make(chan result, 1)
	h := cgo.NewHandle(ch)
	defer h.Delete()

	ctx := C.bridge_create_node(cCfg, unsafe.Pointer(h))
	if ctx == nil {
		select {
		case r := <-ch:
			return nil, fmt.Errorf("create_node returned NULL: %s", r.msg)
		default:
			return nil, errors.New("create_node returned NULL")
		}
	}

	n := &Node{
		ctx:    unsafe.Pointer(ctx),
		events: make(chan Event, eventBuffer),
	}

	// Register the single global event callback before start, so nothing is
	// missed. The handle is held for the node's lifetime.
	eh := cgo.NewHandle(&eventSink{node: n})
	n.evHandle = &eh
	C.bridge_set_event_callback(n.ctx, unsafe.Pointer(eh))

	return n, nil
}

// Start starts the node.
func (n *Node) Start() error {
	_, err := call(func(ud unsafe.Pointer) C.int { return C.bridge_start(n.ctx, ud) })
	return err
}

// Stop stops the node without destroying it.
func (n *Node) Stop() error {
	_, err := call(func(ud unsafe.Pointer) C.int { return C.bridge_stop(n.ctx, ud) })
	return err
}

// Close stops and destroys the node and releases its handles.
func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		return nil
	}
	n.closed = true
	eh := n.evHandle
	n.evHandle = nil
	n.mu.Unlock()

	_, err := call(func(ud unsafe.Pointer) C.int { return C.bridge_destroy(n.ctx, ud) })

	// Only safe once the C side can no longer invoke the callback.
	if eh != nil {
		eh.Delete()
	}
	return err
}

// Subscribe subscribes to a content topic.
func (n *Node) Subscribe(contentTopic string) error {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	_, err := call(func(ud unsafe.Pointer) C.int { return C.bridge_subscribe(n.ctx, ud, ct) })
	return err
}

// Unsubscribe unsubscribes from a content topic.
func (n *Node) Unsubscribe(contentTopic string) error {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	_, err := call(func(ud unsafe.Pointer) C.int { return C.bridge_unsubscribe(n.ctx, ud, ct) })
	return err
}

// Send publishes payload to contentTopic. The C layer expects base64.
func (n *Node) Send(contentTopic string, payload []byte, ephemeral bool) (string, error) {
	msg := struct {
		ContentTopic string `json:"contentTopic"`
		Payload      string `json:"payload"`
		Ephemeral    bool   `json:"ephemeral"`
	}{
		ContentTopic: contentTopic,
		Payload:      base64.StdEncoding.EncodeToString(payload),
		Ephemeral:    ephemeral,
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("marshal message: %w", err)
	}

	cMsg := C.CString(string(raw))
	defer C.free(unsafe.Pointer(cMsg))

	return call(func(ud unsafe.Pointer) C.int { return C.bridge_send(n.ctx, ud, cMsg) })
}

// NodeInfo returns the node info blob for the given id.
func (n *Node) NodeInfo(id string) (string, error) {
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_node_info(n.ctx, ud, cID) })
}

// NodeInfoIDs lists the available node-info ids.
func (n *Node) NodeInfoIDs() (string, error) {
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_node_info_ids(n.ctx, ud) })
}

// AvailableConfigs asks the library which config fields it accepts. Useful for
// discovering the real config surface instead of guessing.
func (n *Node) AvailableConfigs() (string, error) {
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_available_configs(n.ctx, ud) })
}

// NamedPubsubTopic formats a *named* (static-sharding) pubsub topic as
// "/waku/2/<name>".
//
// It does NOT resolve autosharding — verified empirically: passing a content
// topic returns "/waku/2//app/1/name/proto", i.e. simple concatenation. Use
// topic.Shard for the autoshard mapping.
func (n *Node) NamedPubsubTopic(contentTopic string) (string, error) {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_pubsub_topic(n.ctx, ud, ct) })
}

// PeersInMesh reports how many gossipsub mesh peers we have for a pubsub topic.
// Useful for confirming a rotation did not disturb the mesh.
func (n *Node) PeersInMesh(pubsubTopic string) (string, error) {
	pt := C.CString(pubsubTopic)
	defer C.free(unsafe.Pointer(pt))
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_peers_in_mesh(n.ctx, ud, pt) })
}

// PeerID returns this node's libp2p peer id.
func (n *Node) PeerID() (string, error) {
	return call(func(ud unsafe.Pointer) C.int { return C.bridge_my_peerid(n.ctx, ud) })
}

// Events returns the channel asynchronous events are delivered on.
func (n *Node) Events() <-chan Event { return n.events }

// Dropped reports how many events were discarded because the buffer was full.
func (n *Node) Dropped() uint64 { return atomic.LoadUint64(&n.dropped) }
