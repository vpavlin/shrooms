package state

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// The config is edited by read-modify-write from several places at once: every
// /config/* endpoint on the control socket, /join, and the CLI in another
// process entirely. Nothing serialised them, and the write itself was a bare
// os.WriteFile.
//
// That is two failures, not one. Two concurrent edits lose an update — the
// second writer loaded before the first wrote, so the first change silently
// disappears. And a write interrupted partway leaves a truncated file: this one
// holds the network key, so a torn write can drop it, and a config that fails
// Validate is a daemon that will not start until somebody repairs it by hand.
//
// state.json already had both properties. The config now matches it.

// UpdateConfig applies fn to the config on disk, with nobody else able to write
// it in between.
//
// fn receives the loaded config and mutates it. Returning an error abandons the
// change and leaves the file untouched, which is what validation failures want:
// a config that would not load must never reach the disk.
//
// Loaded unvalidated on purpose. An existing file that no longer validates —
// written by an older build, or hand-edited — must still be repairable through
// this path, and refusing to read it would make the one interface that could
// fix it the one that cannot.
func UpdateConfig(path string, fn func(*Config) error) error {
	release, err := lockConfig(path)
	if err == nil {
		defer release()
	}

	cfg, err := LoadConfigUnvalidated(path)
	if err != nil {
		return err
	}
	if err := fn(&cfg); err != nil {
		return err
	}
	return WriteConfig(path, cfg)
}

// lockConfig serialises config writers, across processes as well as goroutines.
//
// A lock file beside the config rather than the config itself: WriteConfig
// replaces the file by rename, so a lock held on it would be a lock on an inode
// that has stopped being the config.
//
// Failing to lock is not failing to write. The lock upgrades a lost update into
// an ordered one; on a filesystem that cannot lock, an unserialised write is
// still better than refusing to save a setting somebody just changed.
func lockConfig(path string) (func(), error) {
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		// A read-only or missing directory. WriteConfig will report it
		// properly a moment later; there is nothing useful to say here.
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

// writeFileAtomic replaces path with body, or leaves it exactly as it was.
//
// Same shape as the state writer: a uniquely-named temp file in the same
// directory, chmod before the rename so the config is never briefly readable at
// the default mode, and rename last because it is the only step that is atomic.
func writeFileAtomic(path string, body []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmpf, err := os.CreateTemp(dir, filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := tmpf.Name()
	defer os.Remove(tmp) // no-op once renamed

	if _, err := tmpf.Write(body); err != nil {
		tmpf.Close()
		return err
	}
	if err := tmpf.Chmod(mode); err != nil {
		tmpf.Close()
		return err
	}
	if err := tmpf.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}
