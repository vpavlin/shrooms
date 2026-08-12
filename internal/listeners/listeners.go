// Package listeners finds what is listening on this device's mesh addresses.
//
// The point is a binding, not a scan. A service bound to the overlay address —
// sshd on fd3b:…:22 rather than on :: — is reachable by mesh members and by
// nothing else: not the LAN, not a café network, not the internet. That makes
// the bind itself the access control, which is stronger than anything the
// forwarder in internal/service can offer and needs no forwarder at all
// (ADR-026).
//
// What is missing in that arrangement is only that nobody is told. This finds
// the ports so they can be announced.
//
// Deliberately narrow: a socket bound to :: or to a LAN address is *not*
// mesh-only, and advertising it as though it were would be a lie about who can
// reach it. Only sockets whose local address is exactly one of ours count.
package listeners

import (
	"bufio"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/netip"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Listener is one socket bound to a mesh address.
type Listener struct {
	Addr  netip.Addr
	Port  uint16
	Proto string // "tcp" or "udp"

	// Name is what to call it, from the well-known port if there is one.
	// Advisory: the port is the fact, and the name is for reading.
	Name string
}

// Spec renders a listener the way it travels and is displayed: "ssh:22".
func (l Listener) Spec() string {
	name := l.Name
	if name == "" {
		name = "port-" + strconv.Itoa(int(l.Port))
	}
	return name + ":" + strconv.Itoa(int(l.Port))
}

// procTCP6 and procUDP6 are where Linux publishes this. Variables so a test can
// point them at a fixture; there is no portable API for the question and
// parsing /proc is what every tool that answers it does.
var (
	procTCP6 = "/proc/net/tcp6"
	procUDP6 = "/proc/net/udp6"
)

// listenState is TCP_LISTEN in the st column.
const listenState = "0A"

// On is what is listening on any of the given addresses.
//
// Sorted by port so the result is stable: this ends up in an announcement, and
// a list that reorders itself would look like a change every time.
func On(addrs []netip.Addr) ([]Listener, error) {
	want := map[netip.Addr]bool{}
	for _, a := range addrs {
		want[a] = true
	}

	var out []Listener
	tcp, err := parseProc(procTCP6, "tcp", want, true)
	if err != nil {
		return nil, err
	}
	out = append(out, tcp...)

	// UDP has no listen state — a bound socket is simply bound — so anything
	// on one of our addresses counts. WireGuard's own socket is not among them:
	// it binds the wildcard address, being how the mesh is reached rather than
	// something reachable on it.
	udp, err := parseProc(procUDP6, "udp", want, false)
	if err != nil {
		return nil, err
	}
	out = append(out, udp...)

	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].Proto < out[j].Proto
	})
	return out, nil
}

// parseProc reads one of the /proc tables.
func parseProc(path, proto string, want map[netip.Addr]bool, listening bool) ([]Listener, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			// A kernel without IPv6, or a container without the table. Not an
			// error: the answer is simply that nothing is bound.
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	var out []Listener
	seen := map[uint16]bool{}
	sc := bufio.NewScanner(f)
	for first := true; sc.Scan(); {
		if first { // the header row
			first = false
			continue
		}
		fields := strings.Fields(sc.Text())
		if len(fields) < 4 {
			continue
		}
		if listening && fields[3] != listenState {
			continue
		}
		addr, port, ok := parseLocal(fields[1])
		if !ok || !want[addr] {
			continue
		}
		// One entry per port: a service listening on two of our addresses is
		// one service, and announcing it twice says nothing extra.
		if seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, Listener{Addr: addr, Port: port, Proto: proto, Name: WellKnown(port)})
	}
	return out, sc.Err()
}

// parseLocal decodes the "local_address" column: an IPv6 address as four
// little-endian 32-bit words in hex, a colon, then the port in hex.
//
// The byte order is the part worth being careful about. /proc prints each
// 32-bit word in host order, so on a little-endian machine every group of four
// bytes is reversed relative to the address as written.
func parseLocal(s string) (netip.Addr, uint16, bool) {
	host, portHex, ok := strings.Cut(s, ":")
	if !ok || len(host) != 32 {
		return netip.Addr{}, 0, false
	}
	raw, err := hex.DecodeString(host)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	var b [16]byte
	for i := 0; i < 4; i++ {
		w := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
		binary.BigEndian.PutUint32(b[i*4:i*4+4], w)
	}
	port, err := strconv.ParseUint(portHex, 16, 16)
	if err != nil {
		return netip.Addr{}, 0, false
	}
	return netip.AddrFrom16(b), uint16(port), true
}

// WellKnown names a port, or returns "" when there is nothing useful to say.
//
// A short table rather than /etc/services: what matters is that the common
// cases read well, and a name is advisory anyway — the port is what a peer
// connects to. Anything unlisted is announced by its number, which is honest
// and still useful.
func WellKnown(port uint16) string {
	switch port {
	case 22:
		return "ssh"
	case 53:
		return "dns"
	case 80:
		return "http"
	case 443:
		return "https"
	case 445:
		return "smb"
	case 631:
		return "ipp"
	case 3000:
		return "dev"
	case 3389:
		return "rdp"
	case 5432:
		return "postgres"
	case 5900:
		return "vnc"
	case 8006:
		return "proxmox"
	case 8080, 8000:
		return "http-alt"
	case 8096:
		return "jellyfin"
	case 9090:
		return "cockpit"
	case 11434:
		return "ollama"
	}
	return ""
}

// String is for logs.
func (l Listener) String() string {
	return fmt.Sprintf("%s/%d %s", l.Proto, l.Port, l.Addr)
}
