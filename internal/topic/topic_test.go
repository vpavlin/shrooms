package topic

import (
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

func mustNK(t *testing.T) identity.NetworkKey {
	t.Helper()
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatalf("NewNetworkKey: %v", err)
	}
	return nk
}

func TestDeriveIsDeterministic(t *testing.T) {
	nk := mustNK(t)
	if Derive(nk, 42) != Derive(nk, 42) {
		t.Fatal("Derive is not deterministic")
	}
}

func TestDeriveChangesPerEpoch(t *testing.T) {
	nk := mustNK(t)
	seen := map[string]bool{}
	for e := int64(0); e < 200; e++ {
		ct := Derive(nk, e)
		if seen[ct] {
			t.Fatalf("epoch %d repeated a topic", e)
		}
		seen[ct] = true
	}
}

func TestDeriveChangesPerNetwork(t *testing.T) {
	if Derive(mustNK(t), 7) == Derive(mustNK(t), 7) {
		t.Fatal("two networks derived the same topic")
	}
}

// The invariant the whole scheme rests on: rotation must not change the
// app/version prefix, because that is what determines the shard.
func TestRotationPreservesPrefix(t *testing.T) {
	nk := mustNK(t)
	base := Derive(nk, 0)
	for e := int64(1); e < 500; e++ {
		if !SamePrefix(base, Derive(nk, e)) {
			t.Fatalf("epoch %d changed the app/version prefix", e)
		}
	}
}

// ...and across different networks too, since the prefix is a constant.
func TestPrefixIsNetworkIndependent(t *testing.T) {
	if !SamePrefix(Derive(mustNK(t), 1), Derive(mustNK(t), 99)) {
		t.Fatal("prefix differs between networks; shard would not be stable")
	}
}

func TestShardIsStableAcrossRotation(t *testing.T) {
	want := Shard(Application, Version, NumShardsLogosDev)
	for i := 0; i < 100; i++ {
		if got := Shard(Application, Version, NumShardsLogosDev); got != want {
			t.Fatalf("shard changed between calls: %d != %d", got, want)
		}
	}
	if want >= NumShardsLogosDev {
		t.Fatalf("shard %d out of range", want)
	}
}

// Verified against a live logos.dev node by cmd/s3topics + scripts/check-s3.sh:
// six rotated topics all routed to /waku/2/rs/2/3.
func TestMeshPubsubTopicMatchesObserved(t *testing.T) {
	const observed = "/waku/2/rs/2/3"
	if got := MeshPubsubTopic(2, NumShardsLogosDev); got != observed {
		t.Fatalf("MeshPubsubTopic = %s, want %s (as observed live). "+
			"If Application/Version changed, re-run `make s3` and update this.", got, observed)
	}
}

func TestWindowSpansThreeEpochs(t *testing.T) {
	nk := mustNK(t)
	now := time.Now()
	w := Window(nk, now)
	if len(w) != 3 {
		t.Fatalf("Window returned %d topics, want 3", len(w))
	}
	e := Epoch(now)
	for i, want := range []string{Derive(nk, e-1), Derive(nk, e), Derive(nk, e+1)} {
		if w[i] != want {
			t.Errorf("Window[%d] = %s, want %s", i, w[i], want)
		}
	}
}

// Clock skew between devices is the practical failure mode, so a node slightly
// behind must still land inside a peer's accepted window.
func TestWindowToleratesSkew(t *testing.T) {
	nk := mustNK(t)
	now := time.Unix(Epoch(time.Now())*EpochSeconds+EpochSeconds/2, 0) // mid-epoch
	mine := Current(nk, now)

	for _, skew := range []time.Duration{-59 * time.Minute, -time.Minute, time.Minute, 29 * time.Minute} {
		peer := Window(nk, now.Add(skew))
		found := false
		for _, ct := range peer {
			if ct == mine {
				found = true
			}
		}
		if !found {
			t.Errorf("a peer skewed by %s would miss our topic", skew)
		}
	}
}

func TestNextRotationIsInTheFuture(t *testing.T) {
	now := time.Now()
	next := NextRotation(now)
	if !next.After(now) {
		t.Fatalf("NextRotation %s is not after %s", next, now)
	}
	if d := next.Sub(now); d > time.Duration(EpochSeconds)*time.Second {
		t.Fatalf("NextRotation is %s away, more than one epoch", d)
	}
}

func TestTopicFormat(t *testing.T) {
	ct := Derive(mustNK(t), 1)
	parts := strings.Split(ct, "/")
	if len(parts) != 5 || parts[0] != "" {
		t.Fatalf("malformed content topic %q", ct)
	}
	if parts[1] != Application || parts[2] != Version || parts[4] != Encoding {
		t.Fatalf("unexpected fields in %q", ct)
	}
	// The rotating field must not leak the epoch or the key in cleartext.
	if parts[3] == "" || strings.ContainsAny(parts[3], "/= ") {
		t.Fatalf("bad rotating field %q", parts[3])
	}
}

func TestSamePrefixRejectsMalformed(t *testing.T) {
	for _, s := range []string{"", "/", "no-leading-slash/1/x/proto", "/a/b"} {
		if SamePrefix(s, Derive(mustNK(t), 1)) {
			t.Errorf("SamePrefix accepted malformed topic %q", s)
		}
	}
}
