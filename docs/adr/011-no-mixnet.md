# 011. No mixnet in the data path

**Status:** accepted

## Context

The project began with the idea of using libp2p-mix, the Vac/Logos mixnet, to
carry VPN traffic — or at least the signalling — for metadata privacy.

## Decision

No mix in the data path. Keep control messages small and idempotent so mix
remains a one-flag swappable transport if it becomes worthwhile.

## Why

**The decisive argument is the data plane, not latency.** Direct WireGuard
tunnels already tell any path observer which of your sites communicate and when.
Mixing the *announcement* that site A is at IP X, while actual traffic to IP X
is right there, is buying anonymity you immediately hand back. For mix to buy
anything, the data plane would have to be mixed too — which is the performance
premise we are not giving up (ADR-001).

The implementation reinforces it:

- `PathLength = 3` is a **compile-time constant**; tuning means forking.
- Only 2 of 3 hops delay — the exit applies `NoDelay`.
- **Cover traffic is implemented but never wired up** (`WakuMix.new` does not
  pass it), so on logos.dev there is none and timing correlation at the entry
  hop is unmitigated.
- No latency figures exist anywhere; the DST evaluation is marked "not started".
- Filter is out of mix's scope, so you cannot *receive* over mix. The only
  coherent all-mix control plane is publish-via-lightpush plus poll-via-store —
  a polling architecture at ~32 KB per round trip.

Compare Nym, the production reference: **~1 Mbps of permanent cover traffic per
client** and **15 ms** mean delay per hop. Waku mix is configured with 50 ms per
hop and zero cover — the weak half of the Loopix trade, paying latency without
buying unlinkability, because there is nothing to mix with.

## The consolation

A gossip control plane already delivers the metadata win that matters. NetBird's
Signal service holds `{key, remoteKey, timestamp}` tuples — precisely "which of
my sites are talking, and when", aggregated at one party. Gossip has no such
party. That is a bigger real-world improvement than layering mix on top, and it
costs nothing.

## What would change our mind

Cover traffic being enabled by default, published latency figures, and a filter
equivalent that lets a node receive over mix.
