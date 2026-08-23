package control

import (
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

// Growing the padding must not be a flag day.
//
// The reader used to demand exactly PaddedSize, so changing that constant meant
// old and new nodes could not read each other at all — a mesh would have to be
// upgraded everywhere at once, including a phone that updates on an app store's
// schedule. Readers now accept a set, which turns the change into two ordinary
// steps: tolerant readers everywhere, then move the senders.
func TestReaderAcceptsEveryKnownPadding(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	now := time.Now()
	epoch := now.Unix() / 3600

	a := &Announce{
		Kind: KindAnnounce, DevicePub: id.DevicePub, WGPub: id.WGPub[:],
		Name: "peer", Timestamp: now.Unix(), Seq: 1,
	}

	for _, size := range PaddedSizes {
		sealed, err := NewKeyring(nk, nil).sealPadded(epoch, id.DevicePriv, a, size)
		if err != nil {
			t.Fatalf("%d-byte padding: seal: %v", size, err)
		}
		got, err := OpenAnnounce(nk, epoch, sealed, now)
		if err != nil {
			t.Errorf("%d-byte padding did not open: %v", size, err)
			continue
		}
		if got.Name != "peer" {
			t.Errorf("%d-byte padding decoded %q", size, got.Name)
		}
	}
}

// An unknown size is still refused: the padding is a privacy property, and
// accepting arbitrary lengths would quietly discard it.
func TestReaderRefusesAnUnknownPadding(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	now := time.Now()
	epoch := now.Unix() / 3600

	sealed, err := NewKeyring(nk, nil).sealPadded(epoch, id.DevicePriv, &Announce{
		Kind: KindAnnounce, DevicePub: id.DevicePub, WGPub: id.WGPub[:],
		Name: "peer", Timestamp: now.Unix(), Seq: 1,
	}, 768)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAnnounce(nk, epoch, sealed, now); err == nil {
		t.Error("a 768-byte plaintext was accepted; padding sizes must be a known set")
	}
}
