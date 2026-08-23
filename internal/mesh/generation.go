package mesh

import (
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/vpavlin/shrooms/internal/control"
	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/topic"
)

// The announce generation, and how a device gets the current one.
//
// Everything here exists because revocation used to remove a device from the
// roster and refuse its announces, while leaving it able to READ every announce
// on the mesh for ever — the network key never changed, and every announce is
// sealed under it. See docs/revocation-and-the-network-key.md.

// keys is the keyring announces are sealed under: the network key and the
// generation this device holds. Generation zero derives exactly what it always
// did, so a mesh that has never rotated is unchanged.
func (m *Mesh) keys() control.Keyring {
	return control.NewKeyring(m.nk, m.st.GenerationSecret)
}

// readKeys is every keyring worth trying on an inbound message, current first.
//
// The previous generation is still honoured, because a peer that has not yet
// been rekeyed is still a member and still needs reading. That window is the
// price of not stranding anybody, and it is why revocation is not instant.
func (m *Mesh) readKeys() []control.Keyring {
	out := []control.Keyring{m.keys()}
	if len(m.st.PrevSecret) > 0 {
		out = append(out, control.NewKeyring(m.nk, m.st.PrevSecret))
	}
	return out
}

// baseKeys is generation zero: the network key alone.
//
// Revocations and rekey envelopes are sealed under it deliberately. A device
// that missed a rotation must still be able to read the envelope that fixes it,
// and news of a revocation has to reach the nodes most likely to be behind.
func (m *Mesh) baseKeys() control.Keyring {
	return control.NewKeyring(m.nk, nil)
}

// Generation reports the generation this device is on, for status.
func (m *Mesh) Generation() uint64 { return m.st.Generation }

// adoptGeneration takes a secret delivered in a rekey envelope.
//
// The order of checks is the whole security argument, so it is worth stating:
//
//  1. The statement must be signed by THIS mesh's authority. Without this any
//     holder of the network key could name a generation.
//  2. The secret must match the commitment in that statement. Without this any
//     MEMBER could substitute a generation of its own and lock the rest out.
//  3. It must be strictly newer than what we hold. Without this a revoked
//     device — which legitimately holds the previous secret through the grace
//     window, and can replay the public statement naming it — could walk us
//     back to a generation it can still read.
//  4. We must already hold the revocation it enforces. Without this a device
//     that was offline during the revoke gets rekeyed, and then answers the
//     revoked device's envelope with the new secret, having checked a
//     revocation list that does not yet contain it.
func (m *Mesh) adoptGeneration(rotRaw, secret []byte, now time.Time) error {
	if m.authority == nil {
		return errors.New("this mesh has no authority, so nothing can rotate it")
	}
	rot, err := cred.UnmarshalRotation(rotRaw)
	if err != nil {
		return fmt.Errorf("unreadable rotation: %w", err)
	}
	if err := cred.VerifyRotationBy(m.authority, rot); err != nil {
		return fmt.Errorf("rotation does not verify: %w", err)
	}
	if !rot.Commits(secret) {
		return errors.New("the secret does not match the admin's commitment")
	}
	if rot.Generation <= m.st.Generation {
		return fmt.Errorf("generation %d is not newer than %d", rot.Generation, m.st.Generation)
	}
	if rot.Serial != 0 && !m.holdsRevocation(rot.Serial) {
		return fmt.Errorf("this rotation enforces revocation %d, which has not reached us yet", rot.Serial)
	}

	// The one we are leaving stays readable, so peers not yet rekeyed are not
	// cut off the moment we move.
	m.st.PrevGeneration, m.st.PrevSecret = m.st.Generation, m.st.GenerationSecret
	m.st.Generation = rot.Generation
	m.st.GenerationSecret = append([]byte(nil), secret...)
	m.st.Rotation = append([]byte(nil), rotRaw...)
	if err := m.st.Save(); err != nil {
		return fmt.Errorf("persist the generation: %w", err)
	}
	m.log.Info("moved to a new announce generation",
		"generation", rot.Generation, "enforces_revocation", rot.Serial)
	return nil
}

// holdsRevocation reports whether a revocation with this serial is on our list.
func (m *Mesh) holdsRevocation(serial uint64) bool {
	for _, raw := range m.revoked.All() {
		if r, err := cred.UnmarshalRevocation(raw); err == nil && r.Serial >= serial {
			return true
		}
	}
	return false
}

// handleRekey takes an envelope off the wire.
func (m *Mesh) handleRekey(r *control.Rekey, now time.Time) {
	if !r.For(m.st.Identity.SealPub) {
		return // somebody else's; one map-free comparison and done
	}
	secret, err := r.Unseal(m.st.Identity.SealPriv)
	if err != nil {
		m.log.Debug("could not open a rekey addressed to us", "err", err)
		return
	}
	if err := m.adoptGeneration(r.Rotation, secret, now); err != nil {
		m.log.Debug("declined a rekey", "err", err)
		return
	}
}

// publishRekeys hands the current generation to every member that can receive
// it, once per epoch.
//
// A standing statement rather than an event: a device that wakes finds its own
// envelope waiting rather than having to ask, so there is no request for a
// revoked device to replay and no amplification to be had from one.
//
// Sent to every member rather than only to those that look behind, because
// looking behind is exactly what we cannot see: a device that missed a rotation
// cannot be read, so it is invisible in the roster it is absent from.
func (m *Mesh) publishRekeys(now time.Time) {
	if m.st.Generation == 0 || len(m.st.GenerationSecret) == 0 || len(m.st.Rotation) == 0 {
		return
	}
	for _, p := range m.roster.Peers() {
		sealPub := m.sealPubOf(p)
		if len(sealPub) != 32 {
			continue // a version 1 credential: nothing to address
		}
		raw, err := control.SealRekey(m.nk, topic.Epoch(now), m.st.Identity.DevicePriv,
			m.st.Rotation, sealPub, m.st.GenerationSecret, now)
		if err != nil {
			m.log.Debug("could not seal a rekey", "peer", p.Name, "err", err)
			continue
		}
		if _, err := m.node.Send(topic.Current(m.nk, now), raw, true); err != nil {
			m.log.Debug("could not publish a rekey", "peer", p.Name, "err", err)
		}
	}
}

// sealPubOf reads a peer's sealing key out of the credential it announced.
//
// From the credential rather than from the announce, because the credential is
// admin-signed: a self-asserted key would let a peer name somebody else's and
// have the mesh seal that peer's secrets to it.
func (m *Mesh) sealPubOf(p PeerInfo) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sealPubs[hex.EncodeToString(p.DevicePub)]
}

// Rotate adopts a generation minted by the admin and hands it straight to every
// member, without waiting for the next epoch.
//
// The same checks as a rekey arriving off the wire, deliberately: this comes in
// over the control socket, which the socket group can reach, and "the admin
// meant it" is not something this end can tell. The signature is.
func (m *Mesh) Rotate(rotRaw, secret []byte) error {
	now := time.Now()
	if err := m.adoptGeneration(rotRaw, secret, now); err != nil {
		return err
	}
	// Immediately, and again every epoch after. A member that is offline right
	// now is not lost: its envelope will be waiting for it.
	m.publishRekeys(now)
	return nil
}
