package main

import (
	"github.com/vpavlin/shrooms/internal/hostload"
	"time"

	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func metricsFixture(t *testing.T) (*metricsServer, statusPayload) {
	t.Helper()
	st := statusPayload{Name: "laptop", Version: "v0.9.0-160", ModeRunning: "Core",
		Meshes: []meshStatus{{Label: "office", Overlay: "fdb0::1", Iface: "logos01", Peers: 2}, {Label: "home", Overlay: "fd7b::1", Iface: "logos02", Peers: 1}},
		Peers: []peerStatus{
			{Mesh: "office", Name: "pi5", Overlay: "fdb0::2", RxBytes: 1000, TxBytes: 2000, Live: true, Online: true, RTTMs: 12},
			{Mesh: "office", Name: "phone \"nothing\"", Overlay: "fdb0::3", RxBytes: 5, TxBytes: 6, Relayed: true},
			{Mesh: "home", Name: "pi5", Overlay: "fd7b::2", RxBytes: 7, TxBytes: 8},
		}}
	netdev := filepath.Join(t.TempDir(), "dev")
	os.WriteFile(netdev, []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
    lo: 999 1 0 0 0 0 0 0 999 1 0 0 0 0 0 0
enx349971e78e37: 123456789 1 0 0 0 0 0 0 98765 1 0 0 0 0 0 0
 logos01: 4000 1 0 0 0 0 0 0 3000 1 0 0 0 0 0 0
 logos02: 4100 1 0 0 0 0 0 0 3100 1 0 0 0 0 0 0
`), 0o600)
	delivery := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "# HELP libp2p_network_bytes_total x\n# TYPE libp2p_network_bytes_total counter\n"+
			"libp2p_network_bytes_total{direction=\"in\"} 7630373.0\nlibp2p_network_bytes_total{direction=\"out\"} 5833310.0\n"+
			"libp2p_network_bytes_created{direction=\"in\"} 1.7e9\nlibp2p_peers 5.0\nwaku_node_messages_total{type=\"relay\"} 585.0\nnim_gc_mem_bytes 1\n")
	}))
	t.Cleanup(delivery.Close)
	m := newMetricsServer(9180, func() statusPayload { return st }, slog.New(slog.DiscardHandler))
	m.netdev, m.delivery = netdev, delivery.URL
	return m, st
}

// What a scrape returns: the tunnels per peer and mesh, the machine's
// interfaces, the delivery node's traffic — in the text format, line by line.
func TestMetricsSayWhereTheBytesGo(t *testing.T) {
	m, _ := metricsFixture(t)
	srv := httptest.NewServer(m.handler("office"))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	out := string(b)
	for _, want := range []string{
		`shrooms_info{node="laptop",version="v0.9.0-160",mode="Core"} 1`,
		`shrooms_mesh_peers{mesh="office"} 2`,
		`shrooms_peer_receive_bytes_total{mesh="office",peer="pi5"} 1000`,
		`shrooms_peer_relayed{mesh="office",peer="phone \"nothing\""} 1`,
		`shrooms_peer_rtt_seconds{mesh="office",peer="pi5"} 0.012`,
		`shrooms_host_receive_bytes_total{interface="enx349971e78e37"} 123456789`,
		`shrooms_host_transmit_bytes_total{interface="logos01"} 3000`,
		`shrooms_delivery_bytes_total{direction="in"} 7630373.0`,
		`shrooms_delivery_peers 5.0`,
		`shrooms_delivery_messages_total{type="relay"} 585.0`,
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{`interface="lo"`, `mesh="home"`, `interface="logos02"`, "fd7b", "nim_gc_mem_bytes", "libp2p_network_bytes_created", "shrooms_delivery_bytes_created"} {
		if strings.Contains(out, not) {
			t.Errorf("%s should not be served", not)
		}
	}
	// Every line a comment or a well-formed sample.
	sample := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{([a-zA-Z_][a-zA-Z0-9_]*="([^"\\]|\\.)*",?)*\})? [-+0-9.eE]+$`)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(l, "# HELP ") && !strings.HasPrefix(l, "# TYPE ") && !sample.MatchString(l) {
			t.Errorf("not the text format: %q", l)
		}
	}
}

// A peer asking on one mesh learns that mesh's machines only (ADR-046, as
// amended: a mesh's members cannot read another mesh's).
func TestAPeerSeesOnlyItsOwnMesh(t *testing.T) {
	m, st := metricsFixture(t)
	var b strings.Builder
	m.write(&b, "home")
	out := b.String()
	if !strings.Contains(out, `shrooms_peer_transmit_bytes_total{mesh="home",peer="pi5"} 8`+"\n") {
		t.Errorf("home's own peer missing:\n%s", out)
	}
	for _, not := range []string{`mesh="office"`, "phone", `interface="logos01"`} {
		if strings.Contains(out, not) {
			t.Errorf("home's listener serves %s", not)
		}
	}
	if got := targetList(targets(st, 9180, "home", false)); got != "laptop@[fd7b::1]:9180/1 pi5@[fd7b::2]:9180/1" {
		t.Errorf("a peer on home was told about %s", got)
	}
}

// The scraper beside the daemon gets every machine on every mesh, with one
// target per machine marked first; a peer's address is not this machine.
func TestTheMachinesOwnScraperSeesEveryMesh(t *testing.T) {
	_, st := metricsFixture(t)
	want := `laptop@[fdb0::1]:9180/1 laptop@[fd7b::1]:9180/0 pi5@[fdb0::2]:9180/1 phone "nothing"@[fdb0::3]:9180/1 pi5@[fd7b::2]:9180/0`
	if got := targetList(targets(st, 9180, "office", true)); got != want {
		t.Fatalf("targets %q, want %q", got, want)
	}
	for addr, self := range map[string]bool{"127.0.0.1:5": true, "[::1]:5": true, "[fdb0::1]:5": true, "[fd7b::1]:5": true, "[fdb0::2]:5": false, "[fe80::1]:5": false} {
		if fromSelf(addr, st) != self {
			t.Errorf("fromSelf(%s) = %v", addr, !self)
		}
	}
}

func targetList(groups []sdGroup) string {
	var got []string
	for _, g := range groups {
		got = append(got, g.Labels["node"]+"@"+g.Targets[0]+"/"+g.Labels["first"])
	}
	return strings.Join(got, " ")
}

// No metrics server on the delivery node (older library, not up yet): the
// rest is served as usual.
func TestMetricsWithoutTheDeliveryNode(t *testing.T) {
	m, _ := metricsFixture(t)
	m.delivery = "http://127.0.0.1:1/metrics"
	var b strings.Builder
	m.write(&b, "office")
	// The delivery node's own counters are absent; what this node itself
	// sent (shrooms_delivery_sent_*) is ours and always there.
	for _, passed := range []string{"shrooms_delivery_bytes_total", "shrooms_delivery_peers", "shrooms_delivery_messages_total"} {
		if strings.Contains(b.String(), passed+" ") || strings.Contains(b.String(), passed+"{") {
			t.Errorf("%s served without a delivery node", passed)
		}
	}
	if !strings.Contains(b.String(), "shrooms_peer_up") {
		t.Fatalf("unexpected:\n%s", b.String())
	}
}

// What this node published, for RLN's rate limits: counted, sized, and the
// busiest second, in the text format Prometheus reads (2026-10-21).
func TestWhatThisNodeSentIsExported(t *testing.T) {
	var b strings.Builder
	sentMetrics(&expo{w: &b}, time.Now(), "office")
	out := b.String()
	for _, want := range []string{
		"# TYPE shrooms_delivery_sent_messages_total counter",
		"# TYPE shrooms_delivery_sent_message_bytes histogram",
		"# TYPE shrooms_delivery_sent_peak_per_second gauge",
		"shrooms_delivery_sent_peak_per_second ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// The machine's load, from /proc and statfs: CPU time by mode, the CPUs,
// the load averages, memory and the root disk.
func TestTheMachinesLoadIsExported(t *testing.T) {
	d := t.TempDir()
	stat := filepath.Join(d, "stat")
	os.WriteFile(stat, []byte("cpu  1000 20 300 50000 40 0 6 0 0 0\ncpu0 500 10 150 25000 20 0 3 0 0 0\ncpu1 500 10 150 25000 20 0 3 0 0 0\nintr 1\n"), 0o600)
	load := filepath.Join(d, "loadavg")
	os.WriteFile(load, []byte("1.50 0.75 0.25 2/300 4242\n"), 0o600)
	mem := filepath.Join(d, "meminfo")
	os.WriteFile(mem, []byte("MemTotal:       16000000 kB\nMemFree:  1000 kB\nMemAvailable:    8000000 kB\n"), 0o600)
	var b strings.Builder
	hostLoad(&expo{w: &b}, hostload.Paths{Stat: stat, Loadavg: load, Meminfo: mem, Root: d})
	out := b.String()
	for _, want := range []string{
		`shrooms_host_cpu_seconds_total{mode="user"} 10`,
		`shrooms_host_cpu_seconds_total{mode="idle"} 500`,
		`shrooms_host_cpus 2`,
		`shrooms_host_load{window="1m"} 1.5`,
		`shrooms_host_load{window="15m"} 0.25`,
		`shrooms_host_memory_bytes{kind="total"} 16384000000`,
		`shrooms_host_memory_bytes{kind="available"} 8192000000`,
		`shrooms_host_disk_bytes{kind="size"}`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %s in:\n%s", want, out)
		}
	}
}

// v0.40 prefixed its own metrics with logos_delivery_ (ADR-071). Its message
// count comes out under the name the dashboards already read, once, beside
// libp2p's, whose names did not change.
func TestTheDeliveryNodesMetricsReadTheSameFromEitherLibrary(t *testing.T) {
	for name, body := range map[string]string{
		"july":  "waku_node_messages_total{type=\"relay\"} 7\nlibp2p_peers 3\n",
		"v0.40": "# HELP logos_delivery_node_messages number of messages received\nlogos_delivery_node_messages_total{type=\"relay\"} 7\nlibp2p_peers 3\n",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
		var out strings.Builder
		deliveryMetrics(&out, srv.URL)
		srv.Close()
		got := out.String()
		if !strings.Contains(got, "shrooms_delivery_messages_total{type=\"relay\"} 7\n") ||
			!strings.Contains(got, "shrooms_delivery_peers 3\n") ||
			strings.Count(got, "# TYPE shrooms_delivery_messages_total") != 1 {
			t.Errorf("%s: got\n%s", name, got)
		}
	}
}
