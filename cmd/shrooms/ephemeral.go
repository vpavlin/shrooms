package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Refusing to mint a mesh whose authority would not survive a restart.
//
// The admin keys are fixed when a mesh is minted: the mesh id is their hash and
// the overlay prefix derives from the id, so a key that is lost cannot be
// replaced and the mesh can never admit another device. Existing members keep
// working until their credentials expire, and then it is finished.
//
// Which makes writing that key to a container's own filesystem a way to destroy
// a mesh with no error at any point. The shipped node deployment mounts
// /etc/shrooms, /var/lib/shrooms and /run/shrooms and nothing else, while
// defaultAdminDir resolves to $HOME/.config/shrooms — /root/.config/shrooms for
// a root container, which is on the ephemeral layer. Podman recreates the
// container on every restart, so the key survives until the next one.
//
// Found on 2026-08-25, from a mesh whose admin_keys were configured on every
// member and whose private key was on no machine anybody could find.

// inContainer reports whether this process is running inside one.
//
// Both files are written by the runtime rather than by an image, so neither can
// be inherited from a base image by accident.
func inContainer() bool {
	for _, p := range []string{"/run/.containerenv", "/.dockerenv"} {
		if _, err := os.Stat(p); err == nil {
			return true
		}
	}
	return false
}

// deviceOf returns the filesystem a path is on, walking up to the nearest
// ancestor that exists — the admin directory usually does not yet.
func deviceOf(path string) (uint64, bool) {
	for {
		fi, err := os.Stat(path)
		if err == nil {
			st, ok := fi.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, false
			}
			return uint64(st.Dev), true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return 0, false
		}
		path = parent
	}
}

// ephemeralAdminDir reports whether writing an admin key here would lose it.
//
// The test is whether the directory is on the same filesystem as the container's
// root. A bind mount or a volume has its own device number; the image's own
// layer does not. Outside a container this is always false — a home directory on
// the root filesystem is exactly where an admin key belongs.
func ephemeralAdminDir(dir string) bool {
	if !inContainer() {
		return false
	}
	here, ok := deviceOf(dir)
	if !ok {
		return false
	}
	root, ok := deviceOf("/")
	if !ok {
		return false
	}
	return here == root
}

// refuseEphemeralMint stops a mesh being minted onto a disappearing filesystem.
func refuseEphemeralMint(dir string) error {
	if !ephemeralAdminDir(dir) {
		return nil
	}
	return fmt.Errorf("refusing to mint a mesh: %s is inside this container and "+
		"is not a mounted volume, so the admin key would be deleted the next time "+
		"the container is recreated.\n\n"+
		"That is not recoverable. A mesh's admin keys are fixed when it is minted "+
		"— the mesh id is their hash and the address prefix derives from the id — "+
		"so a lost key cannot be replaced, and the mesh can never admit another "+
		"device. Members keep working until their credentials expire, and then it "+
		"is over.\n\n"+
		"Either mint the mesh on the host rather than in here, or mount a volume "+
		"and point at it:\n"+
		"    -v /etc/shrooms/admin:/etc/shrooms/admin\n"+
		"    shrooms admin init --dir /etc/shrooms/admin", dir)
}
