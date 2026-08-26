package state

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/vpavlin/shrooms/internal/cred"
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

	// original marks the mesh the config names with its top-level network_key,
	// whose credential the single-mesh field mirrors. Set by MeshState from the
	// caller's `legacy`, not persisted: it is a fact about the config, and the
	// config is read every start.
	original bool

	// Seq is the announce sequence number for this mesh.
	Seq uint64

	// Credential is this device's membership of this mesh, or nil. Per mesh
	// because an authority is per mesh: being admitted to one says nothing
	// about another, and a credential names the per-mesh device keys anyway.
	Credential []byte

	// Generation is the announce generation this device is on, and the secret
	// that derives its keys. Zero and nil on a mesh that has never rotated,
	// which derives exactly what it always did.
	//
	// Persisted, and this is the part that matters: the highest generation a
	// device holds a secret for is its anchor against being walked backwards. A
	// revoked device legitimately holds the previous secret through the grace
	// window and can replay the admin's public statement for it, so an anchor
	// that resets on restart would let it win the race and pin a rebooting node
	// to a generation it can still read.
	Generation       uint64
	GenerationSecret []byte

	// PrevSecret is the generation before this one, kept so peers that have not
	// been rekeyed yet can still be read. Without it every peer is unreadable
	// from the moment we rotate until its own envelope reaches it, which is up
	// to an epoch of a mesh that looks broken.
	PrevGeneration uint64
	PrevSecret     []byte

	// Rotation is the admin-signed statement naming Generation, kept so this
	// device can serve the secret onward: every rekey envelope carries the
	// statement its recipient needs to check the secret against.
	Rotation []byte

	// Services is what peers on this mesh last said they publish (ADR-023),
	// keyed by peer id, kept across restarts.
	//
	// Persisted because the alternative is rediscovery, and rediscovery is
	// minutes: a node that restarts — including one our own watchdog
	// restarted — comes back knowing nothing about what anybody offers and
	// waits for each peer's next broadcast. The list is a claim rather than a
	// fact, and a remembered claim is no less true than a freshly received
	// one; what changes is only how old it is, which is recorded with it.
	Services map[string]ServiceClaim
}

// ServiceClaim is one peer's list and when it was last heard.
type ServiceClaim struct {
	Names []string `json:"names"`
	// Bound is what was listening on that peer's mesh address, "name:port"
	// (ADR-026).
	Bound []string `json:"bound,omitempty"`
	Seen  int64    `json:"seen"` // unix seconds
}

// MeshState returns this device's state within one mesh, creating it if this is
// the first time the mesh has been seen.
//
// legacy says this network id is the mesh the device already belonged to before
// there was more than one. Its keys are then kept verbatim rather than derived:
// a node that regenerated its identity would change its overlay address and
// WireGuard public key, breaking every established tunnel and appearing to its
// peers as a stranger while the old device lingered until it timed out.
// original marks the mesh written as the config's top-level network_key: the
// one this device was built around, whose credential the single-mesh field
// mirrors.
//
// Not derived from comparing identities. That was a POINTER comparison, true
// only within the process that created the entry — after a restart decodeMeshes
// builds a fresh Identity and it silently became false, so the single-mesh field
// stopped being kept in step and `shrooms keys` reported a credential the device
// had stopped using. Nor is a value comparison right: an additional mesh joined
// by invite adopts the base identity too (ADR-017), so the keys match for a mesh
// that must NOT touch the first one's credential.
//
// The caller knows, and passes it as `legacy`. This is where that answer is kept
// rather than re-derived from something that looks like it.

func (s *State) MeshState(networkID string, legacy bool) (*MeshState, error) {
	if networkID == "" {
		return nil, errors.New("a mesh with no network id")
	}
	if s.Meshes == nil {
		s.Meshes = map[string]*MeshState{}
	}
	if ms, ok := s.Meshes[networkID]; ok {
		ms.original = legacy
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
	ms.original = legacy
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
	// The credential must name the identity this mesh announces with.
	//
	// Refused rather than stored, because the failure is otherwise silent and
	// remote: the mesh announces a credential naming another device, every peer
	// refuses it, and the only evidence is a line in somebody else's log. Cost
	// an evening to find once.
	if c, err := cred.UnmarshalCredential(raw); err == nil && ms.Identity != nil {
		if !bytes.Equal(c.DevicePub, ms.Identity.DevicePub) {
			return fmt.Errorf("this credential names another device (%x, not %x); "+
				"it belongs to a different mesh or identity",
				c.DevicePub[:8], ms.Identity.DevicePub[:8])
		}
	}
	ms.Credential = append([]byte(nil), raw...)
	// The single-mesh field stays in step only for the device's ORIGINAL mesh.
	//
	// Not "whenever the identities match", which is what this said: an
	// additional mesh that adopts the base identity — every invite-joined one,
	// see ADR-017 — then overwrote the first mesh's credential with its own.
	// The first mesh would go on announcing a credential for a different mesh,
	// which its peers correctly refuse.
	if ms.original && s.Identity != nil {
		s.Credential = ms.Credential
	}
	return s.Save()
}

// SetGenerationFor adopts an announce generation for one mesh.
//
// Refuses anything that is not strictly newer. This is the anchor the whole
// rotation rests on: a revoked device holds the previous secret legitimately
// through the grace window, and the admin's statement naming that generation is
// public and replayable, so without a monotonic floor it could walk a node back
// to a generation it can still read.
//
// The floor is "the highest generation whose SECRET I hold", which is what this
// stores — never "the highest I have heard of". Anchoring on statements heard
// would let anyone replay a statement for a generation whose secret never
// arrives, leaving the node refusing every earlier one and unable to read the
// current one: silent, permanent deafness, inducible by an outsider.
//
// Verification of the statement and its commitment happens before this is
// called. This is the floor, not the check.
func (s *State) SetGenerationFor(networkID string, legacy bool, gen uint64, secret, rotation []byte) error {
	if gen == 0 || len(secret) == 0 {
		return errors.New("a generation needs a number and a secret")
	}
	ms, err := s.MeshState(networkID, legacy)
	if err != nil {
		return err
	}
	if gen <= ms.Generation {
		return fmt.Errorf("generation %d is not newer than %d", gen, ms.Generation)
	}
	// The one we are leaving becomes the previous, so peers that have not been
	// rekeyed yet stay readable until their own envelope reaches them.
	ms.PrevGeneration, ms.PrevSecret = ms.Generation, ms.GenerationSecret
	ms.Generation = gen
	ms.GenerationSecret = append([]byte(nil), secret...)
	ms.Rotation = append([]byte(nil), rotation...)
	return s.Save()
}

// meshStateFile is the on-disk form.
type meshStateFile struct {
	DevicePriv string `json:"device_priv"`
	WGPriv     string `json:"wg_priv"`
	Seq        uint64 `json:"seq"`
	Credential string `json:"credential,omitempty"`

	Generation       uint64 `json:"generation,omitempty"`
	GenerationSecret string `json:"generation_secret,omitempty"`
	PrevGeneration   uint64 `json:"prev_generation,omitempty"`
	PrevSecret       string `json:"prev_secret,omitempty"`
	Rotation         string `json:"rotation,omitempty"`

	Services map[string]ServiceClaim `json:"services,omitempty"`
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
		f.Generation = ms.Generation
		if len(ms.GenerationSecret) > 0 {
			f.GenerationSecret = base64.StdEncoding.EncodeToString(ms.GenerationSecret)
		}
		f.PrevGeneration = ms.PrevGeneration
		if len(ms.PrevSecret) > 0 {
			f.PrevSecret = base64.StdEncoding.EncodeToString(ms.PrevSecret)
		}
		if len(ms.Rotation) > 0 {
			f.Rotation = base64.StdEncoding.EncodeToString(ms.Rotation)
		}
		f.Services = ms.Services
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
		// The sealing key is derived, not stored: state.json predates it, and a
		// device that derived a different one on each start could not be sent
		// anything. Missing this leaves it zero, which is not inert — see
		// Identity.DeriveSealing.
		if err := idn.DeriveSealing(); err != nil {
			return nil, fmt.Errorf("state.json: mesh %s: %w", id, err)
		}

		ms := &MeshState{Identity: idn, Seq: f.Seq, Services: f.Services}
		if f.Credential != "" {
			// Not fatal if unreadable, for the same reason the single-mesh one
			// is not: a device without a credential can still run, and losing
			// the tunnel over it is worse than being asked to re-enrol.
			ms.Credential, _ = base64.StdEncoding.DecodeString(f.Credential)
		}
		// A generation without its secret is not a generation: it would leave
		// the node refusing every earlier one with nothing to read the current
		// one, which is silent, permanent deafness. Anchor on what we HOLD.
		if f.Generation > 0 && f.GenerationSecret != "" {
			if sec, err := base64.StdEncoding.DecodeString(f.GenerationSecret); err == nil && len(sec) > 0 {
				ms.Generation = f.Generation
				ms.GenerationSecret = sec
				ms.Rotation, _ = base64.StdEncoding.DecodeString(f.Rotation)
			}
		}
		if f.PrevGeneration > 0 && f.PrevSecret != "" {
			if sec, err := base64.StdEncoding.DecodeString(f.PrevSecret); err == nil {
				ms.PrevGeneration = f.PrevGeneration
				ms.PrevSecret = sec
			}
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
		original: ms.original,
		dir:      s.dir,
		Identity: ms.Identity,
		Seq:      ms.Seq,
		// Deliberately not the mesh set: a view is one mesh, and handing it the
		// whole map would let one mesh's Save clobber another's.
		Credential: ms.Credential,
		Services:   ms.Services,

		Generation:       ms.Generation,
		GenerationSecret: ms.GenerationSecret,
		PrevGeneration:   ms.PrevGeneration,
		PrevSecret:       ms.PrevSecret,
		Rotation:         ms.Rotation,
		owner:            s,
		view:             ms,
	}
}
