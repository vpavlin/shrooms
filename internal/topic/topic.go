// Package topic derives the mesh's rotating rendezvous content topics.
//
// The rendezvous topic is derived from the network key and a time epoch, so an
// observer who has not got the network key cannot find the mesh's traffic, and
// linkability is bounded to one epoch.
//
// The trick that makes this cheap (DESIGN §4): Waku autosharding computes
//
//	shard = sha256(application ‖ version) mod numShardsInCluster
//
// hashing ONLY the application and version fields of the content topic, not the
// whole string. So rotating the {name} field changes the content topic while
// leaving the shard — and therefore the underlying gossipsub pubsub topic —
// fixed. Peers stay in one mesh, and no SUBSCRIBE/UNSUBSCRIBE control traffic
// announces the rotation to neighbours.
//
// Had we instead rotated the pubsub topic, every rotation would emit visible
// subscription churn and drop us into a fresh mesh with an anonymity set of one.
package topic

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// EpochSeconds is the rendezvous rotation period.
//
// One hour, with peers accepting epoch-1/epoch/epoch+1, gives a ~3 hour linkage
// window. Shorter is viable but clock skew — not the protocol — becomes the
// binding constraint below ~10 minutes.
const EpochSeconds int64 = 3600

// Application and Version fix the shard. They are deliberately NOT
// mesh-specific: a distinctive application string would be a global cleartext
// label on every message, identifying the mesh even though the {name} field is
// opaque. Keeping them generic puts all the rotating entropy in {name} and
// leaves us sharing a shard with other traffic.
//
// Changing either moves every topic to a different shard.
const (
	Application = "logos"
	Version     = "1"
	Encoding    = "proto"
)

// b32 is lowercase and unpadded: content topics are compared as strings, so the
// encoding must be stable and free of characters that need escaping.
var b32 = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Epoch returns the rendezvous epoch for a time.
func Epoch(t time.Time) int64 { return t.Unix() / EpochSeconds }

// Derive returns the rendezvous content topic for a network key and epoch.
func Derive(nk identity.NetworkKey, epoch int64) string {
	mac := hmac.New(sha256.New, nk.TopicKey())
	mac.Write([]byte("mesh/v1/rendezvous"))
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(epoch))
	mac.Write(b[:])

	tag := mac.Sum(nil)[:16]
	return fmt.Sprintf("/%s/%s/%s/%s", Application, Version, b32.EncodeToString(tag), Encoding)
}

// Current returns the topic for the current epoch.
func Current(nk identity.NetworkKey, now time.Time) string {
	return Derive(nk, Epoch(now))
}

// Window returns the topics a node should subscribe to: the previous, current
// and next epoch. Three live topics absorb clock skew between devices, which is
// the practical failure mode — a node with a slow clock would otherwise publish
// where nobody is listening.
func Window(nk identity.NetworkKey, now time.Time) []string {
	e := Epoch(now)
	return []string{Derive(nk, e-1), Derive(nk, e), Derive(nk, e+1)}
}

// NextRotation reports when the current epoch ends, so a caller can schedule
// resubscription rather than polling.
func NextRotation(now time.Time) time.Time {
	return time.Unix((Epoch(now)+1)*EpochSeconds, 0)
}

// SamePrefix reports whether two content topics share the application and
// version fields, and therefore land on the same shard.
//
// This is the invariant the whole rotation scheme rests on. Verified against a
// live node by cmd/s3topics.
func SamePrefix(a, b string) bool {
	pa, oka := prefixOf(a)
	pb, okb := prefixOf(b)
	return oka && okb && pa == pb
}

// prefixOf returns the "/{app}/{version}" portion of a content topic.
func prefixOf(contentTopic string) (string, bool) {
	parts := strings.Split(contentTopic, "/")
	// "/app/version/name/encoding" splits to ["", app, version, name, encoding]
	if len(parts) < 5 || parts[0] != "" {
		return "", false
	}
	return "/" + parts[1] + "/" + parts[2], true
}

// NumShardsLogosDev is the shard count for the logos.dev cluster (autosharding,
// 8 shards). Changing cluster changes this.
const NumShardsLogosDev = 8

// Shard returns the autoshard index for a content topic, per
// WAKU2-RELAY-SHARDING:
//
//	shard = uint64(BE, sha256(application ‖ version)[24:32]) mod numShards
//
// Only the application and version fields are hashed. That is precisely why
// rotating {name} leaves the shard fixed.
func Shard(application, version string, numShards uint64) uint64 {
	sum := sha256.Sum256([]byte(application + version))
	return binary.BigEndian.Uint64(sum[24:32]) % numShards
}

// PubsubTopic returns the sharded pubsub topic a content topic routes to.
func PubsubTopic(clusterID uint16, application, version string, numShards uint64) string {
	return fmt.Sprintf("/waku/2/rs/%d/%d", clusterID, Shard(application, version, numShards))
}

// MeshPubsubTopic returns the pubsub topic every rendezvous topic of this mesh
// routes to, for the given cluster. Constant across epochs by construction.
func MeshPubsubTopic(clusterID uint16, numShards uint64) string {
	return PubsubTopic(clusterID, Application, Version, numShards)
}
