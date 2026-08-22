package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
)

// Who may do what over the control socket (ADR-025).
//
// Two tiers, and the line between them is not "does it change anything" — it
// is what the daemon can do on its own.
//
// **The socket group.** The socket is 0660 with a group, so a desktop app can
// reach it without sudo. That group may drive everything the daemon holds by
// itself: the roster, this device's own settings, joining a mesh it has been
// invited to, leaving one, starting an invite. This is the Docker model, said
// plainly: membership of the group is a serious grant and the documentation
// says so, rather than a permission that pretends to be small.
//
// What bounds it is [ADR-018](../../docs/adr/018-credentials-instead-of-a-shared-key.md).
// On a mesh with admin keys, minting an invite hands over the network key and
// admits nobody: a device is a member only once the admin key has signed a
// credential naming it, and that key is a passphrase-protected file in
// somebody's home directory, not something the socket can reach. So the group
// can expose the control plane's confidentiality; it cannot make a device a
// member, and it cannot remove one.
//
// **Root, or the user the daemon runs as.** Everything that would let the
// socket alone rewrite who belongs: installing a credential for this device,
// and anything else that decides membership without a signature to check.
//
// SO_PEERCRED is what decides the second tier, because it is set by the kernel
// at connect time and cannot be forged by the caller — worth more than
// anything in the request.

type peerCredKey struct{}

// withPeerCred stashes the connecting process's credentials on the context.
// Used as http.Server.ConnContext, which is the only place a handler can still
// reach the connection.
func withPeerCred(ctx context.Context, c net.Conn) context.Context {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return ctx
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return ctx
	}
	var cred *syscall.Ucred
	_ = raw.Control(func(fd uintptr) {
		cred, _ = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	})
	if cred == nil {
		return ctx
	}
	return context.WithValue(ctx, peerCredKey{}, cred)
}

// requireRoot wraps a handler so that only root, or the user the daemon itself
// runs as, may reach it.
//
// Not uid 0 alone: the daemon needs CAP_NET_ADMIN rather than root as such, and
// running it as its own user with ambient capabilities is a reasonable thing to
// want. That user must still be able to administer it, or the rule would forbid
// the owner and permit nobody.
//
// Fails closed. A connection whose credentials could not be read is refused,
// because "we could not tell who this is" is not a reason to let someone
// reconfigure the tunnel.
func requireRoot(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cred, _ := r.Context().Value(peerCredKey{}).(*syscall.Ucred)
		if cred == nil {
			http.Error(w, "cannot determine who is calling", http.StatusForbidden)
			return
		}
		if cred.Uid != 0 && cred.Uid != uint32(os.Getuid()) {
			http.Error(w, fmt.Sprintf(
				"this changes the mesh, so it needs root; you are uid %d.\n"+
					"This endpoint needs root; the socket group has a wider tier than "+
					"status, but not this.", cred.Uid),
				http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
