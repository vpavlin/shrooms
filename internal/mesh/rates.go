package mesh

import (
	"sync"
	"time"
)

// RateSamples is how much recent history is kept per peer, for a sparkline.
// At one sample per probe tick (3s) this is about three minutes — long enough
// to see a transfer start and finish, short enough to stay small.
const RateSamples = 60

// Rate is throughput for one peer, in bytes per second.
type Rate struct {
	RxBps float64
	TxBps float64

	// RxHistory and TxHistory are recent samples, oldest first. Returned as a
	// copy; the caller may keep them.
	RxHistory []float64
	TxHistory []float64
}

// counters is the previous reading for one peer.
type counters struct {
	rx, tx uint64
	at     time.Time

	rxBps, txBps float64
	rxHist       []float64
	txHist       []float64
}

// rates turns WireGuard's monotonic byte counters into throughput.
//
// The counters are cumulative and only meaningful as differences, so this keeps
// the previous reading per peer. Two things it must not do: report a rate from
// a single sample (there is nothing to difference), and report a negative one
// when a peer's counters reset because the peer was reconfigured — which reads
// as a wild spike rather than as the restart it is.
type rates struct {
	mu   sync.Mutex
	prev map[string]*counters
}

func newRates() *rates { return &rates{prev: make(map[string]*counters)} }

// observe records a reading and updates the derived rate.
func (r *rates) observe(id string, rx, tx uint64, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.prev[id]
	if !ok {
		// First sighting: remember it, claim nothing. A rate needs two points.
		r.prev[id] = &counters{rx: rx, tx: tx, at: now}
		return
	}

	elapsed := now.Sub(c.at).Seconds()
	if elapsed <= 0 {
		return
	}

	// Counters go backwards when a peer is reconfigured or the device restarts.
	// Treat it as a fresh start rather than computing a nonsense rate.
	if rx < c.rx || tx < c.tx {
		c.rx, c.tx, c.at = rx, tx, now
		c.rxBps, c.txBps = 0, 0
		return
	}

	rxBps := float64(rx-c.rx) / elapsed
	txBps := float64(tx-c.tx) / elapsed

	// Smoothed a little. A raw per-tick rate on a bursty link jumps between
	// zero and a large number, which is accurate and useless to look at.
	const alpha = 0.4
	c.rxBps = alpha*rxBps + (1-alpha)*c.rxBps
	c.txBps = alpha*txBps + (1-alpha)*c.txBps

	c.rxHist = appendCapped(c.rxHist, c.rxBps)
	c.txHist = appendCapped(c.txHist, c.txBps)
	c.rx, c.tx, c.at = rx, tx, now
}

func appendCapped(s []float64, v float64) []float64 {
	s = append(s, v)
	if len(s) > RateSamples {
		s = s[len(s)-RateSamples:]
	}
	return s
}

// rate returns the current throughput for a peer.
func (r *rates) rate(id string) Rate {
	r.mu.Lock()
	defer r.mu.Unlock()

	c, ok := r.prev[id]
	if !ok {
		return Rate{}
	}
	return Rate{
		RxBps:     c.rxBps,
		TxBps:     c.txBps,
		RxHistory: append([]float64(nil), c.rxHist...),
		TxHistory: append([]float64(nil), c.txHist...),
	}
}

func (r *rates) forget(id string) {
	r.mu.Lock()
	delete(r.prev, id)
	r.mu.Unlock()
}

// Rate reports throughput for a peer.
func (m *Mesh) Rate(id string) Rate { return m.rates.rate(id) }

// sampleRates reads the data plane and updates every peer's throughput.
// Called on the probe tick, which is often enough to watch a transfer and rare
// enough to cost nothing.
func (m *Mesh) sampleRates(now time.Time) {
	stats, err := m.dev.PeerStats()
	if err != nil {
		return
	}
	for _, p := range m.roster.Peers() {
		if st, ok := stats[p.WGPub.String()]; ok {
			m.rates.observe(p.ID(), st.RxBytes, st.TxBytes, now)
		}
	}
}
