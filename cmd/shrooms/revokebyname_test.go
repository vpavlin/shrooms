package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Revoking wanted a hex that nothing on the admin machine printed.
//
// `shrooms keys` prints a device's key on the device itself, which is fine for
// a laptop you still have and useless for the phone you are removing. The
// roster knows both, so --name asks it.

func rosterSocket(t *testing.T, body string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sk")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

const twoMeshRoster = `{"peers":[
  {"name":"nothing","mesh":"kc2","device":"aabbccddeeff00112233445566778899aabbccddeeff001122334455667788ff"},
  {"name":"nothing","mesh":"home","device":"1111111111111111111111111111111111111111111111111111111111111111"},
  {"name":"k11","mesh":"home","device":"2222222222222222222222222222222222222222222222222222222222222222"}
]}`

func TestRevokeResolvesANameOnOneMesh(t *testing.T) {
	sock := rosterSocket(t, twoMeshRoster)
	got, err := deviceByName(sock, "kc2", "nothing")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "aabbccdd") {
		t.Errorf("resolved to %s, want the kc2 device", got)
	}
}

// A name on several meshes must not be guessed at. Revoking the wrong device is
// not an error anybody notices quickly: the mesh keeps working and one machine
// quietly stops being a member.
func TestRevokeRefusesAnAmbiguousName(t *testing.T) {
	sock := rosterSocket(t, twoMeshRoster)
	_, err := deviceByName(sock, "", "nothing")
	if err == nil {
		t.Fatal("picked one of two devices called \"nothing\"")
	}
	if !strings.Contains(err.Error(), "--mesh") {
		t.Errorf("the error does not say how to settle it: %v", err)
	}
	// Both meshes named, so the reader can tell which is which.
	for _, want := range []string{"kc2", "home"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q missing from: %v", want, err)
		}
	}
}

func TestRevokeSaysWhenANameIsUnknown(t *testing.T) {
	sock := rosterSocket(t, twoMeshRoster)
	_, err := deviceByName(sock, "", "laptop-2")
	if err == nil {
		t.Fatal("resolved a name that is not in the roster")
	}
	// The way out, for a device that never announced.
	if !strings.Contains(err.Error(), "shrooms keys") {
		t.Errorf("no fallback offered: %v", err)
	}
}

// With no daemon there is no roster, and the error has to point at --device
// rather than leaving somebody stuck.
func TestRevokeWithNoDaemonPointsAtTheHex(t *testing.T) {
	_, err := deviceByName(filepath.Join(t.TempDir(), "absent.sock"), "", "nothing")
	if err == nil {
		t.Fatal("resolved a name with no daemon running")
	}
	if !strings.Contains(err.Error(), "--device") {
		t.Errorf("no way forward offered: %v", err)
	}
}
