# 010. Ship a container image, not a binary

**Status:** accepted

## Context

A single static binary is the nicest deployment story: `scp` it and run. That is
what the project aimed for.

## Decision

Ship a container image.

## Why

**`liblogosdelivery` requires glibc 2.38.** Our own Go binary only needs 2.34,
but the library does not, and that decides it:

| target | glibc | works? |
|---|---|---|
| Debian 12 | 2.36 | ✗ |
| Ubuntu 24.04 | 2.39 | ✓ |
| Ubuntu 25.10 | 2.42 | ✓ |

A tarball therefore fails on the most common VPS image. Static linking is not
available either: building `liblogosdelivery` from source is blocked upstream
(the Nim build fails with `illegal effect: NestedPoll` at both master and the
revision the Android bindings pin), so we cannot control how it is linked.

The image also carries the 21 shared libraries Basecamp ships alongside it —
which matters, because the library **dlopens `libpq` at runtime** for its Store
backend, and that failure is fatal and only visible at startup.

## Consequences

- Docker is a dependency on every node.
- Deployment ships ~250 MB rather than 11 MB. `docker save | gzip | ssh` makes
  that a one-off.
- **Host networking is required, not incidental.** Behind docker's bridge NAT the
  reflexive address peers observe would be the docker gateway's and the source
  port would be rewritten, so hole punching would fight a layer of NAT that does
  not exist in reality.

## What would change our mind

Upstream fixing the Nim build, which would let us build the library ourselves
and consider static linking — or a `liblogosdelivery` built against an older
glibc.
