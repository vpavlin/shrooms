# Should the roster survive a restart?

**Status:** open question, Vaclav's. Asked 2026-08-25 about mobile
specifically: would remembering peers across a restart or crash reconnect
faster, assuming most peers are still where we last saw them?

Short answers: **nobody decided against it**, it would help, and the help
arrives from a different direction than it looks like it should.

## "Why did we decide not to persist them?"

We did not. There is no ADR, no commit message and no comment anywhere in the
tree that argues against it. The roster began as derived state — rebuilt from
announces, which arrive anyway — and nothing ever revisited that.

What makes this worth saying plainly is the company it keeps. Five other pieces
of derived state have been persisted one at a time, each after something went
wrong:

| what | why it was added |
|---|---|
| the device's own sequence number | a restarted peer looked replayed to everybody |
| peers' sequence marks (`seqmarks.go`) | a restart reopened the replay window the guard exists to close |
| revocations (`revocations.go`) | "in memory only", so the real answer to *how long is a device revoked* was "until this process exits" |
| what peers offer (`services.go`) | a service list is rare and slow to rebuild |
| bootstrap addresses (ADR-031) | a node that had known its peers for weeks restarted, found the public entry nodes refusing, and had nowhere to look |

**The roster is the last one on that list that has not been done.** So the
honest framing is not "we rejected this", it is "this is the next one, and
nobody has been bitten hard enough yet to notice".

## Would it reconnect faster? Yes — but not because of the announce interval

The obvious story is that announces go out every 45 seconds, so a restarted node
waits up to 45 seconds to learn where anybody is. **That is already not true.**

A node marks its announces `Fresh` for 90 seconds after starting
(`FreshWindow`), which means *I have just started, please announce back*. Peers
receiving a Fresh announce reply immediately rather than waiting for their own
tick — `shouldReplyTo` bypasses the cooldown for exactly this, and it
deliberately does not depend on the tunnel looking dead, because after a restart
our session with a peer stays valid for `REJECT_AFTER_TIME` and we would
otherwise sit silent through the whole window the restarted node is waiting in.

So the roster refills in about one round trip **after the rendezvous plane is
up**. That last clause is where the time actually goes: the Logos Delivery node
has to start, dial bootstrap addresses, and subscribe, and on a phone — cold
radio, possibly a network that just changed — that is the slow part by a wide
margin.

**A persisted roster is the only thing that can give WireGuard something to do
before any of that has happened.** At `t≈0` the peer set could be installed from
disk and handshakes could start, while the rendezvous node is still dialling.
For a peer on a stable endpoint — a VPS relay, a home server, anything not on a
phone — the tunnel could be carrying traffic before the first announce is sent.

That is the whole win, and it is biggest exactly where the question was asked:
**mobile**, where the rendezvous bootstrap dominates and restarts are most
frequent.

One further point in its favour. Only *one* side needs to remember. WireGuard
roams a peer's endpoint to wherever its authenticated packets arrive from, so a
restarted phone that remembers a server's address bootstraps both directions:
the handshake reaches the server at its remembered address, and the server
learns the phone's current address from the packet that carried it.

## Two things that make it cheaper than it looks

**The replay blocker is already gone.** Persisting who a peer is means
persisting where its counter got to, or a restored peer's next announce is
either rejected as a replay or trusted as first-seen. `seqmarks.go` already
persists exactly that, per mesh. The hard half is done.

**`Online()` would do the right thing by itself.** A restored entry should keep
the `LastSeen` it actually had, not be stamped with now — and `PeerInfo.Online`
compares `LastSeen` against `OfflineAfter`, so a remembered peer correctly
presents as *known but offline* until it actually speaks. The UI would not need
a new concept, and it would not lie about who is up.

That last point matches how the services cache already behaves: the claims are
persisted, but `Services()` filters them through the live roster, so a
remembered payload never implies a present peer. **Persist the data, re-derive
the authorisation** is the existing house rule and this fits it.

## What makes it not a two-line change

The reconcile loop **deliberately skips peers that are offline and have no live
tunnel**:

> A peer with no announce for OfflineAfter and no live tunnel is gone; keeping
> it configured means transmitting at a dead address forever.

Restore a roster with honest timestamps and every entry is offline, so nothing
is installed and the feature does nothing. Making it work needs an explicit
provisional window: at startup, install remembered peers for some bounded period
even though they are offline, and let the existing rule take over once it
expires. That window is the actual design decision, and it is where the cost
sits — transmitting at addresses that may be dead is the precise thing the
current rule refuses to do.

## The traps, and how bad each one is

**Revocation.** A device revoked while we were off would be installed from disk
before we hear about it. Revocations are persisted, so the ones we already knew
still apply; the gap is only revocations issued during the outage. Note this
window exists today too — our revocation list is equally stale either way — but
persistence removes the *fresh evidence* an announce carries. Bounded by
credential expiry and by rotation (`admin revoke --rotate`), and the provisional
window bounds it again.

**Expiry.** `m.expired()` already gates the reconcile loop, and expiry is the
mechanism SECURITY.md leans on precisely because it cannot be suppressed. A
restored entry must go through that same gate, not around it.

**Stale endpoints, especially mobile.** A remembered address for a peer that has
since moved is a handshake sent to a stranger. It is cheap and it does not
authenticate, so nothing leaks into a tunnel — but it is battery, and it is
traffic to somebody who did not ask for it. This is adjacent to the incident
where a phone on mobile data pulled 100MB through a stranger's relay, and the
lesson there transfers: bound the list, TTL it, and never let a remembered value
outrank a probed one.

**ADR-009's rule is the one to hold on to**: never set an endpoint that has not
answered a probe. A remembered endpoint is a *bootstrap guess*, which is an
existing category in the reconcile switch — the same slot an announced candidate
occupies today. Persisting the roster does not need a new kind of trust, which
is the strongest argument that it belongs.

## What it would look like

Modelled on `BootPeers`, which solved the same shape of problem:

- one bounded, TTL'd snapshot per mesh, written when the roster materially
  changes (it already knows: `Apply` returns whether anything changed)
- restored with real timestamps, filtered through persisted revocations and
  credential expiry
- installed only as bootstrap-guess endpoints, never as probed ones
- a provisional startup window, after which the ordinary offline rule applies
- shown as *known but offline* until the peer actually announces

## The window: 90 seconds, and not a guess

Vaclav's instinct was around two minutes, reasoning that by then the announces
should have arrived and refreshed everybody. Right conclusion; the reason turns
out to be a better one, and it makes the window shorter and more robust.

**The window only ever culls failures.** If a remembered endpoint still works,
the handshake completes in about a round trip, `st.Live(now)` goes true, and the
existing rule keeps the peer configured whatever the timer says. The timer
cannot kill a peer that is actually reachable — success is self-sustaining, so
the exact value is far lower-stakes than it first appears.

**And it does not need to cover announce arrival.** Tying the window to
announces ties it to the rendezvous bootstrap, which is the one unpredictable
part — a cold radio, or the ADR-031 case where the entry nodes refuse and there
is nowhere to look. A window measured that way can expire before the first
announce has even been sent. What rescues a good peer is the WireGuard
handshake, which needs no delivery plane at all, so the window only has to cover
a handshake attempt.

Both things that could rescue a remembered peer already have a 90-second budget
in this tree:

| constant | what it is |
|---|---|
| `RekeyAttemptTime` = 90s | what wireguard-go spends retrying a handshake before giving up |
| `FreshWindow` = 90s | how long announces are marked Fresh, asking peers to answer |

So the rule is **keep a remembered peer for exactly as long as WireGuard is
still trying to reach it** — derived from an existing constant rather than
chosen, and arriving at the same number from the announce side.

Past 90s wireguard-go has spent its attempt budget and nothing is actively
trying that address any more, which is what makes two minutes slightly too long.
Shorter — 45s, one announce interval — undercuts wireguard-go's own retries to
save about eighteen handshake initiations per dead peer, at the protocol's 5s
REKEY_TIMEOUT. Not a trade worth making.

**90s is also well inside `OfflineAfter` (3 min)**, so a remembered peer that
never proves itself is dropped before the ordinary offline rule would have begun
to worry about it. The provisional window closes strictly earlier than the
normal one: a narrowing rather than a widening, which is the safer direction for
an override to point.

Everything else here follows from choices already made.
