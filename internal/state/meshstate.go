package state

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/vpavlin/shrooms/internal/identity"
)

// Per-mesh device state (ADR-015).
//
// A device has one identity per mesh, and a few things that must persist per
// mesh with it — above all the announce sequence number, which peers' replay
// guards track per device *per mesh*. A node that restarted and reset it is
// rejected by every peer until they forget it, which looks exactly like the
// node having vanished.

// MeshState is what this device keeps about one mesh.
type MeshState struct {
	Identity *identity.Identity

	// Seq is the announce sequence number for this mesh.
	Seq uint64

	// Credential is this device's membership of this mesh, or nil. Per mesh
	// because an authority is per mesh: being admitted to one says nothing
	// about another, and a credential names the per-mesh device keys anyway.
	Credential []byte
}

// MeshState returns this device's state within one mesh, creating it if this is
// the first time the mesh has been seen.
//
// legacy says this network id is the mesh the device already belonged to before
// there was more than one. Its keys are then kept verbatim rather than derived:
// a node that regenerated its identity would change its overlay address and
// WireGuard public key, breaking every established tunnel and appearing to its
// peers as a stranger while the old device lingered until it timed out.
func (s *State) MeshState(networkID string, legacy bool) (*MeshState, error) {
	if networkID == "" {
		return nil, errors.New("a mesh with no network id")
	}
	if s.Meshes == nil {
		s.Meshes = map[string]*MeshState{}
	}
	if ms, ok := s.Meshes[networkID]; ok {
		return ms, nil
	}

	var ms *MeshState
	switch {
	case legacy && s.Identity != nil:
		// Adopted whole, including Seq: it is the same device announcing on the
		// same mesh, and the number has to keep going up.
		ms = &MeshState{Identity: s.Identity, Seq: s.Seq, Credential: s.Credential}
	default:
		if s.Master == (identity.Master{}) {
			m, err := identity.NewMaster()
			if err != nil {
				return nil, err
			}
			s.Master = m
		}
		id, err := s.Master.Derive(networkID)
		if err != nil {
			return nil, err
		}
		ms = &MeshState{Identity: id}
	}
	s.Meshes[networkID] = ms
	return ms, s.Save()
}

// SetMeshCredential stores this device's membership of one mesh.
func (s *State) SetMeshCredential(networkID string, raw []byte) error {
	return s.SetMeshCredentialFor(networkID, false, raw)
}

// SetMeshCredentialFor stores it, saying whether the mesh uses this device's
// original identity.
//
// The flag matters and is easy to get wrong: storing a credential creates the
// mesh's state entry if it does not exist, and creating it with the wrong flag
// derives a fresh identity — leaving a credential that names one set of keys
// beside a mesh that announces with another. Every peer then refuses it,
// correctly and silently.
func (s *State) SetMeshCredentialFor(networkID string, legacy bool, raw []byte) error {
	ms, err := s.MeshState(networkID, legacy)
	if err != nil {
		return err
	}
	ms.Credential = append([]byte(nil), raw...)
	// The single-mesh fields stay in step for the mesh that owns them, so a
	// daemon that has not been taught about several meshes yet reads the same
	// thing it always did.
	if s.Identity != nil && ms.Identity == s.Identity {
		s.Credential = ms.Credential
	}
	return s.Save()
}

// meshStateFile is the on-disk form.
type meshStateFile struct {
	DevicePriv string `json:"device_priv"`
	WGPriv     string `json:"wg_priv"`
	Seq        uint64 `json:"seq"`
	Credential string `json:"credential,omitempty"`
}

func encodeMeshes(in map[string]*MeshState) map[string]meshStateFile {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]meshStateFile, len(in))
	for id, ms := range in {
		f := meshStateFile{Seq: ms.Seq}
		if ms.Identity != nil {
			f.DevicePriv = base64.StdEncoding.EncodeToString(ms.Identity.DevicePriv)
			f.WGPriv = base64.StdEncoding.EncodeToString(ms.Identity.WGPriv[:])
		}
		if len(ms.Credential) > 0 {
			f.Credential = base64.StdEncoding.EncodeToString(ms.Credential)
		}
		out[id] = f
	}
	return out
}

func decodeMeshes(in map[string]meshStateFile) (map[string]*MeshState, error) {
	if len(in) == 0 {
		return nil, nil
	}
	out := make(map[string]*MeshState, len(in))
	for id, f := range in {
		devPriv, err := base64.StdEncoding.DecodeString(f.DevicePriv)
		if err != nil || len(devPriv) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("state.json: mesh %s: bad device key", id)
		}
		wgPriv, err := base64.StdEncoding.DecodeString(f.WGPriv)
		if err != nil || len(wgPriv) != 32 {
			return nil, fmt.Errorf("state.json: mesh %s: bad wireguard key", id)
		}
		idn := &identity.Identity{
			DevicePriv: ed25519.PrivateKey(devPriv),
			DevicePub:  ed25519.PrivateKey(devPriv).Public().(ed25519.PublicKey),
		}
		copy(idn.WGPriv[:], wgPriv)
		pub, err := identity.PublicFromPrivate(idn.WGPriv)
		if err != nil {
			return nil, fmt.Errorf("state.json: mesh %s: %w", id, err)
		}
		idn.WGPub = pub

		ms := &MeshState{Identity: idn, Seq: f.Seq}
		if f.Credential != "" {
			// Not fatal if unreadable, for the same reason the single-mesh one
			// is not: a device without a credential can still run, and losing
			// the tunnel over it is worse than being asked to re-enrol.
			ms.Credential, _ = base64.StdEncoding.DecodeString(f.Credential)
		}
		out[id] = ms
	}
	return out, nil
}

// View presents one mesh's state as a State, for code that still expects the
// single-mesh shape.
//
// The Identity pointer and the credential are shared with the per-mesh entry,
// and Seq is written back on Save — so a mesh that advances its sequence number
// advances the one that persists, which is the whole reason this is a view
// rather than a copy.
func (s *State) View(ms *MeshState) *State {
	return &State{
		dir:      s.dir,
		Identity: ms.Identity,
		Seq:      ms.Seq,
		// Deliberately not the mesh set: a view is one mesh, and handing it the
		// whole map would let one mesh's Save clobber another's.
		Credential: ms.Credential,
		owner:      s,
		view:       ms,
	}
}
