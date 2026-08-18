package disco

import (
	"crypto/ed25519"
	"errors"
	"net/netip"
	"testing"
)

// The attack ADR-029 closes: every member holds the same disco key, so a member
// could compose a packet naming somebody else and have it believed. That bought
// path steering — answering another node's probes as a peer it trusts — and
// reflexive-address poisoning, where the lie is about where WE are.
//
// The forger here has the mesh key, which is the whole point: this is not an
// outsider, it is a member. What it does not have is the device key it claims.
func TestAMemberCannotSpeakForAnotherDevice(t *testing.T) {
	k, victimPriv, tx := fixture(t)
	victimPub := victimPriv.Public().(ed25519.PublicKey)

	_, forgerPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}

	// Signed by the forger, but naming the victim: build the body by hand,
	// because the encoder will not produce this.
	inner := make([]byte, 0, innerLen)
	inner = append(inner, byte(TypePong))
	inner = append(inner, victimPub...) // the lie
	inner = append(inner, tx[:]...)
	inner = append(inner, encodeAddr(netip.MustParseAddrPort("203.0.113.9:51820"))...)
	inner = append(inner, ed25519.Sign(forgerPriv, inner)...)

	pkt, err := sealInner(k, inner)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := Decode(k, pkt); !errors.Is(err, ErrSignature) {
		t.Fatalf("a member forged a packet naming another device: err = %v", err)
	}
}

// And the honest case still works, so the check above is not simply refusing
// everything.
func TestAGenuinePacketVerifies(t *testing.T) {
	k, priv, tx := fixture(t)
	want := netip.MustParseAddrPort("198.51.100.4:51820")

	pkt, err := EncodePong(k, priv, tx, want)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Decode(k, pkt)
	if err != nil {
		t.Fatalf("a packet we signed ourselves did not verify: %v", err)
	}
	if m.Observed != want {
		t.Errorf("observed = %v, want %v", m.Observed, want)
	}
	if string(m.SenderPub[:]) != string(priv.Public().(ed25519.PublicKey)) {
		t.Error("the sender is not the device that signed it")
	}
}

// Changing any covered field invalidates the signature, so an on-path attacker
// cannot edit the address a pong reports even though the packet is opaque to
// them — they would have to re-sign, which needs the device key.
func TestTamperingWithTheBodyIsCaught(t *testing.T) {
	k, priv, tx := fixture(t)
	pkt, err := EncodePong(k, priv, tx, netip.MustParseAddrPort("198.51.100.4:51820"))
	if err != nil {
		t.Fatal(err)
	}

	// Tamper inside the plaintext rather than the ciphertext: decrypt, edit,
	// re-seal with the mesh key a member legitimately holds.
	inner, err := openInner(k, pkt)
	if err != nil {
		t.Fatal(err)
	}
	inner[bodyLen-1] ^= 0xff // last byte of the observed port

	tampered, err := sealInner(k, inner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(k, tampered); !errors.Is(err, ErrSignature) {
		t.Fatalf("an edited body was accepted: err = %v", err)
	}
}
