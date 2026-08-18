package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"time"
)

// statusFileInterval is how often the snapshot is rewritten. Matches what a
// human watching a dashboard can perceive; faster would only burn writes.
const statusFileInterval = 2 * time.Second

// writeStatusFile keeps a JSON snapshot on disk for a monitoring view.
//
// A file rather than a port. QML can read a file and cannot open a unix socket,
// and a VPN daemon opening a TCP listener so that a UI can be convenient is a
// lookupGroup resolves a group by name or numeric gid.
//
// Numeric first: a container often has no /etc/group entry for the id it runs
// under, and refusing to accept a plain number there would make this unusable
// in exactly the deployment that needs it most.
func lookupGroup(g string) (int, error) {
	if gid, err := strconv.Atoi(g); err == nil {
		return gid, nil
	}
	found, err := user.LookupGroup(g)
	if err != nil {
		// A username is the obvious thing to write, and on most systems there
		// is a group of the same name. Try that before giving up.
		if u, uerr := user.Lookup(g); uerr == nil {
			if gid, cerr := strconv.Atoi(u.Gid); cerr == nil {
				return gid, nil
			}
		}
		return 0, err
	}
	return strconv.Atoi(found.Gid)
}

// poor trade — with a file, access is decided by permissions, which the
// operating system already enforces and everyone already understands.
//
// Written atomically: a reader polling this must never see half a document, and
// "sometimes the dashboard shows nothing" is a miserable bug to chase.
func writeStatusFile(ctx context.Context, log *slog.Logger, path string, group string, snapshot func() statusPayload) error {
	dir := filepath.Dir(path)
	// 0750: the directory listing leaks nothing by itself, but a world-x
	// directory holding a 0640 file invites the next person to "fix" the file
	// mode rather than the group.
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	// Resolve the reader group once. A file mode of 0640 is only useful with a
	// group somebody is actually in: the daemon runs as root, so without this
	// the file is root:root and the desktop view that the file exists for
	// cannot read it.
	gid := -1
	if group != "" {
		g, err := lookupGroup(group)
		if err != nil {
			log.Warn("status file group not applied; only root will be able to read it",
				"group", group, "err", err)
		} else {
			gid = g
			// The group needs search permission on the directory too, or the
			// file inside it is unreachable whatever its own mode says.
			if err := os.Chown(dir, -1, gid); err != nil {
				log.Warn("could not set the group on the status directory", "dir", dir, "err", err)
			}
		}
	} else {
		log.Warn("status_file is set without status_file_group; "+
			"only root will be able to read it",
			"hint", "status_file_group = \"<your-username>\"")
	}

	write := func() error {
		payload, err := json.MarshalIndent(snapshot(), "", "  ")
		if err != nil {
			return err
		}
		tmp, err := os.CreateTemp(dir, ".status-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())

		if _, err := tmp.Write(payload); err != nil {
			tmp.Close()
			return err
		}
		if err := tmp.Close(); err != nil {
			return err
		}
		// 0640, matching the control socket. The old 0644 was justified as
		// "holds no secret", which conflated secrets with sensitive data: this
		// names every device on the mesh and each peer's real external
		// IP:port, so on a shared host any local user could read the whole
		// topology and where each machine physically is.
		//
		// The group is what makes it readable by a dashboard that is not root,
		// which is the same mechanism and the same decision as the socket —
		// the same reasoning that kept the removed HTTP endpoint loopback-only.
		if err := os.Chmod(tmp.Name(), 0o640); err != nil {
			return err
		}
		// Before the rename, so a reader never sees the file with the wrong
		// owner — the write is atomic and the ownership has to be part of it.
		if gid >= 0 {
			if err := os.Chown(tmp.Name(), -1, gid); err != nil {
				return fmt.Errorf("set group on the status file: %w", err)
			}
		}
		return os.Rename(tmp.Name(), path)
	}

	if err := write(); err != nil {
		return err
	}

	go func() {
		t := time.NewTicker(statusFileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				// Leave nothing behind claiming a mesh that is no longer up.
				_ = os.Remove(path)
				return
			case <-t.C:
				if err := write(); err != nil {
					log.Debug("could not write the status file", "path", path, "err", err)
				}
			}
		}
	}()
	return nil
}
