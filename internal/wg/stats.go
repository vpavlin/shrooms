package wg

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/vpavlin/logos-vpn/internal/identity"
)

// PeerStat is the data-plane view of a peer, read back from wireguard-go.
//
// This is the half of `status` that gossip cannot tell you: the roster says who
// exists, this says whether we can actually reach them.
type PeerStat struct {
	WGPub         identity.WGKey
	Endpoint      string
	LastHandshake time.Time
	RxBytes       uint64
	TxBytes       uint64
	AllowedIPs    []string
}

// Handshaked reports whether a handshake has ever completed.
func (p PeerStat) Handshaked() bool { return !p.LastHandshake.IsZero() }

// PeerStats reads peer state over the UAPI.
func (d *Device) PeerStats() (map[string]PeerStat, error) {
	var sb strings.Builder
	if err := d.IpcGetOperation(&sb); err != nil {
		return nil, fmt.Errorf("read device state: %w", err)
	}

	out := make(map[string]PeerStat)
	var cur *PeerStat
	var hsSec, hsNsec int64

	flush := func() {
		if cur == nil {
			return
		}
		if hsSec != 0 || hsNsec != 0 {
			cur.LastHandshake = time.Unix(hsSec, hsNsec)
		}
		out[cur.WGPub.String()] = *cur
		cur, hsSec, hsNsec = nil, 0, 0
	}

	for _, line := range strings.Split(sb.String(), "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "public_key":
			flush()
			raw, err := hex.DecodeString(val)
			if err != nil || len(raw) != 32 {
				continue
			}
			var k identity.WGKey
			copy(k[:], raw)
			cur = &PeerStat{WGPub: k}
		case "endpoint":
			if cur != nil {
				cur.Endpoint = val
			}
		case "last_handshake_time_sec":
			hsSec, _ = strconv.ParseInt(val, 10, 64)
		case "last_handshake_time_nsec":
			hsNsec, _ = strconv.ParseInt(val, 10, 64)
		case "rx_bytes":
			if cur != nil {
				cur.RxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "tx_bytes":
			if cur != nil {
				cur.TxBytes, _ = strconv.ParseUint(val, 10, 64)
			}
		case "allowed_ip":
			if cur != nil {
				cur.AllowedIPs = append(cur.AllowedIPs, val)
			}
		}
	}
	flush()
	return out, nil
}
