# Where next: the Logos ecosystem, and places where the internet is restricted

**Status:** strategy note, nothing built. Three decisions at the end.

Two goals, asked together: leverage more of the Logos stack, and be useful where
internet access is restricted. They overlap less than they look, and the second
one has a gap much larger than obfuscation.

## The headline gap: there is no exit

Shrooms connects **your own machines to each other**. It does not route your
traffic to the open internet through a peer. There is no exit node, no default
route through the tunnel, no `AllowedIPs = 0.0.0.0/0` — peers get mesh addresses
in `fd…/48` and traffic to anywhere else takes the normal path.

For the office/home/VPS case that is exactly right, and is why the tunnel is
fast and the design stays small.

For somebody behind a national firewall it means Shrooms is **not a
circumvention tool at all**. They do not primarily need their phone to reach
their NAS. They need to reach the open internet from a country that will not let
them, which means traffic leaving through a machine somewhere else. Today,
nothing here does that.

Everything else in this note is secondary to that. Obfuscating a tunnel that
cannot carry the traffic somebody needs is polishing the wrong thing.

**Relay and exit are not the same job**, and the difference matters for whoever
volunteers:

| | what it carries | what the operator risks |
|---|---|---|
| relay | encrypted mesh traffic between members | nothing legible; it cannot read any of it |
| exit | your traffic to the open internet, under its own IP | whatever the user does, attributed to them |

This is why Tor has far fewer exits than relays. A blind relay is a small
favour. An exit is not, and the project should not blur them.

## The chokepoint underneath everything: bootstrap

A node needs the rendezvous plane before it can find anything. That comes from a
preset or explicit entry nodes:

```go
if c.Preset == "" && len(c.EntryNodes) == 0 {
    return errors.New("preset is empty and no entry_nodes are set — " +
        "the node would have nowhere to bootstrap from")
}
```

`logos.test` resolves through DNS discovery; entry nodes are addresses. **Both
are blockable** — a DNS-based bootstrap is a DNS lookup somebody can poison, and
a fixed address list is a list somebody can drop.

Tunnels already up survive this, and that is a genuine strength: an established
mesh keeps working with the fleet unreachable. But cold start and roaming are
exactly when a phone needs discovery, and exactly what a censor can deny.

This is not a Shrooms problem to solve alone — it is the ecosystem's, and it is
the sharpest place where "fitting into Logos" cuts both ways. We inherit Waku's
censorship resistance, including its bootstrap exposure. If Logos ships bridge-
style unlisted entry points, Shrooms gets them free. If it does not, no amount
of work here compensates.

## Where the Logos fit actually is

The obvious reading of "leverage more of the stack" is to add components. That
is mostly not where the value is:

- **Codex** (storage) has no obvious role in a VPN data plane.
- **Nomos** could underpin paying relay operators, which is the incentive
  problem [ADR-012](adr/012-relay-hosting.md) names. Interesting, heavy,
  speculative, and it would make this a different project.
- **RLN** already has a decision recorded in
  [ADR-028](adr/028-when-the-fleet-turns-on-rln.md).

The more interesting direction is the inversion: **Shrooms as transport for the
ecosystem, rather than a consumer of it.**

Every Logos application that wants two users' devices to talk directly has the
same problem this project already solved — NAT traversal, identity, addressing,
relays. And the seam exists: [ADR-025](adr/025-control-from-a-desktop-app.md)
made the control socket the interface, deliberately with no HTTP port, and the
Basecamp module already uses it. An application could ask for a path to a peer
instead of building its own.

That is a real fit, it uses what is already built, and it does not require
adopting anything new.

## What the restricted-internet user actually needs, in order

1. **An exit.** Without it nothing else applies.
2. **Bootstrap that survives blocking.** Mostly upstream of us.
3. **Traffic that does not announce itself** — [obfuscation.md](obfuscation.md),
   starting with the `mvpn` prefix currently sent in the clear.
4. **Somewhere to exit through**, which is the relay-shortage problem with
   higher stakes attached.

Note that 1 and 4 are the same problem seen twice: the technical capability and
the human willing to carry the risk. The second is harder.

## The tension worth naming

The office/home/VPS mesh wants direct paths, low latency and minimal moving
parts. The restricted-internet user wants everything relayed, obfuscated, exiting
somewhere else, and resilient to its bootstrap being attacked.

Those pull apart. Serving both means a mode, not a default — and modes are the
honest answer here, because the projects that ship both consistently find that
most people use the fast one.

It also means the second audience brings a duty the first does not: somebody
relying on this to get past a national firewall is taking a risk on our
behaviour, and "prototype, used daily by its author" is not a promise that
supports that. Being useful there implies an audit, a threat model written for
that user, and a great deal more care about failure modes than a personal mesh
needs.

## Three decisions

1. **Is an exit node in scope?** It is the difference between a private mesh and
   a tool somebody under censorship can use. It is also a meaningful amount of
   work — routing, DNS leak prevention, a kill switch, Android's VpnService
   route configuration — and it hands real risk to whoever runs one.

2. **Transport for other Logos apps: yes or no?** The socket interface makes
   this cheap and it plays to what is already built. It is the strongest
   ecosystem fit available without adopting new components.

3. **Which audience is this for?** Both is possible, as modes. But the second
   audience deserves promises this project cannot make yet, and saying so is
   better than implying otherwise.
