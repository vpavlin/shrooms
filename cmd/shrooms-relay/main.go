// Command shrooms-relay forwards packets for meshes it is not a member of.
//
// A blind relay (docs/blind-relays.md). It holds no network key, has no mesh
// identity, and cannot read a byte of what it carries — the packets are
// WireGuard, encrypted between two devices whose keys it does not have. What it
// sees is opaque per-relay tags, the addresses they connect from, and which
// tags exchange how much traffic and when.
//
// Deliberately not the daemon. The daemon links the Logos Delivery library, a
// WireGuard implementation and a control socket, none of which a relay uses; it
// needs a UDP port and a map. So this is a separate binary with no cgo, which
// makes it a few megabytes on scratch and lets it run anywhere a container runs
// — including the ephemeral compute this was written for, where the usual
// obstacle is a persistent volume.
//
// It needs no volume. There is no identity to keep and no state to lose: the
// forwarding table is soft state, rebuilt from registrations within one refresh
// interval, so a redeployed relay costs a brief outage and nothing else.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vpavlin/shrooms/internal/relay"
)

// readBuf bounds one datagram. A relay frame is a header plus a WireGuard
// packet, so this is generous by a wide margin.
const readBuf = 65535

// statEvery is how often the relay says what it has been doing.
//
// An operator running one for strangers has no other way to see it working, and
// the counters are the whole diagnostic surface: registrations that never
// become forwards mean devices that cannot receive, and a rising throttle count
// means the ceilings are doing their job rather than the machine failing.
var statEvery = 5 * time.Minute

// heartbeat is the longest a healthy relay stays silent.
//
// Long enough that an idle one is not chatter, short enough that an operator
// checking in the morning can see it was alive overnight.
const heartbeat = time.Hour

// statInterval overrides the reporting interval, for seeing the output without
// waiting five minutes for it.
func init() {
	if v := os.Getenv("SHROOMS_RELAY_STAT_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			statEvery = d
		}
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "shrooms-relay:", err)
		os.Exit(1)
	}
}

func run() error {
	// Flags with environment fallbacks, because the deployment descriptors this
	// is meant for (Akash SDL, compose, k8s) all pass configuration as
	// environment and none of them pass argv comfortably.
	fs := flag.NewFlagSet("shrooms-relay", flag.ExitOnError)
	port := fs.Int("port", envInt("SHROOMS_RELAY_PORT", 51820), "UDP port to listen on")
	token := fs.String("token", os.Getenv("SHROOMS_RELAY_TOKEN"),
		"require this token; empty means open to anyone")
	maxReg := fs.Int("max-registrations", envInt("SHROOMS_RELAY_MAX_REGISTRATIONS", relay.MaxRegistrations),
		"how many devices may be registered at once")
	maxSrc := fs.Int("max-per-source", envInt("SHROOMS_RELAY_MAX_PER_SOURCE", 8),
		"how many registrations one source IP may hold; 0 for unlimited")
	rate := fs.Int64("bytes-per-second", envInt64("SHROOMS_RELAY_BYTES_PER_SECOND", 0),
		"total forwarding ceiling; 0 for unlimited")
	peerRate := fs.Int64("peer-bytes-per-second", envInt64("SHROOMS_RELAY_PEER_BYTES_PER_SECOND", 0),
		"per-device forwarding ceiling; 0 for unlimited")
	// Checking a relay from outside is the same binary, because the thing you
	// want to check is usually somewhere you cannot run a shell.
	probeAt := fs.String("probe", "",
		"do not serve; check that the relay at this host:port forwards, and exit")
	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}
	if *probeAt != "" {
		return probe(*probeAt, *token)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Open or token, and the difference is policy rather than safety: an open
	// relay is not usable as a reflector because a registration installs
	// nothing until the registrant proves it receives where it claims. A token
	// decides *who* may use the bandwidth, which is the operator's business.
	key := relay.OpenKey()
	how := "open to anyone"
	if *token != "" {
		key = relay.TokenKey(*token)
		how = "token required"
	}

	srv := relay.NewServerWith(key, nil, relay.Options{
		Blind:                 true,
		Open:                  *token == "",
		MaxRegistrations:      *maxReg,
		MaxPerSource:          *maxSrc,
		BytesPerSecond:        *rate,
		PerPeerBytesPerSecond: *peerRate,
	})

	// Dual-stack on the unspecified address: a container gets whatever the host
	// gives it and binding a specific one means guessing.
	pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: *port})
	if err != nil {
		return fmt.Errorf("listen on :%d: %w", *port, err)
	}
	defer pc.Close()

	log.Info("relaying", "port", *port, "access", how,
		"max_registrations", *maxReg, "max_per_source", *maxSrc,
		"bytes_per_second", *rate, "peer_bytes_per_second", *peerRate)
	if *rate == 0 && *token == "" {
		// Said once, plainly. An open relay with no ceiling is somebody else's
		// bandwidth bill waiting to happen, and the operator should have
		// decided that rather than inherited it.
		log.Warn("open with no bandwidth ceiling — anyone may use as much as they like; " +
			"set -bytes-per-second unless that is what you meant")
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	started := time.Now()
	go report(ctx, log, srv, started)
	serve(ctx, log, pc, srv)
	log.Info("stopped", "stats", fmt.Sprintf("%+v", srv.Stats()))
	return nil
}

// serve is the whole relay: read a datagram, hand it to the table, send back
// whatever the table says to send.
//
// Split out from run so a test can drive it over a real socket. The forwarding
// rules are covered in internal/relay; what this adds and what is worth testing
// here is the part that only exists once there is a wire — that a reply goes
// back to the right address, and that a v4 peer on a dual-stack socket is the
// same peer each time it arrives.
func serve(ctx context.Context, log *slog.Logger, pc *net.UDPConn, srv *relay.Server) {
	go func() {
		<-ctx.Done()
		// Unblocks the read below, which is otherwise parked in the kernel and
		// deaf to the context.
		_ = pc.Close()
	}()

	buf := make([]byte, readBuf)
	for {
		n, from, err := pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			// A single bad read is not fatal — an ICMP error can surface here
			// on some platforms — but a storm of them would spin.
			log.Warn("read", "err", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		out, to, send := srv.Handle(buf[:n], normalise(from), time.Now())
		if send {
			if _, err := pc.WriteToUDPAddrPort(out, to); err != nil {
				log.Debug("send", "to", to, "err", err)
			}
		}
	}
}

// normalise unwraps a v4-mapped v6 address.
//
// A dual-stack socket reports an IPv4 peer as ::ffff:a.b.c.d, and the same
// device arriving once through each path would otherwise look like two — which
// matters because registrations and the per-source cap are keyed by address.
func normalise(a netip.AddrPort) netip.AddrPort {
	if a.Addr().Is4In6() {
		return netip.AddrPortFrom(a.Addr().Unmap(), a.Port())
	}
	return a
}

// report says what the relay has been doing, periodically.
//
// An operator running one for strangers has no other view of it, so this is the
// whole diagnostic surface — and what makes it useful is the interval, not the
// totals. A cumulative "forwarded=2" says nothing about whether the last five
// minutes were busy or dead; a rate does.
//
// Aggregates only, deliberately. A blind relay's claim is that its operator
// learns as little as possible about who is using it, and per-tag or per-address
// lines in a log file would be precisely the traffic-analysis surface the design
// works to keep small. An operator needs to know how much of their bandwidth is
// being used and by how many people — not by whom.
func report(ctx context.Context, log *slog.Logger, srv *relay.Server, started time.Time) {
	t := time.NewTicker(statEvery)
	defer t.Stop()
	prev := srv.Stats()
	prevAt := started
	prevUse := sample()
	// Zero rather than `started`, so a relay that comes up idle says so once
	// promptly instead of staying silent for the first hour.
	var lastSaid time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s := srv.Stats()
			window := now.Sub(prevAt)
			bytes := s.Bytes - prev.Bytes
			packets := s.Forwarded - prev.Forwarded

			// An idle relay says so once and then stays quiet, so that a log
			// somebody actually reads is not mostly zeroes. A relay with peers
			// but no traffic is still worth a line — that combination is what a
			// broken data path looks like.
			use := sample()

			// A quiet relay should be quiet in the log too.
			//
			// The first version only skipped when nothing was registered at
			// all, which is the rare case. The ordinary one is devices
			// registered and idle — a phone that has not needed the relay
			// since breakfast — and that printed a line of zeroes every
			// interval, some three hundred a day, which is how a log stops
			// being read.
			//
			// A heartbeat still goes out hourly, because silence is
			// ambiguous: an operator cannot tell a relay with nothing to say
			// from one that died.
			quiet := packets == 0 && s.Peers == prev.Peers &&
				s.Refused == prev.Refused && s.Dropped == prev.Dropped &&
				s.Throttled == prev.Throttled
			if quiet && now.Sub(lastSaid) < heartbeat {
				prev, prevAt, prevUse = s, now, use
				continue
			}
			lastSaid = now

			log.Info("relaying",
				"up", short(now.Sub(started)),
				"devices", s.Peers,
				"clients", s.Sources,
				"peak", s.Peak,
				"window", short(window),
				"carried", human(bytes),
				"rate", human(uint64(float64(bytes)/window.Seconds()))+"/s",
				"packets", packets,
				"total", human(s.Bytes),
				// What this costs the machine it was lent. A relay should be
				// boring, and publishing it continuously is what makes that
				// claim checkable by the person hosting it.
				"mem", human(use.RSS),
				"cpu", fmt.Sprintf("%.1f%%", use.percent(prevUse, window)),
				"goroutines", use.Goroutines,
			)

			// Refusals and drops are only interesting when they are happening
			// now. Printed on their own line so the ordinary one stays readable,
			// and skipped entirely when there is nothing to say.
			if d := deltas(prev, s); d != "" {
				log.Info("relaying, of note", "window", short(window), "counts", d)
			}
			prev, prevAt, prevUse = s, now, use
		}
	}
}

// deltas describes what went wrong in the last window, or nothing at all.
func deltas(a, b relay.Stat) string {
	parts := []string{}
	if n := b.Refused - a.Refused; n > 0 {
		// Refused means a registration this relay would not accept: a stale
		// timestamp, a device claiming a handle somebody else holds, or a
		// source at its cap. Steady refusals with no registrations is what a
		// client stuck in a loop looks like from here.
		parts = append(parts, fmt.Sprintf("refused=%d", n))
	}
	if n := b.Dropped - a.Dropped; n > 0 {
		// Dropped is a packet with nowhere to go — usually a sender reaching
		// for a peer whose registration has expired.
		parts = append(parts, fmt.Sprintf("dropped=%d", n))
	}
	if n := b.Throttled - a.Throttled; n > 0 {
		// Throttled is this relay working as configured, not failing. Worth
		// saying loudly anyway: it is what a user experiences as a slow
		// network, and the operator is the only one who can see why.
		parts = append(parts, fmt.Sprintf("throttled=%d (ceilings are biting)", n))
	}
	if n := b.Challenged - a.Challenged; n > b.Registered-a.Registered {
		// More challenges than completed registrations: something is asking to
		// register and not proving it receives. One or two is a lost packet;
		// a stream of them is a device behind something that will not let the
		// answer back.
		parts = append(parts, fmt.Sprintf("unanswered-challenges=%d", n-(b.Registered-a.Registered)))
	}
	return strings.Join(parts, " ")
}

// human renders a byte count the way an operator reads a bandwidth bill.
func human(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := uint64(unit), 0
	for v := n / unit; v >= unit && exp < 4; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTP"[exp])
}

// short renders a duration without the noise of sub-second precision.
func short(d time.Duration) string {
	if d >= time.Hour {
		return d.Round(time.Minute).String()
	}
	return d.Round(time.Second).String()
}

func envInt(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envInt64(name string, def int64) int64 {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}
