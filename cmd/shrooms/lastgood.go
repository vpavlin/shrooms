package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/vpavlin/shrooms/internal/state"
)

// The last config that started (ADR-080).
//
// A config edit used to be able to keep the daemon down: refused at load, or
// accepted and then failing a mesh at startup, and the daemon exited, every
// mesh with it, including the one the machine was reached over to fix it. On
// k11 (2026-11-04) one edited lan_hosts line did that, with the owner away
// from home and no other way in. So the daemon keeps a copy of the config it
// last started cleanly with, and starts on that copy, loudly, when the one in
// place is refused. The edited file is never touched: it is the person's
// work, and the fallback lasts until they fix it.

// lastGoodPath is the copy of the config the daemon last started cleanly
// with, beside the config: relative paths in it resolve the same way.
func lastGoodPath(cfgPath string) string { return cfgPath + ".last-good" }

// refusedPath records a config that loaded but failed to start a mesh, so the
// next start (systemd's, seconds later) knows to use the last good one.
func refusedPath(cfgPath string) string { return cfgPath + ".refused" }

type refusedConfig struct {
	SHA256 string    `json:"sha256"`
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// loadConfigOrLastGood loads the config, or the last good copy when the
// config is refused: at load, or because it failed to start before
// (refusedPath). fallback is why, empty when the config itself is used.
// With no usable copy, the config's own error is returned, as before.
func loadConfigOrLastGood(log *slog.Logger, cfgPath string) (cfg state.Config, raw []byte, fallback string, err error) {
	raw, _ = os.ReadFile(cfgPath)
	cfg, err = state.LoadConfig(cfgPath)
	reason := ""
	if err != nil {
		reason = err.Error()
	} else if r, ok := readRefused(cfgPath); ok {
		if r.SHA256 == sum(raw) {
			reason = "it failed to start: " + r.Reason
		} else {
			// Edited since: the refusal was about another file.
			_ = os.Remove(refusedPath(cfgPath))
		}
	}
	if reason == "" {
		return cfg, raw, "", nil
	}
	good, gerr := state.LoadConfig(lastGoodPath(cfgPath))
	if gerr != nil {
		if errors.Is(gerr, fs.ErrNotExist) {
			log.Error("config refused, and there is no last good copy to start on", "config", cfgPath, "reason", reason)
		} else {
			log.Error("config refused, and the last good copy is unusable too", "config", cfgPath,
				"reason", reason, "copy", lastGoodPath(cfgPath), "err", gerr)
		}
		if err != nil {
			return cfg, raw, "", err
		}
		// Refused at startup before, and nothing to fall back to: try it
		// again, as a daemon without this file always has.
		return cfg, raw, "", nil
	}
	log.Error("config refused: running on the last config that started; fix the config and restart",
		"config", cfgPath, "reason", reason, "running", lastGoodPath(cfgPath))
	return good, raw, reason, nil
}

func readRefused(cfgPath string) (refusedConfig, bool) {
	var r refusedConfig
	b, err := os.ReadFile(refusedPath(cfgPath))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}

// noteRefused records that this config failed to start a mesh, when there is
// a different last good copy to start on instead. It says whether it did.
func noteRefused(cfgPath string, raw []byte, reason error) bool {
	good, err := os.ReadFile(lastGoodPath(cfgPath))
	if err != nil || len(raw) == 0 || sum(good) == sum(raw) {
		return false
	}
	b, _ := json.Marshal(refusedConfig{SHA256: sum(raw), Reason: reason.Error(), At: time.Now().UTC()})
	return writeBeside(refusedPath(cfgPath), b, 0o600) == nil
}

// saveLastGood keeps raw as the last good config, after a clean start, with
// the config's own permissions (it holds the mesh keys). Only when changed.
func saveLastGood(cfgPath string, raw []byte) error {
	if len(raw) == 0 {
		return nil
	}
	_ = os.Remove(refusedPath(cfgPath))
	if old, err := os.ReadFile(lastGoodPath(cfgPath)); err == nil && sum(old) == sum(raw) {
		return nil
	}
	mode := fs.FileMode(0o600)
	if fi, err := os.Stat(cfgPath); err == nil {
		mode = fi.Mode().Perm()
	}
	return writeBeside(lastGoodPath(cfgPath), raw, mode)
}

// writeBeside writes atomically: a copy cut short by a crash would be a last
// good config that is neither.
func writeBeside(path string, b []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("keeping %s: %w", path, err)
	}
	return nil
}
