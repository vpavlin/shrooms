# 071. Delivery moves to v0.40 one node at a time, behind a build tag

**Status:** accepted 2026-10-24 by the owner ("if we can test it without
breaking anything, I am fine with the work being done"); being built. Ends the
July pin of ADR-067's patch once every node has moved.

## Context

Every desktop, the VPS and the container image build liblogosdelivery at
logos-delivery 7a3a064b (2026-07-03). The phones use a prebuilt library of the
same revision. ADR-067's shard fix is carried as a patch against it. Upstream's
latest release was v0.39.2, 237 commits on; v0.40.0 was prepared on
2026-10-21 (branch `chore/prepare-release-v0.40.0`, 1a3b6c7d) but not yet
tagged. It is the first release with the shard fix (#4513).

What changed for us between the two, from the changelogs and tried on this
laptop on 2026-10-24:

- **The C ABI.** Calls go through nim-ffi 0.3's generated typed helpers,
  CBOR underneath, linked against TinyCBOR. The constructor answers in a
  callback. The one global event callback became one listener per event. The
  July binding cannot build against it, which is all the nightly
  `upstream-master` job reported from 2026-10-22.
- **QUIC on by default**, on UDP at the node's TCP port. The daemon gives
  discovery that same UDP port. Left on, the node never finished starting;
  off, it was connected in 3 s.
- **Fixed default ports**, 60000 and 9000, where the July library let the
  kernel pick. A second node beside a running daemon failed with "Address
  already in use".
- **Received payloads** are base64, not an array of byte values.
- **Metrics** gained a `logos_delivery_` prefix; libp2p's kept their names.
- **proto3 wire codecs**, and a new store-sync format, which we don't use.

Loam (jimmy-crib/vpavlin) builds its own Android library at v0.38.1, with a
DNS-deadlock fix and an arm64 Android target that upstream doesn't have. Its
desktop and hubs use delivery_module 0.3.0 (v0.39.0), pinned because later
modules break reliable-channel receive (logos-delivery-module#169).

## Decision

**Target v0.40.0, the release branch until it is tagged. Build both bindings
from one package, `internal/waku`, behind one API: the July one by default, and
v0.40's with `-tags ld040`. Nodes move one at a time; the default flips, and
the July binding and ADR-067's patch go, once every node, phones included, runs
v0.40.**

- **v0.40's binding keeps the July behaviour by default.** QUIC is off and the
  kernel picks the ports, unless the caller sets them. This is done in the
  binding, not by the callers, because the July library refuses options it
  doesn't know and the callers serve both bindings.
- **Payloads are read in every shape:** a byte array, base64 (standard or
  URL-safe), and Loam's `{"_bytes": …}`.
- **The daemon's metrics keep their names** (`shrooms_delivery_*`), mapped from
  either library's, so the dashboards don't change.
- **One Android library with Loam.** One upstream revision, Loam's two Android
  patches rebased onto it, and one `.so` for both apps' phones. Desktops build
  from source as now.
- **The nightly job builds master with the v0.40 binding**; the deps workflow
  takes the revision as an input, so both architectures build natively.

## How it was tested before anything shipped

On this laptop, against the v0.40 release branch built by
`docker/build-lib.Dockerfile`:

- every library-linked package with `-race -tags ld040`, and the management
  end-to-end suite with a v0.40 daemon;
- `scripts/e2e-two-nodes.sh` in a privileged container, 14 of 14 in all four
  pairings (July–July, v0.40–v0.40, July–v0.40, v0.40–July). The script takes
  `BIN` and `BIN_B` for that. The pairings cover admission, a tunnel, a restart
  onto remembered endpoints, and a revocation;
- the spike tool on the public logos.test fleet: connected, and a message came
  back in 8 ms;
- the release container build with `TAGS=ld040`: max glibc 2.34.

## Consequences

- Until the flip there are two bindings to keep in step. A change to the API
  goes into both, and CI builds the default only until a deps release of v0.40
  exists.
- A daemon can move to v0.40 without its peers. The first fleet move is one
  node, with its rollback tag kept, before the
  rest.
- The phones move only with the shared Android library, and Loam's own move
  waits on #169 for its desktop. Neither blocks the desktops.
- QUIC stays off. Turning it on needs its own UDP port, opened in every
  firewall, and is its own decision.
