//go:build ld040

package waku

/*
// The typed helpers encode and decode CBOR inline, with TinyCBOR: the
// library build stages libtinycbor.a and its headers beside the .so.
#cgo LDFLAGS: -ltinycbor
#include <stdlib.h>
#include <stdint.h>
#include "liblogosdelivery.h"

// Defined in callback.go via //export.
extern void goFFICallback(int ret, char *msg, size_t len, void *userData);
extern void goReplyCallback(int ret, char *reply, char *errMsg, void *userData);
extern void goCreateCallback(int ret, void *ctx, char *errMsg, void *userData);

// v0.40's typed helpers hand a reply over as `const char *const *reply`, a
// pointer to the string, NULL or pointing at NULL when there is none. One
// trampoline per shape turns that into a plain string for Go, so the Go side
// never reads a C pointer to a pointer.
static void reply_tramp(int ret, const char *const *reply, const char *errMsg, void *ud) {
    goReplyCallback(ret, (char *)(reply ? *reply : NULL), (char *)errMsg, ud);
}
static void create_tramp(int ret, LogosDeliveryCtx *ctx, const char *errMsg, void *ud) {
    goCreateCallback(ret, (void *)ctx, (char *)errMsg, ud);
}

// The user data is a cgo.Handle, an integer: it crosses as uintptr_t and
// becomes a pointer only here, because Go turning an integer into an
// unsafe.Pointer is what -race's pointer checks refuse (CI 2026-10-08).
static int bridge_create(const char *cfg, uintptr_t ud) {
    return logosdelivery_ctx_create(cfg, create_tramp, (void *)ud);
}
static int bridge_start(void *ctx, uintptr_t ud) {
    return logosdelivery_ctx_start_node((const LogosDeliveryCtx *)ctx, (LogosDeliveryStartNodeReplyFn)reply_tramp, (void *)ud);
}
static int bridge_stop(void *ctx, uintptr_t ud) {
    return logosdelivery_ctx_stop_node((const LogosDeliveryCtx *)ctx, (LogosDeliveryStopNodeReplyFn)reply_tramp, (void *)ud);
}
static int bridge_destroy(void *ctx) {
    return logosdelivery_ctx_destroy((LogosDeliveryCtx *)ctx);
}
static int bridge_subscribe(void *ctx, uintptr_t ud, const char *topic) {
    return logosdelivery_ctx_subscribe((const LogosDeliveryCtx *)ctx, topic, (LogosDeliverySubscribeReplyFn)reply_tramp, (void *)ud);
}
static int bridge_unsubscribe(void *ctx, uintptr_t ud, const char *topic) {
    return logosdelivery_ctx_unsubscribe((const LogosDeliveryCtx *)ctx, topic, (LogosDeliveryUnsubscribeReplyFn)reply_tramp, (void *)ud);
}
static int bridge_send(void *ctx, uintptr_t ud, const char *msgJSON) {
    return logosdelivery_ctx_send((const LogosDeliveryCtx *)ctx, msgJSON, (LogosDeliverySendReplyFn)reply_tramp, (void *)ud);
}
static int bridge_node_info(void *ctx, uintptr_t ud, const char *id) {
    return logosdelivery_ctx_get_node_info((const LogosDeliveryCtx *)ctx, id, (LogosDeliveryGetNodeInfoReplyFn)reply_tramp, (void *)ud);
}
static int bridge_node_info_ids(void *ctx, uintptr_t ud) {
    return logosdelivery_ctx_get_available_node_info_ids((const LogosDeliveryCtx *)ctx, (LogosDeliveryGetAvailableNodeInfoIdsReplyFn)reply_tramp, (void *)ud);
}
static int bridge_available_configs(void *ctx, uintptr_t ud) {
    return logosdelivery_ctx_get_available_configs((const LogosDeliveryCtx *)ctx, (LogosDeliveryGetAvailableConfigsReplyFn)reply_tramp, (void *)ud);
}
static int bridge_pubsub_topic(void *ctx, uintptr_t ud, const char *topicName) {
    return logosdelivery_ctx_waku_pubsub_topic((const LogosDeliveryCtx *)ctx, topicName, (LogosDeliveryWakuPubsubTopicReplyFn)reply_tramp, (void *)ud);
}
static int bridge_peers_in_mesh(void *ctx, uintptr_t ud, const char *pubsubTopic) {
    return logosdelivery_ctx_waku_relay_get_num_peers_in_mesh((const LogosDeliveryCtx *)ctx, pubsubTopic, (LogosDeliveryWakuRelayGetNumPeersInMeshReplyFn)reply_tramp, (void *)ud);
}
static int bridge_my_peerid(void *ctx, uintptr_t ud) {
    return logosdelivery_ctx_waku_get_my_peerid((const LogosDeliveryCtx *)ctx, (LogosDeliveryWakuGetMyPeeridReplyFn)reply_tramp, (void *)ud);
}

// Events take the raw handle, ctx->ptr, not the typed context.
static uint64_t bridge_listen(void *ctx, const char *name, uintptr_t ud) {
    return logosdelivery_add_event_listener(((LogosDeliveryCtx *)ctx)->ptr, name, (FFICallBack)goFFICallback, (void *)ud);
}
*/
import "C"

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"runtime/cgo"
	"sync"
	"time"
	"unsafe"
)

// listenedEvents are the events a node registers for: the ones the daemon and
// the phone read (rendezvous.Transport.Deliver), under v0.40's listener names.
// Each payload still carries its eventType, so the readers are unchanged.
var listenedEvents = []string{
	"onMessageReceived",
	"onConnectionStatusChange",
	"onConnectionChange",
	"onTopicHealthChange",
}

// Node is a Logos Delivery node.
type Node struct {
	ctx unsafe.Pointer // *LogosDeliveryCtx

	events  chan Event
	dropped uint64

	mu       sync.Mutex
	evHandle *cgo.Handle // event-listener handle, freed on Close
	closed   bool
}

// call performs a request-style FFI call and waits for its callback, for no
// longer than callTimeout in total. See the legacy binding for why the C call
// runs on its own goroutine and why a timed-out handle is leaked, not deleted.
func call(op string, fn func(ud C.uintptr_t) C.int) (string, error) {
	ch := make(chan result, 1)
	h := cgo.NewHandle(ch)
	msg, err, settled := await(op, func() int { return int(fn(C.uintptr_t(h))) }, ch, callTimeout)
	if settled {
		h.Delete()
	}
	return msg, err
}

// New creates a node. It is not started yet.
//
// Creation is asynchronous in v0.40: the context arrives in a callback, and
// only on success.
func New(cfg Config) (*Node, error) {
	cfg = asBefore(cfg)
	raw, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	cCfg := C.CString(string(raw))
	defer C.free(unsafe.Pointer(cCfg))

	ch := make(chan createResult, 1)
	h := cgo.NewHandle(ch)
	done := make(chan C.int, 1)
	go func() { done <- C.bridge_create(cCfg, C.uintptr_t(h)) }()
	var r createResult
	select {
	case r = <-ch:
	case <-time.After(callTimeout):
		// The handle is leaked: the library may still call back with it.
		lib.record("create", callHung, time.Now())
		return nil, fmt.Errorf("ffi create did not answer within %s: %w", callTimeout, ErrLibraryUnreachable)
	}
	h.Delete()
	if r.ret != 0 || r.ctx == nil {
		if r.msg == "" {
			r.msg = fmt.Sprintf("code %d", r.ret)
		}
		return nil, fmt.Errorf("create_node: %s", r.msg)
	}
	if rc := <-done; rc != 0 {
		return nil, fmt.Errorf("create_node returned %d", int(rc))
	}

	n := &Node{ctx: r.ctx, events: make(chan Event, eventBuffer)}

	// Listeners before start, so nothing is missed. One handle for all of
	// them, held for the node's lifetime.
	eh := cgo.NewHandle(&eventSink{node: n})
	n.evHandle = &eh
	for _, name := range listenedEvents {
		cn := C.CString(name)
		id := C.bridge_listen(n.ctx, cn, C.uintptr_t(eh))
		C.free(unsafe.Pointer(cn))
		if id == 0 {
			C.bridge_destroy(n.ctx)
			eh.Delete()
			return nil, fmt.Errorf("listen for %s: refused", name)
		}
	}
	return n, nil
}

// asBefore fills in what v0.40 changed by default, where the caller did not
// say, so a node comes up as it did on the July library (ADR-071):
//
//   - QUIC off. v0.40 turns it on, on UDP at the node's TCP port, and the
//     daemon gives discovery that same UDP port: left on, the node never
//     finished starting (start's callback timed out, laptop, 2026-10-24); off,
//     it was connected in 3 s. The July library ran QUIC on a random port
//     nobody's firewall opened, so nothing reached it there either.
//   - Ports the kernel picks. The July library did that; v0.40 takes 60000
//     for TCP and 9000 for discovery, so a second node on a machine (a test
//     beside a running daemon) failed with "Address already in use".
//
// Here rather than in the callers because the July library refuses options it
// does not know, and the callers serve both bindings.
func asBefore(cfg Config) Config {
	out := make(Config, len(cfg)+3)
	for k, v := range cfg {
		out[k] = v
	}
	for k, v := range map[string]any{"quicSupport": false, "tcpPort": 0, "discv5UdpPort": 0} {
		if _, set := out[k]; !set {
			out[k] = v
		}
	}
	return out
}

// Start starts the node.
func (n *Node) Start() error {
	_, err := call("start", func(ud C.uintptr_t) C.int { return C.bridge_start(n.ctx, ud) })
	return err
}

// Stop stops the node without destroying it. The listeners stay.
func (n *Node) Stop() error {
	_, err := call("stop", func(ud C.uintptr_t) C.int { return C.bridge_stop(n.ctx, ud) })
	return err
}

// Close stops and destroys the node and releases its handles.
//
// Stopped explicitly first, though v0.40's destroy stops a running node
// itself: upstream says a stop inside destroy is only logged when it fails,
// and is cancelled after 10 s, leaving the node half stopped.
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

	_, _ = call("stop", func(ud C.uintptr_t) C.int { return C.bridge_stop(n.ctx, ud) })
	var err error
	if rc := C.bridge_destroy(n.ctx); rc != 0 {
		err = fmt.Errorf("destroy returned %d", int(rc))
	}
	// destroy removes the listeners, so nothing can call back with it now.
	if eh != nil {
		eh.Delete()
	}
	return err
}

// Subscribe subscribes to a content topic.
func (n *Node) Subscribe(contentTopic string) error {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	_, err := call("subscribe", func(ud C.uintptr_t) C.int { return C.bridge_subscribe(n.ctx, ud, ct) })
	return err
}

// Unsubscribe unsubscribes from a content topic.
func (n *Node) Unsubscribe(contentTopic string) error {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	_, err := call("unsubscribe", func(ud C.uintptr_t) C.int { return C.bridge_unsubscribe(n.ctx, ud, ct) })
	return err
}

// Send publishes payload to contentTopic, as base64, and returns the request
// id.
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
	return call("send", func(ud C.uintptr_t) C.int { return C.bridge_send(n.ctx, ud, cMsg) })
}

// NodeInfo returns the node info blob for the given id.
func (n *Node) NodeInfo(id string) (string, error) {
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))
	return call("node_info", func(ud C.uintptr_t) C.int { return C.bridge_node_info(n.ctx, ud, cID) })
}

// NodeInfoIDs lists the available node-info ids.
func (n *Node) NodeInfoIDs() (string, error) {
	return call("node_info_ids", func(ud C.uintptr_t) C.int { return C.bridge_node_info_ids(n.ctx, ud) })
}

// AvailableConfigs asks the library which config fields it accepts.
func (n *Node) AvailableConfigs() (string, error) {
	return call("available_configs", func(ud C.uintptr_t) C.int { return C.bridge_available_configs(n.ctx, ud) })
}

// NamedPubsubTopic formats a named (static-sharding) pubsub topic as
// "/waku/2/<name>". It does not resolve autosharding; use topic.Shard.
func (n *Node) NamedPubsubTopic(contentTopic string) (string, error) {
	ct := C.CString(contentTopic)
	defer C.free(unsafe.Pointer(ct))
	return call("pubsub_topic", func(ud C.uintptr_t) C.int { return C.bridge_pubsub_topic(n.ctx, ud, ct) })
}

// PeersInMesh reports how many gossipsub mesh peers we have for a pubsub topic.
func (n *Node) PeersInMesh(pubsubTopic string) (string, error) {
	pt := C.CString(pubsubTopic)
	defer C.free(unsafe.Pointer(pt))
	return call("peers_in_mesh", func(ud C.uintptr_t) C.int { return C.bridge_peers_in_mesh(n.ctx, ud, pt) })
}

// PeerID returns this node's libp2p peer id.
func (n *Node) PeerID() (string, error) {
	s, err := call("peer_id", func(ud C.uintptr_t) C.int { return C.bridge_my_peerid(n.ctx, ud) })
	if err != nil {
		return "", err
	}
	return peerIDText(s)
}
