package mesh

import (
	"crypto/ed25519"
	"encoding/hex"
	"log/slog"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/disco"
)

// testKey is a throwaway device key. The prober signs every packet it sends
// (ADR-029), so it needs a real one even where nothing is sent.
func testKey() ed25519.PrivateKey {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return priv
}

// forgettable is a Mesh with just enough wired up for forget() to run: the
// per-peer stores it reclaims, and the two it delegates to.
func forgettable() *Mesh {
	return &Mesh{
		log:       slog.New(slog.DiscardHandler),
		guard:     control.NewReplayGuard(),
		prober:    disco.NewProber(disco.Key{}, testKey(), func([]byte, netip.AddrPort) error { return nil }),
		timing:    newTimings(time.Now()),
		rates:     newRates(),
		repliedTo: make(map[string]time.Time),
		services:  make(map[string]peerServices),
		expiry:    make(map[string]int64),
		roams:     make(map[string]roamFight),
	}
}

// pruneForgotten deleted from m.repliedTo without holding replyMu, while
// shouldReplyTo writes the same map under it from the receive path. Two
// goroutines writing one Go map is not a torn value — it is a fatal
// "concurrent map writes" that takes the daemon down.
//
// It needed a peer to age past ForgetAfter at the moment announces were being
// processed, so it would have shown up rarely, in production, and never in a
// way anyone could reproduce. This test does what the field would eventually
// have done, deterministically, and only fails under -race — which `make test`
// and CI both use.
func TestPruneAndReplyDoNotRaceOnRepliedTo(t *testing.T) {
	m := forgettable()

	ids := make([]string, 32)
	for i := range ids {
		ids[i] = hex.EncodeToString([]byte{byte(i), 0xab, 0xcd})
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// The receive path: announces arriving, each deciding whether to reply.
	wg.Add(1)
	go func() {
		defer wg.Done()
		now := time.Now()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, id := range ids {
				m.replyMu.Lock()
				m.repliedTo[id] = now
				m.replyMu.Unlock()
			}
		}
	}()

	// The probe ticker: peers ageing out and being reclaimed.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.forget(ids)
		}
		close(stop)
	}()

	wg.Wait()
}

// The three maps that were never reclaimed are reclaimed now. services is the
// one with teeth: it accepts a signed list from any network-key holder, so on a
// bearer mesh it grew with every stranger who published one and nothing took an
// entry out again.
func TestForgottenPeersLeaveNoStateBehind(t *testing.T) {
	m := forgettable()
	m.repliedTo["a"] = time.Now()
	m.services["a"] = peerServices{}
	m.expiry["a"] = 1
	m.roams["a"] = roamFight{}

	m.forget([]string{"a"})

	if len(m.repliedTo) != 0 {
		t.Error("repliedTo kept a forgotten peer")
	}
	if len(m.services) != 0 {
		t.Error("services kept a forgotten peer — this one grows from any key holder")
	}
	if len(m.expiry) != 0 {
		t.Error("expiry kept a forgotten peer")
	}
	if len(m.roams) != 0 {
		t.Error("roams kept a forgotten peer")
	}
}
