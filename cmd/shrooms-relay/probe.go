package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/vpavlin/shrooms/internal/ctrl"
	"github.com/vpavlin/shrooms/internal/identity"
	"github.com/vpavlin/shrooms/internal/relay"
)

// Checking that a relay works, from outside it.
//
// A relay is the one piece of this system whose whole job is to be reachable by
// somebody else, so "it started without errors" says very little. What matters
// is whether a stranger's packets survive the path to it — which on hosted
// infrastructure means a NAT, a load balancer, a forwarded port and whatever
// the provider does to UDP, none of which the relay can see.
//
// So the probe is a client, not an inspection. It stands up two throwaway
// devices, registers both the way a real one would, and sends a packet from one
// to the other. If that works, the relay works, and nothing about the answer
// depends on trusting the relay's own account of itself.

// probeTimeout bounds each exchange. Generous: this runs against hosts that may
// be far away, and a slow answer is still an answer.
const probeTimeout = 5 * time.Second

// forwardTimeout is shorter, because the forward is retried and four attempts
// at the full timeout would be a diagnostic that takes twenty seconds to fail.
const forwardTimeout = 2 * time.Second

func probe(target, token string) error {
	at, err := resolve(target)
	if err != nil {
		return err
	}
	// Framed first, because that is what a real client speaks and therefore the
	// only result that means "usable". An unframed retry exists only to tell an
	// old relay apart from an absent one.
	if err := probeOnce(at, token, true); err == nil {
		return nil
	} else if probeOnce(at, token, false) == nil {
		return fmt.Errorf("this relay answers the unframed form but not the one "+
			"shrooms clients use, so it is running a build older than the framing "+
			"fix and no device can register with it — redeploy it from a current "+
			"image (original error: %w)", err)
	} else {
		return err
	}
}

func probeOnce(at netip.AddrPort, token string, framed bool) error {

	key := relay.OpenKey()
	if token != "" {
		key = relay.TokenKey(token)
	}
	// A stable identity per machine, so repeated probes refresh two
	// registrations rather than adding two more.
	//
	// Both halves have to be stable together, and getting that wrong is how
	// this was broken twice. A fixed tag with a fresh device key each run is a
	// different device claiming a handle the last run still holds, which
	// first-claim-wins correctly refuses. Random tags avoid that but leak a
	// registration per run, so a loop of probes exhausts the source's quota and
	// then looks exactly like an unreachable relay — which is precisely what a
	// `while true` loop found.
	//
	// Derived rather than stored, because a diagnostic should not leave files
	// behind. Two machines probing one relay get different tags, so they do not
	// collide; the same machine gets the same two every time, which is how a
	// real client behaves.
	seed := probeSeed()
	meshKey := relay.TokenKey(string(seed[:]))

	if framed {
		fmt.Printf("probing %s (%s)\n", at, accessOf(token))
	}

	a, err := newProbeDevice(seed, 1, framed)
	if err != nil {
		return err
	}
	defer a.close()
	b, err := newProbeDevice(seed, 2, framed)
	if err != nil {
		return err
	}
	defer b.close()

	tagA, tagB := relay.Tag(meshKey, a.wg), relay.Tag(meshKey, b.wg)

	for _, d := range []struct {
		name string
		dev  *probeDevice
		tag  identity.WGKey
	}{{"first", a, tagA}, {"second", b, tagB}} {
		took, err := d.dev.join(at, key, d.tag)
		if err != nil {
			return fmt.Errorf("%s device: %w", d.name, err)
		}
		if framed {
			fmt.Printf("  %-7s device registered in %v (challenge answered)\n", d.name, took.Round(time.Millisecond))
		}
	}

	payload := []byte("shrooms relay probe")
	frame, err := relay.EncodeForward(key, tagB, identity.WGKey{}, payload)
	if err != nil {
		return err
	}
	// Retried, because a confirm is fire-and-forget.
	//
	// The relay answers a registration with a challenge but says nothing about
	// the confirm, so the only way to learn that a device is registered is for
	// traffic to reach it. That leaves a race: the second device's confirm and
	// the first device's forward leave back to back on different sockets, and a
	// forward that arrives first is dropped for naming a destination the relay
	// has not installed yet.
	//
	// Invisible over loopback, where the confirm is processed in microseconds,
	// and reliably fatal over a link with any latency — which is exactly the
	// case a probe exists for.
	start := time.Now()
	var got *relay.Frame
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if err := a.send(at, frame); err != nil {
			return err
		}
		got, lastErr = b.recvWithin(key, forwardTimeout)
		if lastErr == nil {
			break
		}
	}
	if lastErr != nil {
		return fmt.Errorf("the relay accepted both registrations but forwarded nothing "+
			"after 4 attempts: %w", lastErr)
	}
	if got.Type != relay.TypeForward || string(got.Payload) != string(payload) {
		return errors.New("what came back is not what was sent")
	}
	// The relay fills the source in from its own table rather than copying
	// what the sender claimed, so this also checks it is not simply echoing.
	if got.Src != tagA {
		return fmt.Errorf("the relay named the wrong sender: %x", got.Src[:8])
	}
	// Only the framed run reports success. The unframed one exists solely to
	// tell an old relay from an absent one, and announcing "OK" from it would
	// contradict the error printed a line later.
	if framed {
		fmt.Printf("  packet relayed in %v\n", time.Since(start).Round(time.Millisecond))
		fmt.Printf("\nOK — %s forwards, and cannot be pointed at an address that does not answer\n", at)
	}
	return nil
}

func accessOf(token string) string {
	if token == "" {
		return "no token"
	}
	return "with token"
}

// resolve accepts a name or an address, because a hosted relay is usually
// named — an Akash provider hands out a hostname and a forwarded port, not an
// address.
func resolve(target string) (netip.AddrPort, error) {
	if ap, err := netip.ParseAddrPort(target); err == nil {
		return ap, nil
	}
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("%q is not host:port", target)
	}
	ips, err := net.LookupHost(host)
	if err != nil || len(ips) == 0 {
		return netip.AddrPort{}, fmt.Errorf("cannot resolve %q: %w", host, err)
	}
	return netip.ParseAddrPort(net.JoinHostPort(ips[0], port))
}

type probeDevice struct {
	conn   *net.UDPConn
	priv   ed25519.PrivateKey
	wg     identity.WGKey
	framed bool
}

// probeSeed is a value stable for this machine and unlikely to match another's.
//
// Hostname and user id, hashed. Neither is secret and neither needs to be: the
// only thing riding on it is that two people probing the same relay do not land
// on each other's handles, and that this machine lands on its own each time.
func probeSeed() [32]byte {
	host, _ := os.Hostname()
	return sha256.Sum256([]byte("shrooms-relay/probe/v1|" + host + "|" + strconv.Itoa(os.Getuid())))
}

func newProbeDevice(seed [32]byte, n byte, framed bool) (*probeDevice, error) {
	c, err := net.ListenUDP("udp", nil)
	if err != nil {
		return nil, err
	}
	// Derived from the same seed, so this device is the same device across
	// runs and the relay sees a refresh rather than a stranger.
	ds := sha256.Sum256(append(seed[:], n))
	priv := ed25519.NewKeyFromSeed(ds[:])
	var wg identity.WGKey
	wg[0] = n
	return &probeDevice{conn: c, priv: priv, wg: wg, framed: framed}, nil
}

func (d *probeDevice) close() { d.conn.Close() }

// framed says whether to speak the dialect real clients speak.
//
// A shrooms client wraps relay frames in the control header that lets them
// share the WireGuard socket; tools speaking the frames directly do not. A relay
// older than that fix reads only the unframed form — so it answers this probe
// and ignores every real client, which is a worse outcome than failing.
//
// Found the hard way: a relay probed green and neither of two containers could
// register with it.
func (d *probeDevice) send(to netip.AddrPort, pkt []byte) error {
	if d.framed {
		pkt = ctrl.Wrap(ctrl.SubRelay, pkt)
	}
	_, err := d.conn.WriteToUDPAddrPort(pkt, to)
	return err
}

func (d *probeDevice) recv(k relay.Key) (*relay.Frame, error) {
	return d.recvWithin(k, probeTimeout)
}

func (d *probeDevice) recvWithin(k relay.Key, within time.Duration) (*relay.Frame, error) {
	if err := d.conn.SetReadDeadline(time.Now().Add(within)); err != nil {
		return nil, err
	}
	buf := make([]byte, readBuf)
	n, _, err := d.conn.ReadFromUDPAddrPort(buf)
	if err != nil {
		return nil, fmt.Errorf("nothing came back within %v", within)
	}
	// Accept either dialect on the way back, so a relay that answers in the
	// other one is diagnosed rather than reported as silent.
	_, frame, _ := ctrl.Unwrap(buf[:n])
	return relay.Decode(k, frame)
}

// join runs the exchange a real client runs, which is what makes this a test of
// the relay rather than of the probe.
func (d *probeDevice) join(at netip.AddrPort, k relay.Key, tag identity.WGKey) (time.Duration, error) {
	start := time.Now()
	if err := d.send(at, relay.EncodeRegister(k, tag, d.priv, time.Now())); err != nil {
		return 0, err
	}
	f, err := d.recv(k)
	if err != nil {
		return 0, fmt.Errorf("no challenge — the relay is unreachable, the port is not "+
			"forwarded, it wants a different token, or this address has hit its "+
			"registration cap (try again in a couple of minutes): %w", err)
	}
	if f.Type != relay.TypeChallenge {
		return 0, fmt.Errorf("expected a routability challenge, got frame type %d", f.Type)
	}
	if err := d.send(at, relay.EncodeConfirm(k, tag, f.Nonce, d.priv, time.Now())); err != nil {
		return 0, err
	}
	return time.Since(start), nil
}
