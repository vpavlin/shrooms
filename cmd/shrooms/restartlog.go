package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// How often the rendezvous watchdog may restart the daemon.
//
// The watchdog restarts the process when the rendezvous plane has been unusable
// for rendezvousStall. That is the right answer when a restart fixes it, and
// it usually does. When it does not — the fleet is unreachable, the network is
// down, this machine's NAT has changed — the process exits, comes back, resets
// its own clock, and does it again ten minutes later, indefinitely.
//
// That loop is worse than doing nothing, because the thing it destroys is the
// half that still works. `status` says so out loud: "Peer discovery is stalled;
// established tunnels are unaffected." A restart affects them — every mesh's
// tunnels drop, published services stop accepting, and discovery starts from
// nothing. Trading a working data plane for another attempt at a control plane
// that is not coming back is a bad trade at any interval, and an appalling one
// on a loop.
//
// So consecutive restarts back off: ten minutes, then twenty, forty, and so on
// to a ceiling. A node whose fleet is genuinely gone settles into checking
// every couple of hours instead of tearing itself down six times an hour, and a
// node whose problem was momentary is unaffected because the first restart is
// not delayed at all.
//
// The counter has to survive the restart, which is the whole difficulty: the
// process exits, so it lives in a file beside the state rather than in memory.

// restartBackoffCeiling bounds the wait. Two hours is long enough that a
// hopeless node stops hurting itself, short enough that a fleet coming back is
// noticed the same afternoon.
const restartBackoffCeiling = 2 * time.Hour

// restartForgetAfter is how long a node must stay healthy before its restart
// history stops counting.
//
// Longer than the longest backoff, so a node cannot alternate between "briefly
// healthy" and "restarting" and keep resetting itself to no delay. Shorter than
// a day, so yesterday's outage does not penalise this morning.
const restartForgetAfter = 3 * time.Hour

// restartLog remembers how often the watchdog has restarted us recently.
type restartLog struct {
	path string
	// Count is how many consecutive restarts have been made without a
	// sustained recovery in between.
	Count int `json:"count"`
	// Last is when the most recent one was made.
	Last time.Time `json:"last"`
}

func loadRestartLog(stateDir string) *restartLog {
	r := &restartLog{path: filepath.Join(stateDir, "rendezvous-restarts.json")}
	body, err := os.ReadFile(r.path)
	if err != nil {
		return r // absent or unreadable: no history, which is the safe default
	}
	// A corrupt file means no history rather than a failure to start. The
	// worst case is one un-delayed restart, which is what a fresh node does
	// anyway.
	_ = json.Unmarshal(body, r)
	return r
}

// wait is how long this node must be unhealthy before restarting again.
//
// Zero for the first restart, so nothing about the common case changes.
func (r *restartLog) wait(base time.Duration) time.Duration {
	if r.Count <= 0 {
		return 0
	}
	d := base
	for i := 1; i < r.Count && d < restartBackoffCeiling; i++ {
		d *= 2
	}
	if d > restartBackoffCeiling {
		d = restartBackoffCeiling
	}
	return d
}

// ready reports whether enough time has passed since the last restart, and how
// much is left if not.
func (r *restartLog) ready(now time.Time, base time.Duration) (bool, time.Duration) {
	if r.Last.IsZero() {
		return true, 0
	}
	// A node that has been healthy for a good while is not in a loop, whatever
	// its history says. Checked against wall-clock time rather than uptime
	// because the point is that the fault stopped happening.
	if now.Sub(r.Last) >= restartForgetAfter {
		return true, 0
	}
	w := r.wait(base)
	if elapsed := now.Sub(r.Last); elapsed < w {
		return false, w - elapsed
	}
	return true, 0
}

// note records a restart about to happen.
func (r *restartLog) note(now time.Time) {
	if !r.Last.IsZero() && now.Sub(r.Last) >= restartForgetAfter {
		r.Count = 0
	}
	r.Count++
	r.Last = now
	r.save()
}

// clear forgets the history, called when the plane has been healthy long
// enough that whatever was wrong is over.
func (r *restartLog) clear() {
	if r.Count == 0 {
		return // nothing to write, and this runs on a timer
	}
	r.Count = 0
	r.Last = time.Time{}
	r.save()
}

func (r *restartLog) save() {
	body, err := json.Marshal(r)
	if err != nil {
		return
	}
	// Best effort throughout. A node that cannot write this still works; it
	// merely forgets it was restarting, which is the behaviour before this
	// file existed.
	_ = os.WriteFile(r.path, body, 0o600)
}

// String is for the log line, so an operator can see why nothing is happening.
func (r *restartLog) String() string {
	if r.Count == 0 {
		return "no recent restarts"
	}
	return fmt.Sprintf("%d consecutive restarts, last %s ago",
		r.Count, time.Since(r.Last).Round(time.Second))
}
