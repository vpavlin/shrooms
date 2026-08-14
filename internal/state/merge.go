package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

// Two processes own this file: the daemon, which writes it on every announce,
// and the admin commands, which write a credential or a new mesh into it. They
// are not coordinated, and a whole-file write from either one deleted whatever
// the other had done since it last read.
//
// So writes take a lock and merge rather than replace. The rules below are all
// the same shape — never drop, never go backwards — because the cost of the two
// mistakes is not symmetric. Keeping a value that has since been superseded
// costs one stale announce. Dropping one costs an identity nobody can sign for,
// which is unrecoverable without re-enrolling by hand.

// readStateFile reads the on-disk form without interpreting it.
//
// Deliberately not LoadOrCreateState: this must not create anything, must not
// fail on a file a newer binary wrote, and must not care whether the keys in it
// are valid — it is one half of a merge, not a state to run on.
func readStateFile(path string) (stateFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return stateFile{}, err
	}
	var sf stateFile
	if err := json.Unmarshal(raw, &sf); err != nil {
		return stateFile{}, err
	}
	return sf, nil
}

// mergeStateFiles combines what is on disk with what this process holds.
//
// Ours wins where both have a value, because this process is the one running on
// it. Disk wins where we have nothing, because something else put it there and
// we have no business deleting it.
func mergeStateFiles(disk, ours stateFile) stateFile {
	out := ours

	// The sequence number only ever goes up. A device whose Seq regresses is
	// rejected by every peer's ReplayGuard until they forget it, which looks
	// exactly like the device having vanished — so a concurrent writer that
	// advanced it further than we know about is followed, not overruled.
	if disk.Seq > out.Seq {
		out.Seq = disk.Seq
	}

	// The identity is deliberately not merged: ours is the one the daemon is
	// running on, and a file holding a different one means something created a
	// second identity for this device. Overwriting it is what happened before;
	// preferring ours at least keeps the running node consistent with itself.

	if out.Credential == "" {
		out.Credential = disk.Credential
	}
	if out.Master == "" {
		out.Master = disk.Master
	}
	if len(out.Services) == 0 {
		out.Services = disk.Services
	}

	for id, dm := range disk.Meshes {
		om, held := out.Meshes[id]
		if !held {
			// A mesh this process has never heard of, because it was added
			// after we loaded. This single line is the bug that cost an
			// identity: it used to be dropped.
			if out.Meshes == nil {
				out.Meshes = make(map[string]meshStateFile, len(disk.Meshes))
			}
			out.Meshes[id] = dm
			continue
		}
		if dm.Seq > om.Seq {
			om.Seq = dm.Seq
		}
		if om.Credential == "" {
			om.Credential = dm.Credential
		}
		if om.DevicePriv == "" {
			om.DevicePriv, om.WGPriv = dm.DevicePriv, dm.WGPriv
		}
		if len(om.Services) == 0 {
			om.Services = dm.Services
		}
		out.Meshes[id] = om
	}
	return out
}

// lockFile takes an exclusive lock covering this state directory.
//
// A separate file rather than state.json itself, because the write path is
// write-to-temp-and-rename: a lock held on state.json would be a lock on an
// inode that is about to stop being the state.
//
// A failure to lock is not a failure to save. Locking is what makes the merge
// atomic against another process rather than merely correct against a stale
// read, and on a filesystem that does not support it — or a state directory
// that is somehow read-only for locking — writing without one is still better
// than not writing at all.
func (s *State) lockFile() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.dir, "state.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}
