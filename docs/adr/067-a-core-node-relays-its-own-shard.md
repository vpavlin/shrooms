# 067. A Core node relays its own mesh's shard, not the whole network

**Status:** accepted 2026-10-20 by the owner; being built. Amends what Core
meant since ADR-003 and `state.Config.Mode`'s note ("Core relays for the
network"). The patch goes when the July pin does: v0.40 carries upstream's fix
(ADR-071).

## Context

On 2026-10-19 the VPS moved about 356 GB of delivery traffic in a day, against
about 5 GB a day the week before. Measured on 2026-10-20:

- 80% of it was shard 1 of logos.test (`/waku/2/rs/2/1`). Shrooms uses shard 3:
  every shrooms topic routes there, since autosharding places a topic by its
  application name.
- Shard 1 was entirely Scala. Test sessions on 2026-10-19 sent catch-up rounds
  that never converged, and the reliable-channel layer (SDS) puts about 18 KB of
  bloom filter on every message. Scala's author has stopped the sessions and is
  cutting both.
- Relaying multiplies what it carries about 9×: each message arrives from about
  4 peers and goes out to about 5.

A Core node relays every shard of the cluster. That was meant as the
neighbourly setting, and on an idle network it cost 0.5 GB a day. It also
means any application's load on any shard lands on our VPS and on every
desktop running Core.

Setting `shards` did not help. The pinned library subscribed relay to every
shard at start whatever `shards` said. Upstream fixed that on 2026-10-15
(logos-delivery #4513, 7253a291). The fix isn't in a release yet, and our pin
is older.

## Decision

- **A Core node relays the one shard its meshes use**
  (`topic.MeshShard`), and no other.
- **`relay_all_shards = "true"`** in the config brings back relaying every
  shard, for a node whose operator wants to carry the network.
- **The library is patched until the pin passes 7253a291.** The patch is the
  same change, written against the pinned revision
  (`docker/patches/0001-subscribe-only-configured-shards.patch`, applied in
  `docker/build-lib.Dockerfile`). Unset, `shards` is every shard, so nothing
  else built from that library changes.
- **Edge nodes are untouched:** they relay nothing. Phones run Edge (ADR-035),
  so the Android library, which we fetch prebuilt, needs no patch.

## Consequences

- The VPS and desktops stop carrying other applications' shards. They still
  carry everything on shard 3, which other applications share with us (the
  `/logos/1/…` topics, for one): about 61 KB/s on the VPS on 2026-10-20, a
  twentieth of its total that day. Shrooms' own announces are well under
  1 MB/h of it.
- Shrooms still serves the network on its own shard: members' announces, and
  filter and lightpush for Edge phones on that shard.
- **Less neighbourly than before.** A node that would have relayed for
  strangers on other shards no longer does. That is the trade: relaying
  someone else's load was never what a member signed up for, and an operator
  who wants it can turn it back on.
- **The patch goes when the pin moves.** It is applied only where the old
  all-shards call is still there, so the nightly build of upstream master,
  which has the fix in its own words, is unaffected. And the build fails if
  that call survives, so the patch cannot stop applying unnoticed.
