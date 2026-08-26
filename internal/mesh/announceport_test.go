package mesh

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/vpavlin/shrooms/internal/disco"
	"github.com/vpavlin/shrooms/internal/state"
)

// A mesh must announce the port it bound.
//
// The mesh package was never wrong about this — candidates() has always used
// m.cfg.ListenPort. What was wrong is what the callers put there: they worked
// out the nth mesh's port, bound WireGuard to it, and handed over a config
// still holding the device's port. So this test covers the seam rather than
// either side of it, because each side was individually correct.
//
// Failure mode: a peer on the same LAN dials the announced address, reaches the
// FIRST mesh's WireGuard socket, and its handshake is rejected in silence
// because the keys belong to another mesh. Two devices one hop apart then need
// a relay to talk, which is how this was reported — twice.
func TestAMeshAnnouncesThePortItBound(t *testing.T) {
	const bound = 51824

	dev := state.Config{Name: "laptop", ListenPort: 51820}
	cfg := dev.ForMesh(state.Mesh{Label: "kc", NetworkKey: "k"}, bound)

	var k disco.Key
	m := &Mesh{
		cfg:    cfg,
		roster: &Roster{peers: map[string]PeerInfo{}},
		prober: disco.NewProber(k, make([]byte, 64),
			func([]byte, netip.AddrPort) error { return nil }),
	}

	got := m.candidates()
	if len(got) == 0 {
		t.Skip("no local addresses on this host, so there is nothing to check")
	}
	for _, c := range got {
		ap, err := netip.ParseAddrPort(c)
		if err != nil {
			t.Fatalf("announced %q, which is not an address and port: %v", c, err)
		}
		if ap.Port() != bound {
			t.Errorf("announced %s, but this mesh is listening on %d — "+
				"a peer dialling that reaches another mesh's socket", c, bound)
		}
		if strings.HasSuffix(c, ":51820") {
			t.Errorf("announced the DEVICE's port in %s", c)
		}
	}
}
