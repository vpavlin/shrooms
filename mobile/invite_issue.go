package mobile

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/vpavlin/shrooms/internal/keycard"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/invite"
	"github.com/vpavlin/shrooms/internal/mesh"
)

// Admitting a device from the phone, with the authority on a card
// (docs/keycard-on-mobile.md).
//
// Three calls rather than one, mirroring what the desktop does over its control
// socket, and for the same reason: a credential can only be signed once the
// joining device's keys are known, and those arrive in the middle. The split is
// what makes a smartcard usable at all — the tap belongs between step two and
// step three, at the moment there is something real to sign.
//
//	MintInvite()                 -> a token, shown as a QR
//	AwaitInvite(token, seconds)  -> blocks; returns who asked
//	AdmitWithCard(...)           -> tap, sign, publish
//
// The wait is a foreground operation on the phone, which is not a limitation to
// be worked around: Android reads NFC only while a screen is in front, so the
// card cannot be tapped in the background whatever this API looked like.

// maxInviteHold is the longest an invite may be held open, matching the
// desktop's own cap.
const maxInviteHold = 2 * time.Hour

// InviteRequest is what a joining device asked for, as the app needs to show it.
type InviteRequest struct {
	Name      string `json:"name"`
	DevicePub string `json:"device_pub"`
	WGPub     string `json:"wg_pub"`
	SealPub   string `json:"seal_pub,omitempty"`
	EphPub    string `json:"eph_pub"`
}

// There is deliberately no Deferred field. A first-round request — the one that
// asks which mesh this is — is answered inside the mesh and never consumes the
// invite, so what reaches this API is always the second round: a device that
// has derived its per-mesh keys and is asking to be admitted (ADR-017). A flag
// that is always false is worse than absent, because an app would branch on it.

// MintInvite returns a fresh token for one device and fifteen minutes.
//
// Rendered by the app as `shrooms://enrol?token=…`, optionally with a bootstrap
// address (ADR-031) so the joiner does not depend on the public fleet.
func MintInvite() (string, error) {
	s, err := invite.New()
	if err != nil {
		return "", err
	}
	return s.String(), nil
}

// InviteURI renders a token as the string a QR should carry, including a
// bootstrap address when this device knows one.
func InviteURI(token, configDir string) (string, error) {
	s, err := invite.ParseToken(token)
	if err != nil {
		return "", err
	}
	_, stateDir := paths(configDir)
	boot := ""
	if st := liveState(stateDir); st != nil {
		if peers := st.BootPeers(time.Now()); len(peers) > 0 {
			boot = peers[0]
		}
	}
	return s.URIWithBoot(boot), nil
}

// AwaitInvite blocks until somebody redeems the token, and returns what they
// asked for as JSON.
//
// Requires a running mesh: the invite travels over the rendezvous plane, and
// holding one open means being subscribed to the topic the token names. A
// phone that is not connected cannot admit anybody, which is the same
// constraint the desktop has and worth failing on plainly.
func AwaitInvite(token string, timeoutSeconds int, meshLabel string) (string, error) {
	s, err := invite.ParseToken(token)
	if err != nil {
		return "", err
	}
	m, err := meshFor(meshLabel)
	if err != nil {
		return "", err
	}
	// Bounded at both ends as the desktop bounds it: an invite held for a day
	// is a token left lying around, and on a phone it is also a foreground
	// screen nobody is watching.
	if timeoutSeconds <= 0 || time.Duration(timeoutSeconds)*time.Second > maxInviteHold {
		timeoutSeconds = int(invite.DefaultTTL.Seconds())
	}
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	req, err := m.HoldInvite(ctx, s)
	if err != nil {
		return "", err
	}
	if req == nil {
		return "", errors.New("the invite expired before anyone used it")
	}
	out := InviteRequest{
		Name:      req.Name,
		DevicePub: hex.EncodeToString(req.DevicePub),
		WGPub:     hex.EncodeToString(req.WGPub),
		SealPub:   hex.EncodeToString(req.SealPub),
		EphPub:    hex.EncodeToString(req.EphPub),
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// AdmitWithCard signs a credential for a pending request and publishes it.
//
// The tap happens here, which is the whole shape of the feature: the credential
// names the joining device's own keys, so there is nothing to pre-sign and the
// card has to be present at this moment rather than any earlier one.
//
// lifeDays of zero means the default.
func AdmitWithCard(t CardTransport, configDir, pin, token, requestJSON, name string,
	lifeDays int, meshLabel string) (string, error) {

	s, err := invite.ParseToken(token)
	if err != nil {
		return "", err
	}
	var r InviteRequest
	if err := json.Unmarshal([]byte(requestJSON), &r); err != nil {
		return "", fmt.Errorf("unreadable request: %w", err)
	}
	devPub, err := hex.DecodeString(r.DevicePub)
	if err != nil {
		return "", fmt.Errorf("device key: %w", err)
	}
	wgPub, err := hex.DecodeString(r.WGPub)
	if err != nil {
		return "", fmt.Errorf("tunnel key: %w", err)
	}
	ephPub, err := hex.DecodeString(r.EphPub)
	if err != nil {
		return "", fmt.Errorf("ephemeral key: %w", err)
	}
	// Empty from a device that predates the control-plane sealing key, which
	// issues a version 1 credential exactly as before.
	sealPub, err := hex.DecodeString(r.SealPub)
	if err != nil {
		return "", fmt.Errorf("sealing key: %w", err)
	}
	if name == "" {
		name = r.Name
	}
	if name == "" {
		name = "device"
	}

	m, err := meshFor(meshLabel)
	if err != nil {
		return "", err
	}
	auth := m.Authority()
	if auth == nil {
		return "", errors.New("this mesh has no admin keys, so there is no credential to issue — " +
			"an invite here hands over the network key and admits nobody")
	}

	// Opened before the signature so a wrong PIN or an unpaired card is
	// reported while the invite is still open, rather than after it lapses.
	signer, err := keycard.NewSigner(t, configDir, pin)
	if err != nil {
		return "", err
	}
	if !auth.Has(signer.Public()) {
		return "", errors.New("this card is not an admin of that mesh — " +
			"its key is not in admin_keys")
	}

	life := time.Duration(lifeDays) * 24 * time.Hour
	if lifeDays <= 0 {
		life = cred.DefaultLife
	}
	// The same call the desktop makes, deliberately: serial semantics, clock
	// slack, which mesh id gets stamped, and the check that what the card
	// signed actually verifies all live in one place now
	// (internal/cred/issue.go). A phone issuing credentials by different rules
	// from a laptop would be a bug nobody found until the credentials met.
	raw, err := cred.IssueFor(signer, auth, devPub, wgPub, sealPub, name, 0, time.Now(), life)
	if err != nil {
		return "", err
	}

	req := &invite.Request{
		DevicePub: devPub, WGPub: wgPub, Name: name,
		EphPub: ephPub,
	}
	if err := m.ReplyInvite(s, req, raw); err != nil {
		return "", err
	}
	return fmt.Sprintf("admitted %s for %d days", name, int(life.Hours()/24)), nil
}

// meshFor picks the running mesh an invite belongs to.
//
// An empty label means the first, which is what a single-mesh device means and
// what the desktop's own endpoints accept.
func meshFor(label string) (*mesh.Mesh, error) {
	mu.Lock()
	s := running
	mu.Unlock()
	if s == nil || len(s.instances) == 0 {
		return nil, errors.New("this device is not connected, so it cannot hold an invite open — " +
			"an invite travels over the rendezvous plane and needs a running mesh")
	}
	if label == "" {
		return s.instances[0].mesh, nil
	}
	for _, in := range s.instances {
		if in.label == label {
			return in.mesh, nil
		}
	}
	return nil, fmt.Errorf("no mesh called %q on this device", label)
}
