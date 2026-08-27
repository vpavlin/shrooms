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

**The phone as the admin.** `init` a mesh on the phone, then invite the laptop
to it. Every part exists and the app can hold the card over NFC, but that
direction has never been run — everything so far has been the desktop admitting
the phone. Until it is, "a phone is a full admin" is a claim rather than a fact.

The APK that can do it is published (versionCode 62); the phone needs a fresh
invite to whatever mesh it should be on, since it was revoked while testing.

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
  laptop.
- `shrooms config flatten` exists and has not been run on any node. The 78
  remaining "which shape is this mesh" branches cannot go until it has.
- `advertise` is per mesh now; whether the relay settings follow is decided
  (they inherit, and a mesh may opt out).
- Three release tags hold a 31 MB library, and `shrooms-relay` is tracked while
  `.gitignore` claims to ignore it.

## After 1.0

**Re-doing the meshes**: consolidating to `home` and `office`, on card
authorities, one account per mesh. Deliberately after the release — it voids
credentials and re-invites every device, which is not a thing to do while also
trying to cut a version.
