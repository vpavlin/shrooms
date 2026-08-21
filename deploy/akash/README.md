# Running a blind relay

A blind relay forwards packets between devices that cannot reach each other
directly, for meshes it is not a member of. It cannot read what it carries —
the packets are WireGuard, encrypted between two devices whose keys it does not
have — and it never learns which mesh anything belongs to.

Read [docs/blind-relays.md](../../docs/blind-relays.md) before running one for
strangers. In particular: **this is an experiment, offered with no guarantees of
any kind.** It has not been audited, it may lose your traffic, and it may be
withdrawn at any time. It is shared so people can poke at it.

## What the operator can and cannot see

**Cannot:** message contents, any network key, who is on a mesh, device names,
or anything from a control plane.

**Can:** opaque per-relay tags, the IP addresses those tags connect from, which
pairs of tags exchange packets, how much, and when. That is a traffic-analysis
surface. It is smaller than it sounds — the tags are meaningless off this
particular relay, so two operators comparing notes see unrelated numbers — and
it is not nothing.

## Anywhere a container runs

    docker run -d --name shrooms-relay \
      -p 51820:51820/udp \
      -e SHROOMS_RELAY_BYTES_PER_SECOND=12500000 \
      ghcr.io/vpavlin/shrooms-relay:latest

The image is about 2.5 MB and runs on scratch as an unprivileged user. It keeps
no state, needs no volume, and holds no identity, so redeploying it costs one
refresh interval of relayed traffic and nothing else.

Build it yourself with:

    docker build -f docker/relay.Dockerfile -t shrooms-relay .

Check any relay, yours or somebody else's, with `shrooms-relay -probe` — see
below.

## Deployments are paid in ACT, not AKT

Worth knowing before the first attempt, because the failure is opaque. Creating
a deployment with an AKT deposit is refused by the chain outright:

    Deposit invalid ... with gas used: '35786': unknown request

That is not a configuration mistake. `x/deployment/handler/server.go` rejects it
by design:

```go
// AKT deposits are only allowed via AccountDeposit (existing deployment)
if msg.Deposit.Amount.Denom == sdkutil.DenomUakt {
    return nil, v1.ErrInvalidDeposit
}
```

So AKT can top up a deployment that already exists, and cannot create one.
Deposits and pricing are both in **`uact`**, which you mint from AKT:

    provider-services tx bme mint-act <amount>uakt --from <key> ...

Mint **at least 10 ACT** — smaller amounts have been reported not to become
spendable. There is an open issue where minted ACT does not appear in
`query bank balances` at all
([akash-network/support#445](https://github.com/akash-network/support/issues/445)),
so if `deployment create` then fails with `insufficient balance` rather than
`Deposit invalid`, that is a known problem with the chain rather than with
anything here.

The `pricing.denom` in both descriptors is `uact` for the same reason.

## Where to deploy from

**https://air.akash.network/** — Console Air, the console that takes **AKT** as
payment. The one the docs send you to, `console.akash.network`, is not the same
thing, which is easy to lose track of when you only do this occasionally.

## Publish the image first

The descriptors reference `ghcr.io/vpavlin/shrooms-relay:latest`, which does not
exist until somebody pushes it. Akash providers are amd64, so build for that
explicitly:

    docker build --platform linux/amd64 \
      -f docker/relay.Dockerfile -t ghcr.io/vpavlin/shrooms-relay:latest .
    docker push ghcr.io/vpavlin/shrooms-relay:latest

It has to be public — a provider pulls it with no credentials of yours.

## On Akash

Two descriptors here. **Start with `relay-noip.yaml`.**

`relay.yaml` takes an IP lease, which is the expensive part — often more than
the compute under it. What a lease actually buys is the ability to *choose* a
port. Akash's own announcement puts the limitation it removed plainly:

> some services (like a VPN, for example) must use standard ports in the 0-1024
> range, which isn't possible unless you have a dedicated IP

Before leases you were still exposed, on a port the provider picked; you just
had no say in which. **A relay does not need a say.** It is reached at whatever
address and port its users are configured with, and that is an arbitrary value
either way. So `relay-noip.yaml` exposes UDP with no lease, the provider assigns
a high port, and `provider lease-status` tells you which.

The real cost of going without is that the assigned port can change when the
deployment is recreated, so the address is not stable across redeploys. For a
relay handed out alongside a token, that is a line in a message you were sending
anyway.

**Neither has been run against a live provider.** They are written from the SDL
documentation. The part most worth checking is whether a given provider forwards
UDP this way at all — Kubernetes NodePort supports it, but that is not a promise
about every Akash provider.

### Checking it works

    shrooms-relay -probe <host>:<port>

This is a client, not an inspection: it stands up two throwaway devices,
registers both the way a real one would, and relays a packet between them. A
pass means the whole path works — NAT, load balancer, forwarded port and all —
rather than that something is listening.

    probing relay.example.com:31234 (no token)
      first   device registered in 41ms (challenge answered)
      second  device registered in 39ms (challenge answered)
      packet relayed in 42ms

    OK — relay.example.com:31234 forwards, and cannot be pointed at an address
    that does not answer

A failure names the likely causes, since from outside they look identical:
unreachable, port not forwarded, or the wrong token.

Add `-token <t>` if the relay wants one.

## Settings

| variable | default | what it does |
|---|---|---|
| `SHROOMS_RELAY_PORT` | 51820 | UDP port |
| `SHROOMS_RELAY_TOKEN` | *(empty)* | require a token; empty means open |
| `SHROOMS_RELAY_BYTES_PER_SECOND` | 0 | total ceiling; 0 is unlimited |
| `SHROOMS_RELAY_PEER_BYTES_PER_SECOND` | 0 | per-device ceiling |
| `SHROOMS_RELAY_MAX_REGISTRATIONS` | 512 | table size |
| `SHROOMS_RELAY_MAX_PER_SOURCE` | 8 | registrations one IP may hold |

Set a bandwidth ceiling. An open relay without one is an unbounded claim on a
machine you are paying for, and the relay says so on startup rather than letting
you find out from a bill.

## Is it safe to leave open?

Open does not mean unauthenticated in the way that usually matters. The danger
with an open forwarder is **reflection** — someone points it at a third party
and it sends traffic there. A registration here installs nothing until the
registrant echoes a nonce sent to the address it gave, which an attacker
registering somebody else's address never receives. Relaying is also one packet
in, one packet out, so there is no amplification to be had.

What is left to abuse is bandwidth, which is what the ceilings are for. A token
is about choosing *who* uses it, not about safety.
