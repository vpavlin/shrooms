package control

import (
	"fmt"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
)

// The whole point: a real credential must fit a real announce.
func TestARealCredentialFitsARealAnnounce(t *testing.T) {
	nk, _ := identity.NewNetworkKey()
	id, _ := identity.New()
	admin, _ := cred.NewAdmin()
	now := time.Now()

	c, err := admin.Issue(id.DevicePub, id.WGPub[:], "a-fairly-long-device-name", 42, now, 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	a := &Announce{Kind: KindAnnounce, DevicePub: id.DevicePub, WGPub: id.WGPub[:],
		Name: "a-fairly-long-device-name", Seq: 4294967295, Timestamp: now.Unix(),
		Endpoints:  []string{"203.0.113.4:51820", "192.168.0.9:51820", "10.0.0.5:51820"},
		Credential: raw}
	sealed, err := Seal(nk, 1, id.DevicePriv, a)
	if err != nil {
		t.Fatalf("a real credential does not fit a real announce: %v", err)
	}
	fmt.Printf("  credential %d bytes, announce sealed to %d bytes\n", len(raw), len(sealed))

	got, err := OpenAnnounce(nk, 1, sealed, now)
	if err != nil {
		t.Fatal(err)
	}
	back, err := cred.UnmarshalCredential(got.Credential)
	if err != nil {
		t.Fatalf("the credential did not survive the announce: %v", err)
	}
	if err := cred.Verify(admin.Pub, back, now); err != nil {
		t.Errorf("did not verify after riding an announce: %v", err)
	}
}
