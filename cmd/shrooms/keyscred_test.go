package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vpavlin/shrooms/internal/cred"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/state"
)

// `shrooms keys` used to print the single-mesh credential field and nothing
// else, so a device holding a per-mesh credential (ADR-015) was told about the
// one it had stopped announcing.
//
// Found on pi5 on 2026-09-08, which held both: a base credential named "jimmy"
// expiring on the 17th, and the mesh's own, renewed to 8 October, under the
// right name. `keys` reported the first as fact while every other view — the
// peer's `status`, `admin renew --dry-run`, and pi5's own log — agreed on the
// second. That is a diagnostic disagreeing with reality at the exact moment
// somebody is using it to decide whether to renew.

// twoCredentials builds pi5's arrangement: one device, one mesh, a stale blob
// in the single-mesh field and a current one in the mesh's own slot.
func twoCredentials(t *testing.T) (cfgPath, stateDir string, stale, live *cred.Credential) {
	t.Helper()
	dir := t.TempDir()
	cfgPath = filepath.Join(dir, "config.toml")
	stateDir = filepath.Join(dir, "state")

	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.DefaultConfig()
	cfg.Name = "pi5"
	cfg.NetworkKey = nk.String()
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	st, err := state.LoadOrCreateState(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	admin, err := cred.NewAdmin()
	if err != nil {
		t.Fatal(err)
	}
	auth, err := cred.NewAuthority(admin.Pub)
	if err != nil {
		t.Fatal(err)
	}

	// The old one: another name, an earlier expiry, exactly as pi5 had it.
	staleRaw, err := cred.IssueFor(admin, auth, st.Identity.DevicePub, st.Identity.WGPub[:],
		st.Identity.SealPub[:], "jimmy", 1787042643, time.Now(), 9*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	liveRaw, err := cred.IssueFor(admin, auth, st.Identity.DevicePub, st.Identity.WGPub[:],
		st.Identity.SealPub[:], "pi5", 1788850933, time.Now(), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	st.Credential = staleRaw
	ms, err := st.MeshState(state.NetworkID(nk), true)
	if err != nil {
		t.Fatal(err)
	}
	ms.Credential = liveRaw
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}

	if stale, err = cred.UnmarshalCredential(staleRaw); err != nil {
		t.Fatal(err)
	}
	if live, err = cred.UnmarshalCredential(liveRaw); err != nil {
		t.Fatal(err)
	}
	return cfgPath, stateDir, stale, live
}

// capture runs cmdKeys with stdout redirected, since what it PRINTS is the
// whole behaviour under test.
func capture(t *testing.T, args []string) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	err = cmdKeys(args)
	w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	b, _ := os.ReadFile("/dev/stdin")
	_ = b
	out := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, rerr := r.Read(buf)
		out = append(out, buf[:n]...)
		if rerr != nil {
			break
		}
	}
	return string(out)
}

func TestKeysReportsTheCredentialTheDeviceAnnounces(t *testing.T) {
	cfgPath, stateDir, stale, live := twoCredentials(t)

	out := capture(t, []string{"--config", cfgPath, "--state", stateDir})

	if !strings.Contains(out, live.Name) {
		t.Errorf("keys did not name the live credential %q:\n%s", live.Name, out)
	}
	if strings.Contains(out, stale.Name) {
		t.Errorf("keys reported the stale credential %q, which the device no longer announces:\n%s",
			stale.Name, out)
	}
	if !strings.Contains(out, "1788850933") {
		t.Errorf("keys did not report the live serial:\n%s", out)
	}
	if strings.Contains(out, "1787042643") {
		t.Errorf("keys reported the stale serial:\n%s", out)
	}
}

// A device that has never been enrolled still needs `keys` to work — that is
// what it is FOR, and it runs before any credential exists.
func TestKeysWithNoCredentialAtAll(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	nk, err := identity.NewNetworkKey()
	if err != nil {
		t.Fatal(err)
	}
	cfg := state.DefaultConfig()
	cfg.NetworkKey = nk.String()
	if err := state.WriteConfig(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	out := capture(t, []string{"--config", cfgPath, "--state", filepath.Join(dir, "state")})
	if !strings.Contains(out, "none yet") {
		t.Errorf("keys did not say there is no credential:\n%s", out)
	}
	if !strings.Contains(out, "shrooms admin issue") {
		t.Errorf("keys did not print the enrolment command, which is its main job:\n%s", out)
	}
}
