package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
)

// Creating things in somebody's home while running as root.
//
// defaultAdminDir resolves to the INVOKING user's home under sudo, deliberately:
// an admin key belongs to the person rather than to the machine, so
// `sudo shrooms admin init` should not bury it in /root. What it did not do is
// own the directory it made, so root created ~/.config/shrooms mode 0700 and
// every later command run without sudo — which is most of them — got
// "permission denied" on a path in the user's own home.
//
// Found the hard way: `keycard pair` paired with the card, could not write the
// pairing beside the admin key, and left a slot consumed on a card that has
// five of them.
func ensureUserDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return giveToUser(path)
}

// giveToUser hands a path root just created to whoever invoked sudo.
//
// The directory is not enough. `sudo shrooms init --keycard` chowned
// ~/.config/shrooms and then wrote admin-kc.json into it as root, so the mesh
// worked but `shrooms admin show` reported it "unreadable: permission denied"
// beside three meshes it could read — the file being the one thing in the
// directory its owner could not open.
//
// Best effort throughout: a file owned by root beats no file, and every caller
// has already written the thing by the time this runs. The one error worth
// returning is a chown that fails for a reason other than not-applicable,
// because that is the case where the caller believes it handed something over
// and did not.
func giveToUser(path string) error {
	// Only when root is standing in for somebody. Without SUDO_USER this is
	// root acting for itself, and /root is where its things belong.
	name := os.Getenv("SUDO_USER")
	if name == "" || os.Geteuid() != 0 {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	uid, err1 := strconv.Atoi(u.Uid)
	gid, err2 := strconv.Atoi(u.Gid)
	if err1 != nil || err2 != nil {
		return nil
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return fmt.Errorf("created %s but could not give it to %s: %w", path, name, err)
	}
	return nil
}
