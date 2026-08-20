# What to build next

**Status:** agreed 2026-08-20. Two priorities, in order.

The comparison with [nostr-vpn](comparison.md) prompted this, but it is not what
decided it. The deciding evidence was a first-time user spending a day on a
mesh that never connected, while every failure along the way was silent: a
daemon nudge that did not land and said nothing, a firewall line documented only
under "cloud server", a publish pipeline shipping to a directory nobody served,
and a deadlock whose only symptom was a handshake retrying forever.

A project that spreads one friend at a time is won or lost in the first hour.

## 1. The first hour

**Turn on [ADR-031](adr/031-bootstrap-from-the-mesh-itself.md).** Built, tested,
and switched off: no node sets `delivery_port`, so nothing publishes a bootstrap
address and nobody persists one. One config line on a public relay closes the
one architectural point where nostr-vpn is genuinely better placed — they touch
third-party infrastructure only at enrolment, we announce over it every 45
seconds, and on 2026-08-20 that difference took a node off the mesh.

**Blind relays** ([blind-relays.md](blind-relays.md)). "You need a reachable
machine first" is the barrier that stops this spreading between friends — it is
exactly what stopped one. Scoped small: a token for policy, a
return-routability check for safety, trust-on-first-use for ownership.

**Say why, not what.** Some of this landed on 2026-08-20 — `paths` now shows
what a node announces about itself, `invite` explains a waiting daemon instead
of reporting 404, `init` reports whether the daemon actually picked the mesh up.
What is left is the case that cost the day: a node that announces good addresses
and never receives a packet. The daemon has every fact needed to say
"peers can see this device and nothing reaches it — check the firewall", and
says nothing.

## 2. Keycard

The authority on a card rather than a disk, tapped when it is needed
([keycard-on-mobile.md](keycard-on-mobile.md)). Half built: the card can be
paired, read and proved, and cannot yet issue anything.

This is the one thing in the space nobody else appears to be doing, and it makes
"no coordinator" concrete rather than architectural — the authority stops being
a file on the machine that happens to be up and becomes an object in a pocket.

## Not doing

**iOS.** nostr-vpn ships it and we will not, and the reason is substrate rather
than effort: websockets to a relay fit inside a Network Extension's memory
limit, a libp2p node does not ([ADR-022](adr/022-keycard-for-the-admin-key.md)).
Matching it means replacing the rendezvous plane, which is the spine of the
project. Not worth it to match a competitor.

**Release velocity as a goal.** They shipped eleven releases in seven days. The
useful lesson there is a publish pipeline that works, which is now fixed; the
useless one is treating release count as progress.
