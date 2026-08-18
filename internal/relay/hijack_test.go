package relay

import (
	"crypto/ed25519"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/identity"
)

// The attack: registration was authenticated only by the mesh-wide MAC, which
// every member can compute, and nothing tied the key inside the frame to the
// sender. So a member could tell a relay that another device's tunnel key was
// reachable at its own address — and every peer relaying to that victim
// delivered to the attacker instead. The payload stays encrypted end to end, so
// this is the victim going dark plus "who was trying to reach them".
func TestAMemberCannotRegisterAnotherDevicesKey(t *testing.T) {
	k := testKey(t)

	victimDevice, victimWG := deviceAndKey(t, 1)
	attackerPriv, _ := deviceAndKey(t, 2)

	// The relay is told which device owns which tunnel key — the question it
	// cannot answer alone. In the daemon this comes from the roster.
	s := NewServer(k, func(devicePub []byte, wg identity.WGKey) bool {
		return string(devicePub) == string(victimDevice.Public().(ed25519.PublicKey)) && wg == victimWG
	})

	attackerAddr := netip.MustParseAddrPort("198.51.100.66:51820")
	now := time.Now()

	// Signed by the attacker, registering the victim's tunnel key.
	frame := EncodeRegister(k, victimWG, attackerPriv, now)
	s.Handle(frame, attackerAddr, now)

	s.mu.Lock()
	_, registered := s.peers[victimWG]
	refused := s.refused
	s.mu.Unlock()

	if registered {
		t.Error("a member registered another device's tunnel key")
	}
	if refused == 0 {
		t.Error("the refusal was not counted, so it would be invisible")
	}
}

// The victim registering its own key still works, or the check above would be
// indistinguishable from a relay that accepts nothing.
func TestTheOwnerCanRegister(t *testing.T) {
	k := testKey(t)
	victimDevice, victimWG := deviceAndKey(t, 1)

	s := NewServer(k, func(devicePub []byte, wg identity.WGKey) bool {
		return string(devicePub) == string(victimDevice.Public().(ed25519.PublicKey)) && wg == victimWG
	})

	addr := netip.MustParseAddrPort("198.51.100.7:51820")
	now := time.Now()
	s.Handle(EncodeRegister(k, victimWG, victimDevice, now), addr, now)

	s.mu.Lock()
	reg, ok := s.peers[victimWG]
	s.mu.Unlock()
	if !ok || reg.addr != addr {
		t.Fatalf("the owner's registration was refused: %v %v", ok, reg.addr)
	}
}

// A captured registration is replayable only while the relay accepts its
// timestamp, which is what stops one recorded from a café being useful later.
func TestAStaleRegistrationIsRefused(t *testing.T) {
	k := testKey(t)
	device, wg := deviceAndKey(t, 3)
	s := NewServer(k, func([]byte, identity.WGKey) bool { return true })

	now := time.Now()
	old := now.Add(-2 * RegisterSkew)
	s.Handle(EncodeRegister(k, wg, device, old), netip.MustParseAddrPort("198.51.100.8:51820"), now)

	s.mu.Lock()
	_, ok := s.peers[wg]
	s.mu.Unlock()
	if ok {
		t.Error("a registration older than the skew window was accepted")
	}
}

// And a frame whose signature does not match the device it names never reaches
// the server's ownership check at all.
func TestAForgedSignatureIsRejectedAtDecode(t *testing.T) {
	k := testKey(t)
	device, wg := deviceAndKey(t, 4)
	frame := EncodeRegister(k, wg, device, time.Now())

	// Flip a byte of the signature and re-MAC, which a member can do.
	sigAt := 1 + keyLen + stampLen + devicePubLen
	frame[sigAt] ^= 0xff
	copy(frame[sigAt+sigLen:], mac(k, frame[:sigAt+sigLen]))

	if _, err := Decode(k, frame); !errors.Is(err, ErrNotSignedByDevice) {
		t.Fatalf("err = %v, want a signature refusal", err)
	}
}

func deviceAndKey(t *testing.T, n byte) (ed25519.PrivateKey, identity.WGKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg identity.WGKey
	wg[0], wg[1] = n, 0xcd
	return priv, wg
}
