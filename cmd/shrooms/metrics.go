package main

// Prometheus metrics (docs/metrics.md), on this node's mesh addresses at
// metrics_port: members can scrape them, nobody else can reach them.
//
// Three sources, because the question they answer — "where does a terabyte a
// day come from" (2026-10-09) — has three candidates:
//
//   - the tunnels: what each peer's WireGuard session carried, per mesh, and
//     whether it went direct or through a relay — from the same status
//     snapshot `shrooms status` prints;
//   - the delivery node: what the rendezvous plane's libp2p node exchanged,
//     which a Core node does for the whole cluster, not only for us — from
//     the library's own metrics server on loopback, passed through under
//     shrooms_delivery_ names;
//   - the machine: every interface's byte counters (/proc/net/dev; the daemon
//     runs on the host's network), which is what a router sees.
//
// /targets lists this node and every peer for Prometheus' http_sd, so a
// scraper needs to be told about one node and finds the rest.
//
// Each mesh's listener shows that mesh and the machine, never another mesh
// (ADR-046, amended 2026-10-25). A device's keys are per mesh so that its
// meshes cannot be linked (ADR-015), and one page for every listener handed
// a member of one mesh the names, addresses and traffic of every other.
//
// Text format written by hand, as the project does wire formats: no client
// library for a few dozen lines.

import (
	"github.com/vpavlin/shrooms/internal/hostload"
	"github.com/vpavlin/shrooms/internal/mesh"

	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// deliveryMetricsPort is where the delivery node serves its metrics, on
// loopback only: a port the kernel gave as free when the node was set up
// (freeLoopbackPort); 0 when it serves none.
var deliveryMetricsPort int

// freeLoopbackPort is a loopback TCP port nothing holds now.
func freeLoopbackPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// metricsServer serves /metrics and /targets on each mesh address.
type metricsServer struct {
	port     uint16
	snapshot func() statusPayload
	log      *slog.Logger
	delivery string // the delivery node's metrics URL; "" for none
	netdev   string // /proc/net/dev; a test's fixture
	host     hostload.Paths

	mu sync.Mutex
	ls map[string]net.Listener // by address; each serves its own mesh
}

func newMetricsServer(port uint16, snapshot func() statusPayload, log *slog.Logger) *metricsServer {
	m := &metricsServer{port: port, snapshot: snapshot, log: log, netdev: "/proc/net/dev", host: hostload.Real, ls: map[string]net.Listener{}}
	if deliveryMetricsPort != 0 {
		m.delivery = fmt.Sprintf("http://127.0.0.1:%d/metrics", deliveryMetricsPort)
	}
	return m
}

// run keeps a listener on each mesh address — meshes come and go with
// reloads, and an address only exists once its interface is up — until ctx
// ends.
func (m *metricsServer) run(ctx context.Context) {
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		m.listen()
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, l := range m.ls {
				l.Close()
			}
			m.mu.Unlock()
			return
		case <-t.C:
		}
	}
}

func (m *metricsServer) listen() {
	want := map[string]string{} // address -> its mesh
	for _, ms := range m.snapshot().Meshes {
		if ms.Overlay != "" {
			want[net.JoinHostPort(ms.Overlay, strconv.Itoa(int(m.port)))] = ms.Label
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for a, l := range m.ls {
		if _, ok := want[a]; !ok {
			l.Close()
			delete(m.ls, a)
		}
	}
	for a, label := range want {
		if _, ok := m.ls[a]; ok {
			continue
		}
		l, err := net.Listen("tcp", a)
		if err != nil {
			continue // the address is not up yet; the next round tries again
		}
		m.ls[a] = l
		m.log.Info("metrics up", "address", a)
		go (&http.Server{Handler: m.handler(label), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
			WriteTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 64 << 10}).Serve(l)
	}
}

// handler serves one mesh's listener.
func (m *metricsServer) handler(label string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		m.write(w, label)
	})
	mux.HandleFunc("GET /targets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		st := m.snapshot()
		// This machine asking is the scraper beside it (deploy/metrics), which
		// is on every mesh this daemon is: it gets them all. A peer gets the
		// mesh it asked on.
		all := fromSelf(r.RemoteAddr, st)
		json.NewEncoder(w).Encode(targets(st, m.port, label, all))
	})
	return mux
}

// fromSelf is whether a request came from this machine: loopback, or one of
// its own overlay addresses. A peer cannot send from those: WireGuard takes
// from a peer only the addresses it is allowed, which are its own.
func fromSelf(remote string, st statusPayload) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() {
		return true
	}
	for _, ms := range st.Meshes {
		for _, a := range []string{ms.Overlay, ms.OverlayV4} {
			if o := net.ParseIP(a); o != nil && o.Equal(ip) {
				return true
			}
		}
	}
	return false
}

// onlyMesh is the snapshot as one mesh may see it: that mesh, its peers.
func onlyMesh(st statusPayload, label string) statusPayload {
	out := st
	out.Meshes, out.Peers = nil, nil
	for _, ms := range st.Meshes {
		if ms.Label == label {
			out.Meshes = append(out.Meshes, ms)
		}
	}
	for _, p := range st.Peers {
		if p.Mesh == label {
			out.Peers = append(out.Peers, p)
		}
	}
	return out
}

// write is the metrics, now, as mesh label's listener serves them: that
// mesh, and the machine without the other meshes' interfaces.
func (m *metricsServer) write(w io.Writer, label string) {
	full := m.snapshot()
	others := map[string]bool{}
	for _, ms := range full.Meshes {
		if ms.Label != label && ms.Iface != "" {
			others[ms.Iface] = true
		}
	}
	st := onlyMesh(full, label)
	e := &expo{w: w}
	e.family("shrooms_info", "gauge", "This node: its name, version and delivery mode.")
	e.sample("shrooms_info", 1, "node", st.Name, "version", st.Version, "mode", st.ModeRunning)

	e.family("shrooms_mesh_peers", "gauge", "Peers this node knows on each mesh.")
	for _, ms := range st.Meshes {
		e.sample("shrooms_mesh_peers", float64(ms.Peers), "mesh", ms.Label)
	}

	e.family("shrooms_mesh_dropped_not_for_self_total", "counter", "Packets peers sent this node that were not addressed to it, or could have gone further, dropped before the kernel.")
	for _, ms := range st.Meshes {
		if !ms.NotRunning {
			e.sample("shrooms_mesh_dropped_not_for_self_total", float64(ms.NotForSelf), "mesh", ms.Label)
		}
	}

	peers := append([]peerStatus(nil), st.Peers...)
	sort.Slice(peers, func(i, j int) bool { return peers[i].Mesh+peers[i].Name < peers[j].Mesh+peers[j].Name })
	e.family("shrooms_peer_receive_bytes_total", "counter", "Bytes received from a peer through its tunnel (WireGuard, since the tunnel was set up).")
	for _, p := range peers {
		e.sample("shrooms_peer_receive_bytes_total", float64(p.RxBytes), "mesh", p.Mesh, "peer", p.Name)
	}
	e.family("shrooms_peer_transmit_bytes_total", "counter", "Bytes sent to a peer through its tunnel.")
	for _, p := range peers {
		e.sample("shrooms_peer_transmit_bytes_total", float64(p.TxBytes), "mesh", p.Mesh, "peer", p.Name)
	}
	e.family("shrooms_peer_up", "gauge", "1 while the tunnel to a peer works.")
	for _, p := range peers {
		e.sample("shrooms_peer_up", b2f(p.Live), "mesh", p.Mesh, "peer", p.Name)
	}
	e.family("shrooms_peer_relayed", "gauge", "1 while a peer is reached through a relay rather than directly.")
	for _, p := range peers {
		e.sample("shrooms_peer_relayed", b2f(p.Relayed), "mesh", p.Mesh, "peer", p.Name)
	}
	e.family("shrooms_peer_announcing", "gauge", "1 while a peer's announces arrive (the rendezvous plane's view).")
	for _, p := range peers {
		e.sample("shrooms_peer_announcing", b2f(p.Online), "mesh", p.Mesh, "peer", p.Name)
	}
	e.family("shrooms_peer_rtt_seconds", "gauge", "Round trip to a peer on its selected path.")
	for _, p := range peers {
		if p.RTTMs > 0 {
			e.sample("shrooms_peer_rtt_seconds", float64(p.RTTMs)/1000, "mesh", p.Mesh, "peer", p.Name)
		}
	}

	e.family("shrooms_host_receive_bytes_total", "counter", "Bytes this machine received on each interface (/proc/net/dev) — what a router counts.")
	var ifs []ifaceStat
	for _, i := range readNetDev(m.netdev) {
		if !others[i.name] {
			ifs = append(ifs, i)
		}
	}
	for _, i := range ifs {
		e.sample("shrooms_host_receive_bytes_total", float64(i.rx), "interface", i.name)
	}
	e.family("shrooms_host_transmit_bytes_total", "counter", "Bytes this machine sent on each interface.")
	for _, i := range ifs {
		e.sample("shrooms_host_transmit_bytes_total", float64(i.tx), "interface", i.name)
	}

	hostLoad(e, m.host)

	if rss := residentBytes(); rss > 0 {
		e.family("shrooms_process_resident_bytes", "gauge", "The daemon's resident memory.")
		e.sample("shrooms_process_resident_bytes", float64(rss))
	}
	if m.delivery != "" {
		deliveryMetrics(w, m.delivery)
	}
	sentMetrics(e, time.Now(), label)
}

// sentMetrics is what this node itself published on the rendezvous plane, by
// mesh and kind (internal/mesh/sendstats.go): for sizing RLN's per-publisher
// rate limits, which the delivery node's own counters cannot answer — they
// count everything it relays.
//
// One mesh's, on that mesh's listener; the busiest second is the machine's.
func sentMetrics(e *expo, now time.Time, label string) {
	all, peak := mesh.SendStats(now)
	var counts []mesh.SendCount
	for _, c := range all {
		if c.Mesh == label {
			counts = append(counts, c)
		}
	}
	e.family("shrooms_delivery_sent_messages_total", "counter", "Messages this node published on the rendezvous plane (its own, not relayed), by mesh and kind.")
	for _, c := range counts {
		e.sample("shrooms_delivery_sent_messages_total", float64(c.Messages), "mesh", c.Mesh, "kind", c.Kind)
	}
	e.family("shrooms_delivery_sent_bytes_total", "counter", "Payload bytes of those messages, as handed to the delivery node (the wire adds the envelope).")
	for _, c := range counts {
		e.sample("shrooms_delivery_sent_bytes_total", float64(c.Bytes), "mesh", c.Mesh, "kind", c.Kind)
	}
	e.family("shrooms_delivery_send_failures_total", "counter", "Publishes the delivery node refused or failed.")
	for _, c := range counts {
		e.sample("shrooms_delivery_send_failures_total", float64(c.Failures), "mesh", c.Mesh, "kind", c.Kind)
	}
	e.family("shrooms_delivery_sent_message_bytes", "histogram", "Payload size of each published message.")
	for _, c := range counts {
		for i, b := range mesh.SendSizeBuckets {
			e.sample("shrooms_delivery_sent_message_bytes_bucket", float64(c.Buckets[i]), "mesh", c.Mesh, "kind", c.Kind, "le", strconv.FormatFloat(b, 'f', -1, 64))
		}
		e.sample("shrooms_delivery_sent_message_bytes_bucket", float64(c.Messages), "mesh", c.Mesh, "kind", c.Kind, "le", "+Inf")
		e.sample("shrooms_delivery_sent_message_bytes_sum", float64(c.Bytes), "mesh", c.Mesh, "kind", c.Kind)
		e.sample("shrooms_delivery_sent_message_bytes_count", float64(c.Messages), "mesh", c.Mesh, "kind", c.Kind)
	}
	e.family("shrooms_delivery_sent_peak_per_second", "gauge", "Most messages this node published in any one second over the last 10 minutes, all meshes together: what an RLN epoch of one second would have to allow.")
	e.sample("shrooms_delivery_sent_peak_per_second", float64(peak))
}

// deliveryRenames are the delivery node's metrics passed through, and what
// they are called here. v0.40 renamed its own metrics with a logos_delivery_
// prefix, libp2p's kept theirs; both libraries' names are here, so a
// dashboard reads one name whichever library a node runs (ADR-071).
var deliveryRenames = [][2]string{
	{"libp2p_network_bytes_total", "shrooms_delivery_bytes_total"},
	{"libp2p_peers", "shrooms_delivery_peers"},
	{"waku_node_messages_total", "shrooms_delivery_messages_total"},
	{"logos_delivery_node_messages_total", "shrooms_delivery_messages_total"},
}

// deliveryMetrics passes the delivery node's traffic through, renamed; it
// writes nothing when the node has no metrics server (an older library, or
// the node not up yet).
func deliveryMetrics(w io.Writer, url string) {
	hc := &http.Client{Timeout: 3 * time.Second}
	resp, err := hc.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	lines := map[string][]string{}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		l := sc.Text()
		for _, rn := range deliveryRenames {
			if strings.HasPrefix(l, rn[0]+"{") || strings.HasPrefix(l, rn[0]+" ") {
				lines[rn[0]] = append(lines[rn[0]], rn[1]+l[len(rn[0]):])
			}
		}
	}
	help := map[string]string{
		"shrooms_delivery_bytes_total":    "Bytes the delivery (rendezvous) node exchanged with the fleet — all of it, most of it other applications' when it runs as Core.",
		"shrooms_delivery_peers":          "Peers the delivery node is connected to.",
		"shrooms_delivery_messages_total": "Messages the delivery node handled.",
	}
	written := map[string]bool{}
	for _, rn := range deliveryRenames {
		if len(lines[rn[0]]) == 0 || written[rn[1]] {
			continue
		}
		written[rn[1]] = true
		typ := "counter"
		if !strings.HasSuffix(rn[1], "_total") {
			typ = "gauge"
		}
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", rn[1], help[rn[1]], rn[1], typ)
		for _, l := range lines[rn[0]] {
			fmt.Fprintln(w, l)
		}
	}
}

// expo writes the text exposition format.
type expo struct{ w io.Writer }

func (e *expo) family(name, typ, help string) {
	fmt.Fprintf(e.w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (e *expo) sample(name string, v float64, labels ...string) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			fmt.Fprintf(&b, "%s=\"%s\"", labels[i], escapeLabel(labels[i+1]))
		}
		b.WriteByte('}')
	}
	fmt.Fprintf(e.w, "%s %s\n", b.String(), strconv.FormatFloat(v, 'f', -1, 64))
}

// escapeLabel escapes a label value as the text format wants: backslash,
// double quote and newline, and nothing else (Go's %q would also escape
// non-ASCII, which Prometheus reads literally).
func escapeLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(strings.ToValidUTF8(s, "?"))
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

type ifaceStat struct {
	name   string
	rx, tx uint64
}

// readNetDev reads each interface's byte counters, loopback left out.
func readNetDev(path string) []ifaceStat {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []ifaceStat
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, ok := strings.Cut(sc.Text(), ":")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		f := strings.Fields(rest)
		if name == "lo" || len(f) < 9 {
			continue
		}
		rx, _ := strconv.ParseUint(f[0], 10, 64)
		tx, _ := strconv.ParseUint(f[8], 10, 64)
		out = append(out, ifaceStat{name, rx, tx})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func residentBytes() uint64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				kb, _ := strconv.ParseUint(f[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}

// sdGroup is one target group of Prometheus' http_sd.
type sdGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

// targets is what a scraper should scrape: on mesh label, this node and its
// peers there. For this machine's own scraper (all), every machine on every
// mesh, once per mesh it shares, since each listener shows only its own:
// "first" marks one target per machine, from which Prometheus keeps the
// machine's own series (prometheus.yml.in), so nothing is counted twice. A
// machine on two meshes is one machine as far as its name says.
func targets(st statusPayload, port uint16, label string, all bool) []sdGroup {
	if !all {
		st = onlyMesh(st, label)
	}
	var out []sdGroup
	seen := map[string]bool{}  // node+mesh
	first := map[string]bool{} // node
	add := func(name, mesh, addr string) {
		if addr == "" || seen[name+"\x00"+mesh] {
			return
		}
		seen[name+"\x00"+mesh] = true
		f := "0"
		if !first[name] {
			first[name], f = true, "1"
		}
		out = append(out, sdGroup{
			Targets: []string{net.JoinHostPort(addr, strconv.Itoa(int(port)))},
			Labels:  map[string]string{"node": name, "mesh": mesh, "first": f},
		})
	}
	for _, ms := range st.Meshes {
		add(st.Name, ms.Label, ms.Overlay)
	}
	for _, p := range st.Peers {
		add(p.Name, p.Mesh, p.Overlay)
	}
	return out
}
