package main

import (
	"encoding/json"
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
		Meshes: []meshStatus{{Label: "office", Overlay: "fdb0::1", Peers: 2}, {Label: "home", Overlay: "fd7b::1", Peers: 1}},
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
	srv := httptest.NewServer(m.handler())
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
		`shrooms_peer_transmit_bytes_total{mesh="home",peer="pi5"} 8`,
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
	for _, not := range []string{`interface="lo"`, "nim_gc_mem_bytes", "libp2p_network_bytes_created", "shrooms_delivery_bytes_created"} {
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

// One target per machine, however many meshes it is seen on.
func TestTargetsListEachMachineOnce(t *testing.T) {
	m, _ := metricsFixture(t)
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/targets")
	if err != nil {
		t.Fatal(err)
	}
	var groups []sdGroup
	json.NewDecoder(resp.Body).Decode(&groups)
	var got []string
	for _, g := range groups {
		got = append(got, g.Labels["node"]+"@"+g.Targets[0])
	}
	want := `laptop@[fdb0::1]:9180 pi5@[fdb0::2]:9180 phone "nothing"@[fdb0::3]:9180`
	if strings.Join(got, " ") != want {
		t.Fatalf("targets %q, want %q", strings.Join(got, " "), want)
	}
}

// No metrics server on the delivery node (older library, not up yet): the
// rest is served as usual.
func TestMetricsWithoutTheDeliveryNode(t *testing.T) {
	m, _ := metricsFixture(t)
	m.delivery = "http://127.0.0.1:1/metrics"
	var b strings.Builder
	m.write(&b)
	if strings.Contains(b.String(), "shrooms_delivery") || !strings.Contains(b.String(), "shrooms_peer_up") {
		t.Fatalf("unexpected:\n%s", b.String())
	}
}
