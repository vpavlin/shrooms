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

## On Akash

`relay.yaml` here is a starting point, and is **not yet verified against a live
provider** — it is written from the SDL documentation rather than from a
deployment that has run. The IP lease is the part most likely to need
adjustment.

The lease is also the cost that cannot be designed away. Akash's standard
ingress terminates HTTP and HTTPS, and a relay is UDP, so it needs a dedicated
public IPv4 — usually more than the compute underneath it. What Akash gives in
return is unmetered traffic, which for a relay is the resource that matters.

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
