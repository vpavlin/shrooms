package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"syscall"
)

// Who may do what over the control socket.
//
// The socket is 0660 with a group, so that `shrooms status` does not need root
// (see serveControl). Reading the roster is what that group was opened up for.
// Changing the mesh is not: joining, revoking and holding an invite open all
// reconfigure the VPN, and a group that was granted read access to a status
// page should not thereby gain the ability to enrol the machine into somebody
// else's mesh.
//
// So the mutating endpoints check the peer's credentials rather than trusting
// the file mode. SO_PEERCRED is decided by the kernel at connect time and
// cannot be forged by the caller, which is why it is worth more here than
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
					"The socket group can read status and nothing else.", cred.Uid),
				http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
