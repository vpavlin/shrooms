package mesh

import (
	"testing"
	"time"
)

// A rate needs two readings. Reporting one from the first sighting would show a
// peer's entire lifetime transfer as if it happened in one tick.
func TestNoRateFromASingleSample(t *testing.T) {
	r := newRates()
	now := time.Now()
	r.observe("p", 1_000_000, 500_000, now)

	if got := r.rate("p"); got.RxBps != 0 || got.TxBps != 0 {
		t.Errorf("claimed %v/%v bps from one sample", got.RxBps, got.TxBps)
	}
}

func TestRateFromTwoSamples(t *testing.T) {
	r := newRates()
	now := time.Now()
	r.observe("p", 0, 0, now)
	r.observe("p", 30_000, 15_000, now.Add(3*time.Second))

	got := r.rate("p")
	// Smoothed from zero, so the first reading is a fraction of the true rate;
	// what matters is that it is positive and proportional.
	if got.RxBps <= 0 || got.TxBps <= 0 {
		t.Fatalf("no rate after two samples: %v/%v", got.RxBps, got.TxBps)
	}
	if got.RxBps <= got.TxBps {
		t.Errorf("rx %v should exceed tx %v, given twice the bytes", got.RxBps, got.TxBps)
	}
}

// Counters reset when a peer is reconfigured or restarts. Differencing across
// that produces a huge negative, which as an unsigned subtraction becomes an
// absurd spike — a transfer that never happened.
func TestCounterResetDoesNotSpike(t *testing.T) {
	r := newRates()
	now := time.Now()
	r.observe("p", 0, 0, now)
	r.observe("p", 10_000_000, 10_000_000, now.Add(3*time.Second))
	r.observe("p", 100, 100, now.Add(6*time.Second)) // peer restarted

	if got := r.rate("p"); got.RxBps != 0 || got.TxBps != 0 {
		t.Errorf("rate after a counter reset = %v/%v, want 0", got.RxBps, got.TxBps)
	}
}

// History is bounded: this is a sparkline, not a metrics store.
func TestHistoryIsBounded(t *testing.T) {
	r := newRates()
	now := time.Now()
	for i := 0; i <= RateSamples+20; i++ {
		r.observe("p", uint64(i*1000), uint64(i*500), now.Add(time.Duration(i)*time.Second))
	}
	got := r.rate("p")
	if len(got.RxHistory) > RateSamples {
		t.Errorf("kept %d samples, cap is %d", len(got.RxHistory), RateSamples)
	}
}

// The returned history must be a copy, or a caller ranging over it races the
// sampler.
func TestHistoryIsACopy(t *testing.T) {
	r := newRates()
	now := time.Now()
	r.observe("p", 0, 0, now)
	r.observe("p", 1000, 1000, now.Add(time.Second))

	h := r.rate("p").RxHistory
	if len(h) == 0 {
		t.Fatal("no history")
	}
	h[0] = -1
	if r.rate("p").RxHistory[0] == -1 {
		t.Error("history is shared with the caller")
	}
}
