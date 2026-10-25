// Package resolvconf reads the name servers the machine itself resolves with.
//
// The delivery library does not consult the system resolver: its
// dnsAddrsNameServers defaults to Cloudflare, queried directly on UDP/53, and
// that is how it resolves the fleet's /dns4/ entry nodes and probes whether it
// is online. A machine whose network permits only its own resolver (a VPN with
// DNS-leak protection, a corporate network) then fails both and reports itself
// offline while every other program on it works. Handing the library what
// /etc/resolv.conf says makes it resolve the way the machine does (ADR-071).
package resolvconf

import (
	"bufio"
	"net/netip"
	"os"
	"strings"
)

// Path is the file read when nothing else is named.
const Path = "/etc/resolv.conf"

// Nameservers returns the nameserver addresses in a resolv.conf, in file order.
//
// Only addresses the library can take: an IP, with no zone. A missing or
// unreadable file, or one naming no usable server, gives nil, which callers
// treat as "let the library keep its default". The stub resolver of
// systemd-resolved (127.0.0.53) is kept: it is reachable on loopback whatever
// a VPN's firewall allows, and it forwards to whatever the VPN set.
func Nameservers(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		addr, err := netip.ParseAddr(fields[1])
		if err != nil || addr.Zone() != "" {
			continue
		}
		out = append(out, addr.String())
	}
	return out
}
