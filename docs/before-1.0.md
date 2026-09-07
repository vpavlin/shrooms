# Before 1.0

**Status:** state of play, 2026-08-27. Vaclav's list, plus what this week left
open. [roadmap.md](roadmap.md) is from 2026-08-20 and predates the Keycard work;
this does not replace it, it says where things actually stand.

## Proven

**A mesh whose authority is a Keycard, end to end.** Mint, invite, revoke, all
signed on the card, against real hardware — not only in the container e2e. The
card never gives up its key, and revocation was watched from both sides: the
laptop dropped the peer and the phone reported the tunnel gone.

Also proven this week, mostly by breaking: two devices on one LAN connect
directly with no relay ([one-kind-of-mesh.md](one-kind-of-mesh.md) — every mesh
but the first was announcing the wrong port), remembered peers survive a restart
and reconnect in under half a second, and the reader path needs no build tag and
no build dependency.

## Not tested

**The phone as the admin — and half of it is not built.** Checked 2026-08-27
rather than assumed, which changed the answer.

`AdmitWithCard` exists: the phone can admit a device to a mesh it did not mint,
signing on the card. That half is built and has never been run, so "a phone is a
full admin" is a claim rather than a fact.

The other half does not exist. The app's create-a-mesh calls `Mobile.init`,
which writes a `NetworkKey` and **no admin keys** — an authority-less mesh, the
pre-credential kind, with nothing revocable. There is no mobile equivalent of
`mintCardAuthorityFull`. So a phone cannot mint a card-backed mesh at all, and
`shrooms init` minting an authority by default while the app's equivalent does
not is also a parity gap.

That matters more since joining by network key was removed: an authority-less
mesh is the one shape left where membership rests on holding a key.

The APK that can do it is published (versionCode 62); the phone needs a fresh
invite to whatever mesh it should be on, since it was revoked while testing.

## Needs a decision

**Round two of an enrolment is published once and cannot be asked for again.**

The daemon's second reader on `node.Events()` is fixed (2026-09-07), and that
was the cause of enrolments failing about half the time with

    the mesh answered but did not issue a credential: context deadline exceeded

on the joiner while the inviter printed `Admitted`. But the reason a single lost
message could end the exchange at all is still there, and Waku will lose an
ephemeral message occasionally without anybody's help.

The asymmetry is in `internal/mesh/invite.go:handleInvite`. A **first**-round
request is answered every time it is seen — `answerDeferred` runs per request —
so the joiner's five-second retry recovers a lost answer for free. A
**second**-round request is pushed to `held.reqs` once and every repeat is
dropped by the `default:` arm, because "an invite admits one device". The
credential response is therefore published exactly once. Lose it and the joiner
retries into silence until its deadline, having already consumed the invite.

The fix would be to remember the response published for an invite and re-publish
it when the *same* request arrives again. It is safe as far as I can tell: a
retry inside one `invite.Redeem` re-sends the identical sealed blob, same
`EphPub` and same `DevicePub`, so a cached response still opens for it and for
nobody else.

**It is your call because it touches what "once" means.** Today the guarantee is
enforced by the response existing once. Afterwards it would be enforced by the
cache key — same device, same ephemeral key — which is a different and slightly
weaker statement, and it is the sort of thing worth deciding deliberately rather
than discovering later.

**Whether `:latest` should wait for arm64.**

`image-manifest` needs both architectures, so a broken arm64 build freezes
`:latest` for amd64 hosts too — which is why vps could not be updated on
2026-09-07 and had to pull `:<sha>-amd64` by hand. The arm64 job has been red
since at least 2026-09-04.

The comment guarding this is right about the thing it guards: pushing a
single-arch *image* to `:latest` hands every arm64 host an amd64 image, silently.
But a *manifest list* containing only amd64 is a different animal — an arm64
host pulling it gets "no matching manifest for linux/arm64", which is a refusal,
not a wrong image.

So the manifest could be assembled from whichever arches pushed, with the job
failing loudly afterwards if one is missing. amd64 keeps moving; arm64 gets an
honest error instead of a stale image. **Your call** — it trades "everyone waits
for the slowest arch" for "arm64 can be behind, visibly".

**A configured relay that has never answered still beats a live discovered one.**

`internal/mesh/paths.go:206`, `selectRelay`:

```go
if len(m.relays) > 0 {
    if t, ok := m.liveRelay(now); ok {
        return relayChoice{ok: true, addr: t.addr}
    }
    return relayChoice{ok: true, addr: m.relays[0].addr}
}
```

"Configured relays override discovery" is right, and the reason given for it is
right: every device with the same list agrees on one relay without negotiating,
which matters because a relay only forwards between peers that have BOTH
registered with it (`internal/relay/server.go:328`).

What is not right is the second return. When no configured relay is live, this
picks the first one anyway and discovery is never reached — so a stale or
mismatched blind relay permanently hides a member relay that is up, reachable
and already carrying traffic.

Found on 2026-09-07: the laptop had a blind relay pinned, k11 was reachable only
through a relay, and vps was sitting there as a live member relay with working
tunnels to both. The laptop kept sending into the dead one — 2.0K out, 0 back —
and never looked at vps. The fix on the day was to unpin the blind relay by
hand.

**Your call what the fallback should be.** Falling through to discovery when
nothing configured is live is the obvious answer and it breaks the "everyone
agrees without negotiating" property — two devices could fall through at
different moments and pick differently. Preferring a live discovered member
relay over a configured one that has never once answered is narrower and keeps
that property in every case that currently works.

## Wants an outside look

**The CLI.** Vaclav's instinct on 2026-08-27, and it is right: the shape has
grown by accretion and the people closest to it are the worst placed to see it.
`init` creates a mesh but `mesh remove` deletes one; `prepare` is device setup
that reads as a niche flow; `admin issue --name` and `admin revoke --name` mean
different things by the same word. Some of that is written up in
[where-mesh-commands-live.md](where-mesh-commands-live.md), and that document is
itself a proposal from inside the project.

**An assistant that has worked on it is not an unbiased reviewer of it** — much
of the current surface was shaped in the same sessions that would be reviewing
it, including the parts most likely to be wrong. The useful version is a fresh
model given the binary and no history, and a person who has never used it,
watched rather than asked: where they stop, what they type that does not exist,
what they assume undoes what.

**Basecamp's UI** has not been touched in weeks while the CLI and the app both
moved. Parity between the desktop module and the Android app is a stated goal;
nobody has checked lately whether it still holds.

## Open, not urgent

- The delivery plane reconnects ~23 times an hour and re-subscribes each time.
  Found 2026-08-27 while chasing something else; low bandwidth, unexplained, and
  the sort of thing that shows up as battery on a phone rather than bytes on a
  laptop. Worth re-measuring now that the daemon no longer runs two readers on
  the node's event channel — half of every mesh's rendezvous traffic was going
  to the invite transport and being discarded, which is exactly the shape of
  "discovers peers slowly, in one direction only".
- `shrooms config flatten` exists and has not been run on any node. The 78
  remaining "which shape is this mesh" branches cannot go until it has.
- `advertise` is per mesh now; whether the relay settings follow is decided
  (they inherit, and a mesh may opt out).
- Three release tags hold a 31 MB library. `shrooms-relay` and
  `android/logosvpn-sources.jar` are both tracked while `.gitignore` claims to
  ignore them, so those rules do nothing — one `git rm --cached` each.
- [cli-review-2026-08.md](cli-review-2026-08.md) is an outside review of the
  CLI. Its cheapest findings: `shrooms --help` never mentions the `mesh`,
  `services` or `keycard` groups, and `-h` errors on exactly those groups.

## After 1.0

**Re-doing the meshes**: consolidating to `home` and `office`, on card
authorities, one account per mesh. Deliberately after the release — it voids
credentials and re-invites every device, which is not a thing to do while also
trying to cut a version.
