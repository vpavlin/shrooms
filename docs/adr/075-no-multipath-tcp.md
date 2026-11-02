# 075. Shrooms' listeners don't offer multipath TCP

**Status:** accepted 2026-11-01 by the owner ("ok, go for it"); built.

## Context

Since Go 1.24, a Go program's TCP listeners accept multipath TCP (MPTCP)
unless told otherwise. CVE-2026-98432, published 2026-10-15 and rated 9.8 by
the kernel's CVE team, is a race in the Linux IPv4 TCP handshake (a
use-after-free on a connection request carrying IP options). Anthropic's
published root exploit went through an MPTCP listener. Ubuntu had no fixed
kernel as of 2026-11-01.

Shrooms' own listeners were all MPTCP and all on mesh IPv6 addresses (the
agent's 7387, metrics on 9180, services), which the IPv4-only bug cannot
reach; the delivery node's public libp2p port (30304 on the VPS) is the
library's, in plain TCP. Nothing in Shrooms uses MPTCP.

## Decision

`godebug multipathtcp=0` in `go.mod` and `mobile/go.mod`: every binary built
from this repository listens and dials with plain TCP. A future IPv4 listener
does not offer the path the exploit used.

The kernel is still the fix. The VPS moved to 7.2.13 the same day; hosts on
kernels without the fix can drop inbound IPv4 packets carrying IP options
(the review host does, as a stopgap).

## Consequences

- No MPTCP for anything Shrooms serves. Nobody used it; a connection that
  would have spread across two networks now uses one.
- The Go team calls turning MPTCP off per program a fragile mitigation: the
  underlying bug is not MPTCP's, and a host's other MPTCP listeners are
  unaffected by this.

## What would change our mind

A use for MPTCP (a phone keeping an agent connection across Wi-Fi and mobile
data, say), on kernels long past the fix.
