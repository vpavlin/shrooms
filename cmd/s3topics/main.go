// Command s3topics is spike S3: verify that rotating the rendezvous topic
// keeps every epoch on the SAME shard.
//
// The rotation design rests on this. Waku autosharding hashes only the
// application and version fields, so rotating {name} should change the content
// topic while leaving the pubsub topic fixed. If that were wrong, every
// rotation would emit gossipsub subscription churn and drop the mesh into a
// fresh, empty anonymity set.
//
// nwaku#2538 ("autosharding resolves content topics to wrong shard") is why
// this is checked against a live node rather than assumed.
//
// Method: publish to N consecutive epoch topics and let the library report
// where each one routed. The library logs "start publish Waku message ...
// pubsubTopic=..." on stderr; scripts/check-s3.sh compares those against the
// value computed locally by internal/topic.
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/topic"
	"github.com/vpavlin/shrooms/internal/waku"
)

func main() {
	var (
		epochs  = flag.Int("epochs", 6, "consecutive epochs to publish to")
		preset  = flag.String("preset", "logos.dev", "network preset")
		cluster = flag.Int("cluster", 2, "cluster id, for the locally computed expectation")
		settle  = flag.Duration("settle", 20*time.Second, "wait for peers before publishing")
	)
	flag.Parse()

	log.SetFlags(log.Ltime)

	nk, err := identity.NewNetworkKey()
	if err != nil {
		log.Fatalf("network key: %v", err)
	}

	expected := topic.MeshPubsubTopic(uint16(*cluster), topic.NumShardsLogosDev)
	fmt.Printf("S3_EXPECTED %s\n", expected)
	log.Printf("locally computed shard for /%s/%s: %s", topic.Application, topic.Version, expected)

	node, err := waku.New(waku.Config{"mode": "Core", "preset": *preset})
	if err != nil {
		log.Fatalf("create node: %v", err)
	}
	defer node.Close()

	go func() {
		for range node.Events() {
		}
	}()

	if err := node.Start(); err != nil {
		log.Fatalf("start: %v", err)
	}
	log.Printf("node started; settling %s", *settle)
	time.Sleep(*settle)

	base := topic.Epoch(time.Now())
	for i := 0; i < *epochs; i++ {
		ct := topic.Derive(nk, base+int64(i))

		// Every rotated topic must share the app/version prefix, or the shard
		// cannot be stable regardless of what the library does.
		if !topic.SamePrefix(ct, topic.Derive(nk, base)) {
			log.Fatalf("epoch %d changed the app/version prefix: %s", base+int64(i), ct)
		}

		if err := node.Subscribe(ct); err != nil {
			log.Fatalf("subscribe %s: %v", ct, err)
		}
		if _, err := node.Send(ct, []byte(fmt.Sprintf("s3-epoch-%d", base+int64(i))), true); err != nil {
			log.Fatalf("send %s: %v", ct, err)
		}
		fmt.Printf("S3_PUBLISHED %s\n", ct)
		time.Sleep(300 * time.Millisecond)
	}

	log.Printf("published to %d rotated topics", *epochs)
}
