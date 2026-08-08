package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// statusFileInterval is how often the snapshot is rewritten. Matches what a
// human watching a dashboard can perceive; faster would only burn writes.
const statusFileInterval = 2 * time.Second

// writeStatusFile keeps a JSON snapshot on disk for a monitoring view.
//
// A file rather than a port. QML can read a file and cannot open a unix socket,
// and a VPN daemon opening a TCP listener so that a UI can be convenient is a
// poor trade — with a file, access is decided by permissions, which the
// operating system already enforces and everyone already understands.
//
// Written atomically: a reader polling this must never see half a document, and
// "sometimes the dashboard shows nothing" is a miserable bug to chase.
func writeStatusFile(ctx context.Context, log *slog.Logger, path string, snapshot func() statusPayload) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
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
		// Readable by anyone who can reach the directory: this names the mesh's
		// devices and addresses but holds no secret, and a dashboard running as
		// the desktop user has to be able to read it.
		if err := os.Chmod(tmp.Name(), 0o644); err != nil {
			return err
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
