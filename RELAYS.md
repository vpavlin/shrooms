# Blind relays

A relay forwards packets between two devices that cannot reach each other
directly — a phone on mobile data and a machine behind a home router, most
commonly. A **blind** relay does that for meshes it is not a member of: it holds
no network key, has no roster, and cannot read a byte of what it carries
(see [docs/blind-relays.md](docs/blind-relays.md)).

This file is a list people can share. Nothing subscribes to it and nothing
verifies it automatically — announcing relays on a well-known topic is designed
but not built, and a hand-kept list is the honest version of it until then.

**No guarantees whatsoever.** These are experiments other people are running.
Any of them may vanish, drop your traffic, or be run by somebody curious about
who is using it. Read *What an operator can see* below before using one you do
not run yourself.

## The list

| address | operator | token | notes |
|---|---|---|---|
| _none listed yet_ | | | |

To add one, open a pull request. Please include the output of

    shrooms-relay -probe <host>:<port>

so the entry is at least "somewhat verified" at the moment it was added, and say
whether you intend to keep it running or are just experimenting.

## Using one

Two lines of config, both from whoever offered you the relay:

    relay_addr  = "203.0.113.10:31760"
    relay_blind = "true"

and, if the operator requires one:

    relay_token = "the token they gave you"

`relay_token` implies `relay_blind`, so a token-gated relay needs only the two
lines. Check it works before relying on it:

    shrooms-relay -probe 203.0.113.10:31760

**A blind relay has to be configured, not discovered.** It holds no network key
and runs no delivery node, so it cannot announce itself the way a relay that is
a member of your mesh does. That is not an oversight; it is the same property
that lets a stranger run one safely.

**The address may move.** A relay on ephemeral compute with an assigned port
gets a new one when it is redeployed, so an address here is a snapshot rather
than a promise.

## What an operator can see

They **cannot** see: message contents, your network key, who is on your mesh,
your device names, or anything from the control plane. The traffic is WireGuard,
encrypted between two devices whose keys the relay does not hold. That is
arithmetic, not a promise about good behaviour.

They **can** see: opaque per-relay tags, the addresses those tags connect from,
which pairs exchange packets, how much, and when. The tags are derived from your
mesh's own key, so they mean nothing on any other relay and two operators
comparing notes see unrelated numbers — but a single operator watching their own
relay sees a traffic pattern.

Compared to running your own relay, that is a real change: today the only
machines seeing that are yours.

## Running one

It is a 2.5 MB container with no state, no volume and no identity:

    docker run -d -p 51820:51820/udp \
      -e SHROOMS_RELAY_BYTES_PER_SECOND=12500000 \
      ghcr.io/vpavlin/shrooms-relay:latest

See [deploy/akash/](deploy/akash/) for running one on ephemeral compute, where
the whole thing costs a few tens of cents a month.

**Set a bandwidth ceiling.** Open with no ceiling is an unbounded claim on a
machine you pay for, and the relay says so on startup rather than letting you
find out from a bill.
