# 020. Membership is a seam

**Status:** accepted — shapes how [ADR-018](018-credentials-instead-of-a-shared-key.md) is built

## Context

Building credentials made something obvious: **this is the secure group
messaging problem**. Who is a member, how a member is admitted, how one is
removed, and what key the group encrypts under — that is the whole of ADR-018,
and it is also the whole of MLS (RFC 9420).

Logos already has an implementation. `libchat` is MLS-based
(`core/conversations/src/inbox_v2/mls_provider.rs`) and runs over
logos-delivery, the same transport this project uses for rendezvous. So the
question is not academic: should the mesh's membership simply *be* an MLS group?

What MLS would give us is real, and better than what ADR-018 describes:

| ours | MLS |
|---|---|
| admin-signed credential | credential plus a trust model |
| revocation by serial | Remove proposal, new epoch |
| per-recipient announce wrapping | TreeKEM — cheaper, and does not leak membership size |
| `PairPSK` from a shared secret | exporter secret, which maps cleanly |
| — | **post-compromise security**, which we do not have at all |

That last line is the prize. [SECURITY.md](../../SECURITY.md) concedes there is
no forward secrecy on the control plane: compromising the network key decrypts
every captured announce, not one epoch's worth — the epoch key is derived from
the network key and nothing is deleted, so rotation gives unlinkability and not
forward secrecy. (Both documents used to say "for the epoch", which read as a
one-hour bound that does not exist.) MLS would make a removed device unable to
read *future* traffic even holding every key it ever had.

**Why a plain hash ratchet is not the cheap version of this.** `k(n+1) = H(k(n))`
with `k(n)` destroyed would give forward secrecy without any of MLS. It also
turns a derived key into held state, and that is the property this whole design
rests on: a device rejoins from the network key alone, because addresses,
topics and keys are all derived rather than allocated. With a ratchet, a device
that lost its position — a reinstall, a restored backup, a re-flashed phone —
could no longer catch up from the key it holds; enrolment would have to carry a
ratchet position; and two nodes at different positions would fail in the way
this project has learned to fear most, where everything looks healthy and
nothing arrives.

So the revisit condition is not "when we have time". It is when credentials
become the whole of membership and the network key is demoted to a
rendezvous-only secret, because then rejoining is a credential operation and no
longer depends on deriving a payload key from something everyone keeps forever.

## Why not now

Three reasons, in increasing order of how hard they are to fix.

**Groups are ephemeral.** Checked rather than assumed, because this is the fact
the decision rests on: libchat's MLS provider is
`core/conversations/src/inbox_v2/mls_provider.rs`, the type is named
`MlsEphemeralPqProvider`, and its `StorageProvider` is
`openmls_memory_storage::MemoryStorage` — group state lives in memory and is
gone on restart. Upstream named it `Ephemeral`, so this is a stated position
rather than an oversight.

Mesh membership is the opposite. It must survive a VPS reboot, a laptop shut for
a month, and a phone that is off overnight. "This device is a member" is a
durable fact about hardware, not a conversation that can be re-established by
the people in it. Until group state persists, membership cannot live there.

Worth noting what this is *not*: libchat persists other things. There is a
`core/sqlite` crate, and double-ratchet sessions persist in
`core/double-ratchets/src/storage/session.rs`. `StorageProvider` is an openmls
trait with a sqlite-shaped hole already waiting, so this reads as a gap on a
path rather than a refusal.

**MLS needs ordering.** Epochs advance through an agreed sequence of commits,
and Waku is eventually consistent and unordered. Chat systems put a Delivery
Service in front to order commits — usually a server, which is the thing this
project exists not to need. Two devices committing concurrently fork the group,
and in a personal mesh (a phone in Doze, a VPS always on) concurrent commits are
routine rather than exceptional.

**The liveness models differ.** MLS state is walked through every epoch; a
device that misses a month either replays every commit or is re-added. The
announce is deliberately stateless — miss one and the next arrives in 45
seconds. [ADR-003](003-waku-as-rendezvous-not-control-plane.md) chose that
because Doze forces it, and it is why the mesh survives a rendezvous outage at
all.

And in no version of this does MLS replace WireGuard. It would agree membership
and keys; tunnels are untouched. The scope of any future swap is ADR-008 and
most of ADR-018 — the control plane, and nothing else.

## Decision

**Membership is an interface, not a design.** The daemon asks three questions —
who is a member, what key protects the control plane, and what PSK belongs to
this pair of devices — and does not care how they are answered. Credentials are
one implementation. An MLS group could be another.

**And the per-recipient announce wrapping in ADR-018 is not built.** It is the
largest remaining piece of that design, it scales worst (about 48 bytes per
member, and the fixed padding starts to leak membership size), and it is
precisely what TreeKEM does properly. The shared payload key stays: it is the
weakest part of the system and it is also cheap, replaceable, and honestly
described. Building an expensive version of something that may be replaced is
worse than keeping a cheap version that certainly will be.

## Consequences

- `internal/cred` stays free of wire format and I/O, so the seam is cheap to
  hold. It is nearly free today and expensive once threaded through the daemon,
  which is the whole reason to decide this now.
- ADR-018 gets smaller: credentials, expiry and revocation, without the
  encryption redesign. That is the part that earns its keep regardless of what
  happens to libchat.
- If libchat gains persistence and a workable ordering story, the swap is an
  implementation of one interface rather than a rewrite, and post-compromise
  security arrives with it.
- If it does not, nothing is lost. Credentials stand on their own, and the mesh
  never depended on a group-chat library to know who its members are.
- The trigger for re-reading this is specific: when `MlsEphemeralPqProvider`
  stops being ephemeral — when that `StorageProvider` points at something
  durable. Not a version number, and not enthusiasm.
