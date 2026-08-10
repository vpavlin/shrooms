package control

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

// A message from another mesh must be unreadable, not merely differently keyed.
//
// Meshes share one public shard by design, so every node sees every other
// mesh's traffic on the wire. The only thing between them is that the
// ciphertext does not open — there is no filter, no allow-list and nothing else
// to fall back on, which is why this is worth asserting rather than assuming.
func TestAnnounceFromAnotherMeshDoesNotOpen(t *testing.T) {
	theirs, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	ours, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.New()
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	epoch := now.Unix() / 3600

	announce := func(name string) *Announce {
		return &Announce{
			Kind:      KindAnnounce,
			DevicePub: id.DevicePub,
			WGPub:     id.WGPub[:],
			Name:      name,
			Timestamp: now.Unix(),
			Seq:       1,
		}
	}

	sealed, err := Seal(theirs, epoch, id.DevicePriv, announce("a-peer-elsewhere"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAnnounce(ours, epoch, sealed, now); err == nil {
		t.Fatal("a message sealed for another mesh opened with our key")
	}

	// And ours opens with ours — otherwise this would pass against a Seal that
	// never worked at all, which is the way isolation tests usually lie.
	mine, err := Seal(ours, epoch, id.DevicePriv, announce("ours"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenAnnounce(ours, epoch, mine, now)
	if err != nil {
		t.Fatalf("our own message did not open: %v", err)
	}
	if got.Name != "ours" {
		t.Errorf("opened the wrong message: %q", got.Name)
	}
}
