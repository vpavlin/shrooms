# The bootstrap address an invite carries, and the path that ignores it

**Status:** open. Found 2026-09-08 by reading, not by being bitten — the join
that prompted the reading failed for
[another reason](dns-the-library-insists-on.md) entirely.

[ADR-031](adr/031-bootstrap-from-the-mesh-itself.md) exists because of a real
outage. On 2026-08-20 five of the six public entry nodes refused TCP on 30303,
a laptop restarted, and it could not rejoin anything. Every other machine
stayed up — not because they were healthier, but because they were already
connected and never had to bootstrap again.

Its third part is that **an invite carries a bootstrap address**, so a device
being admitted has a way into the rendezvous plane that does not depend on the
public fleet answering. That part reaches one of the two join paths.

## The two paths

`shrooms join <TOKEN>` runs the same exchange from either end of a fork:

**Direct** — `--local`, or a machine with no daemon. `cmdJoinInvite` builds a
node then and there, and puts the token's address into its entry nodes:

```go
if boot := invite.BootFromToken(token); boot != "" {
    fleet.EntryNodes = append(fleet.EntryNodes, boot)
    fmt.Fprintf(out, "Connecting to the fleet via %s...\n", boot)
}
```

**Via the waiting daemon** — the default on a machine set up with `prepare`,
and what the install page tells everybody to do. `joinViaDaemon` POSTs the
token to the control socket; `joinHere` runs the exchange over the transport
the daemon **already has**. That node was constructed at startup from
`waitingFleet(cfgPath)`, which reads preset, mode, cluster and the config's
`entry_nodes` — and nothing from the token. The token is parsed for its secret
and for nothing else.

`BootFromToken` has exactly one non-test caller, which is how this stayed
invisible.

## Why it is not a one-line fix

ADR-031 says it plainly, in the course of explaining why learned addresses are
written to disk:

> Bootstrap addresses are consumed when the delivery node is constructed and
> the library offers no way to add one to a running node.

So the waiting daemon cannot be handed an address mid-exchange. It would have
to persist it and start again — which is less far-fetched than it sounds,
because that daemon already re-executes itself the moment a join succeeds,
precisely so the data plane is wired by the ordinary startup path.

## Options

1. **Persist and retry.** On `/join`, if the token carries an address, write it
   to `boot-peers.json` and re-exec before redeeming. Costs a restart on every
   invite redeemed through a daemon, including the overwhelming majority that
   did not need one.
2. **Persist and retry only on failure.** Redeem first; if it fails and the
   token carried an address the node has not got, persist, re-exec, and try
   once more. Slower in the failing case, free in the normal one, and the
   failing case is already two minutes of waiting.
3. **Refuse and redirect.** If the daemon has no peers and the token carries an
   address, do not attempt the exchange at all — tell the user to run
   `shrooms join <TOKEN> --local`, which already works. Cheapest, honest, and
   leaves a manual step in the path ADR-031 was meant to automate.

(2) looks right. (3) is worth doing anyway as the error message for whatever
remains broken.

## Reproducing it

Harder than it should be, which is the other reason nobody noticed:

- The inviter must actually publish an address. `bootAddrFor` requires Core
  **and** `relay` **and** a pinned `delivery_port` **and** a public IP — a home
  laptop satisfies none of the last three, so a home mesh puts nothing in its
  invites and this whole mechanism is dormant.
- The joiner must be unable to reach the public entry nodes. Blocking DNS to
  `1.1.1.1` does it, which is exactly what a machine running Mullvad does for
  free — see [the other note](dns-the-library-insists-on.md).

So the test rig is: a VPS with `--relay` and a pinned delivery port as the
inviter, and a laptop with its VPN on as the joiner. Both halves of that are
sitting around already.

## What it does not affect

Nothing that is currently working. When the fleet is up, every path resolves
its entry nodes and the token's address is redundant. This only bites on the
day ADR-031 was written for — which is the day nobody will want to be
discovering that the escape hatch is on the other route.
