// Command wakuspike is spike S1: prove that a Go process can drive
// liblogosdelivery via cgo, join a Waku network, and complete a
// publish -> receive round trip through the event callback.
//
// Success criteria:
//  1. the Nim and Go runtimes coexist in one process
//  2. the node starts and connects
//  3. a message published by this node comes back through the event thread
//     WHILE the node is actually connected to the fleet — a disconnected node
//     delivers its own message to itself, which satisfies (3) and proves
//     nothing about the network
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sync/atomic"
	"time"

	"github.com/vpavlin/shrooms/internal/waku"
)

func main() {
	var (
		topic     = flag.String("topic", "/logosvpn-spike/1/s1/proto", "content topic")
		mode      = flag.String("mode", "Core", "Core | Edge")
		cluster   = flag.Int("cluster", 2, "cluster id (2 = logos.dev); -1 to omit")
		preset    = flag.String("preset", "", "network preset, e.g. logos.dev (empty to omit)")
		extraCfg  = flag.String("config", "", "extra flat config JSON merged into the node config")
		settle    = flag.Duration("settle", 25*time.Second, "wait for peers before publishing")
		wait      = flag.Duration("wait", 45*time.Second, "how long to wait for our message back")
		probeOnly = flag.Bool("probe", false, "only print available configs/node info, then exit")
		listen    = flag.Bool("listen", false, "subscribe and report the first message received; never publish")
		verbose   = flag.Bool("v", false, "dump every event")
	)
	flag.Parse()

	log.SetFlags(log.Ltime | log.Lmicroseconds)

	cfg := waku.Config{"mode": *mode}
	if *cluster >= 0 {
		cfg["clusterId"] = *cluster
	}
	if *preset != "" {
		cfg["preset"] = *preset
	}
	if *extraCfg != "" {
		var extra map[string]any
		if err := json.Unmarshal([]byte(*extraCfg), &extra); err != nil {
			log.Fatalf("parse -config: %v", err)
		}
		for k, v := range extra {
			cfg[k] = v
		}
	}
	shown, _ := json.Marshal(cfg)
	log.Printf("creating node: %s", shown)

	node, err := waku.New(cfg)
	if err != nil {
		log.Fatalf("create: %v", err)
	}
	defer func() {
		log.Printf("closing node")
		if err := node.Close(); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	// Track fleet connectivity from the event stream.
	//
	// This is not decoration. A node with zero fleet peers still delivers a
	// message it publishes to its OWN subscribers, so the round-trip check
	// below passes while completely disconnected — it proves the FFI works and
	// says nothing whatsoever about the fleet. S1 is the canary we reach for
	// when the mesh misbehaves, and a canary that cannot fail is worse than no
	// canary at all.
	var connStatus atomic.Value
	connStatus.Store("unknown")

	// Drain events immediately so the C thread is never blocked.
	received := make(chan string, 8)
	go func() {
		for ev := range node.Events() {
			if *verbose {
				log.Printf("event ret=%d %s", ev.Ret, truncate(ev.JSON, 400))
			}
			var e struct {
				EventType        string `json:"eventType"`
				ConnectionStatus string `json:"connectionStatus"`
			}
			if json.Unmarshal([]byte(ev.JSON), &e) == nil && e.EventType == "connection_status_change" {
				connStatus.Store(e.ConnectionStatus)
				log.Printf("connection status: %s", e.ConnectionStatus)
			}
			if msg, hash, ok := waku.ParseMessage(ev.JSON); ok {
				log.Printf("message_received %s on %s (%d bytes)", truncate(hash, 20), msg.ContentTopic, len(msg.Payload))
				select {
				case received <- string(msg.Payload):
				default:
				}
			}
		}
	}()

	if cfgs, err := node.AvailableConfigs(); err == nil {
		// Untruncated under -probe: printing what the library accepts is the
		// entire purpose of that mode, and an 800-char cut hid most of it.
		if *probeOnly {
			log.Printf("available configs:\n%s", cfgs)
		} else {
			log.Printf("available configs: %s", truncate(cfgs, 800))
		}
	} else {
		log.Printf("available configs: %v", err)
	}

	if err := node.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}
	log.Printf("node started")

	if ids, err := node.NodeInfoIDs(); err == nil {
		log.Printf("node info ids: %s", truncate(ids, 400))
		for _, id := range []string{"peerId", "listenAddresses", "enr"} {
			if v, err := node.NodeInfo(id); err == nil {
				log.Printf("  %s = %s", id, truncate(v, 300))
			}
		}
	}

	if *probeOnly {
		log.Printf("probe only, exiting")
		return
	}

	if err := node.Subscribe(*topic); err != nil {
		log.Fatalf("subscribe %s: %v", *topic, err)
	}
	log.Printf("subscribed to %s", *topic)

	if *listen {
		log.Printf("listening on %s for %s (will not publish)", *topic, *wait)
		select {
		case p := <-received:
			log.Printf("RECEIVED %q", truncate(p, 200))
			log.Printf("dropped events: %d", node.Dropped())
			log.Printf("S1 PASS (event path works)")
			return
		case <-time.After(*wait):
			log.Printf("dropped events: %d", node.Dropped())
			log.Printf("S1 FAIL: nothing received in %s", *wait)
			os.Exit(1)
		}
	}

	log.Printf("settling %s to find peers...", *settle)
	time.Sleep(*settle)

	// Gate on connectivity BEFORE publishing, so the failure names the real
	// cause instead of surfacing as a mysterious pass.
	if st, _ := connStatus.Load().(string); st == "Disconnected" {
		log.Printf("dropped events: %d", node.Dropped())
		log.Printf("S1 FAIL: node is Disconnected after settling %s — no fleet peers", *settle)
		log.Printf("  The round trip below would have PASSED anyway: a disconnected")
		log.Printf("  node still delivers its own published message to its own")
		log.Printf("  subscribers. That proves the cgo binding works, not the fleet.")
		log.Printf("  Check whether logos.dev is reachable before debugging anything else.")
		os.Exit(1)
	}
	log.Printf("connected (status=%v), publishing", connStatus.Load())

	marker := fmt.Sprintf("s1-spike-%d", time.Now().UnixNano())
	log.Printf("publishing marker %q", marker)

	sentAt := time.Now()
	if _, err := node.Send(*topic, []byte(marker), false); err != nil {
		log.Fatalf("send: %v", err)
	}

	deadline := time.After(*wait)
	for {
		select {
		case p := <-received:
			if p != marker {
				log.Printf("received a different message: %q", truncate(p, 120))
				continue
			}
			log.Printf("ROUND TRIP OK in %s", time.Since(sentAt).Round(time.Millisecond))
			log.Printf("dropped events: %d", node.Dropped())
			// Re-check: the fleet can drop away between settling and publishing,
			// and a self-delivered marker looks identical to a real round trip.
			if st, _ := connStatus.Load().(string); st == "Disconnected" {
				log.Printf("S1 FAIL: the marker came back, but the node is Disconnected —")
				log.Printf("  it was delivered locally and never reached the fleet.")
				os.Exit(1)
			}
			log.Printf("S1 PASS")
			return

		case <-deadline:
			log.Printf("dropped events: %d", node.Dropped())
			log.Printf("S1 FAIL: marker did not come back within %s", *wait)
			os.Exit(1)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
