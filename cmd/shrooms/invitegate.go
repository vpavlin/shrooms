package main

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/invite"
)

// May a caller that is not root complete an enrolment? (ADR-033)
//
// The question is not who is calling. Anybody in the socket group can already
// change this device's settings, and anybody at all can mint an invite token —
// `invite.New` needs no daemon. The question is whether the admin approved the
// device that is actually joining, and that is answerable from what the caller
// hands over.
//
// It matters because /invite/reply publishes the mesh's NETWORK KEY. That is
// the one thing on this socket the daemon gives away rather than relays: a peer
// receiving a credential checks the admin's signature for itself and refuses a
// forgery, but nothing downstream can check a network key. On a mesh with no
// admin keys it IS membership; on any other it is the whole control plane.
//
// So four conditions, and each exists for a different attack.
func groupMayReply(m inviteHolder, secret invite.Secret, credential []byte) error {
	auth := m.Authority()

	// 1. There has to be an authority at all, and it has to be a card.
	//
	// With no admin keys the network key is membership itself and nothing is
	// signed, so there is nothing to verify and no gate to build.
	//
	// With an admin key in a FILE, the gate protects much less than it appears
	// to: that file lives in a user's session, and a caller in the socket group
	// may well be that user. "The admin signed this" and "the caller could have
	// signed this" then collapse into one question. A card key cannot be used
	// without the card and its PIN, whoever the caller is running as.
	if auth == nil {
		return errors.New("this mesh has no admin keys, so an invite hands over " +
			"the network key and admits whoever holds the token. That needs root")
	}
	if !auth.CardOnly() {
		return errors.New("this mesh's admin key is a file rather than a card, " +
			"so signing it is not proof of anything this check could rely on. " +
			"That needs root")
	}

	// 2. There has to be a credential. A reply with none hands over the network
	// key and nothing else, which is precisely the thing being gated.
	if len(credential) == 0 {
		return errors.New("no credential: replying without one hands over the " +
			"network key on its own. That needs root")
	}
	c, err := cred.UnmarshalCredential(credential)
	if err != nil {
		return fmt.Errorf("unreadable credential: %w", err)
	}

	// 3. It has to be one THIS mesh's authority signed, and still valid.
	//
	// VerifyBy checks the mesh id, the signature against every admin key, and
	// the validity window. A credential for another mesh is somebody else's
	// business; an expired one proves the admin approved something once.
	if err := cred.VerifyBy(auth, c, time.Now()); err != nil {
		return fmt.Errorf("the credential does not verify against this mesh's "+
			"admin keys: %w", err)
	}

	// 4. It has to name the device THIS exchange is admitting.
	//
	// The one that does the work. Anybody may mint a token and walk a device of
	// their own through the exchange, then offer any credential they can lay
	// hands on — one issued to a different device, or to this device on another
	// occasion. Every such credential names somebody else's keys.
	//
	// The daemon knows which keys it handed out, because HoldInvite recorded
	// them. Both keys, not just the device key: a credential names a tunnel key
	// too, and admitting a device under somebody else's tunnel key is its own
	// kind of wrong.
	devicePub, wgPub, ok := m.Admitting(secret.Topic())
	if !ok {
		return errors.New("this daemon is not holding that invite, so it cannot " +
			"tell which device the credential is for. Hold the invite here, or " +
			"reply as root")
	}
	if !bytes.Equal(c.DevicePub, devicePub) {
		return errors.New("the credential names a different device than the one " +
			"that asked to join")
	}
	if !bytes.Equal(c.WGPub, wgPub) {
		return errors.New("the credential names a different tunnel key than the " +
			"one that asked to join")
	}
	return nil
}
