package agent

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync/atomic"
	"time"
)

// UploadsKeep is how long a file sent to a session is kept (ADR-078): what a
// phone uploads, and what other agents send with send_file. 0 keeps them for
// good.
//
// Nothing removed them before: on 2026-11-01 five machines held 1.1 GB of
// them, a third older than two weeks, and every file any agent ever sent
// stayed on its receiver's disk.
var DefaultUploadsKeep = 14 * 24 * time.Hour

var uploadsKeep atomic.Int64 // nanoseconds

func init() { uploadsKeep.Store(int64(DefaultUploadsKeep)) }

// SetUploadsKeep sets how long uploads are kept; 0 keeps them for good.
func SetUploadsKeep(d time.Duration) { uploadsKeep.Store(int64(d)) }

func (m *Manager) uploadsRoot() string { return filepath.Join(m.dir, "uploads") }

// pruneUploads removes uploaded and received files older than the keeping,
// then the folders that left empty. It returns how many files and bytes.
func (m *Manager) pruneUploads(now time.Time) (files int, bytes int64) {
	keep := time.Duration(uploadsKeep.Load())
	if keep <= 0 {
		return 0, 0
	}
	root := m.uploadsRoot()
	var dirs []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root {
				dirs = append(dirs, p)
			}
			return nil
		}
		fi, err := d.Info()
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < keep {
			return nil
		}
		if os.Remove(p) == nil {
			files++
			bytes += fi.Size()
		}
		return nil
	})
	// Deepest first, so a drop folder emptied empties its parents.
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		os.Remove(d) // only if empty
	}
	return files, bytes
}

// keepUploads prunes uploads now and then every hour.
func (m *Manager) keepUploads() {
	for {
		if n, b := m.pruneUploads(time.Now()); n > 0 {
			m.log.Info("uploads past their keeping removed", "files", n, "bytes", b,
				"keep", time.Duration(uploadsKeep.Load()))
		}
		time.Sleep(time.Hour)
	}
}
