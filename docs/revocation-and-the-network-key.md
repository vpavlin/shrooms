# Revocation, the network key, and the middle ground

Vaclav, 2026-08-22, on the audit finding against ADR-018: *"the adr is tough — is
there some middle ground?"*

Yes. This note sets out the gap, why the obvious fix is as expensive as it looks,
and the cheaper thing that gets most of the value.

## The gap

[ADR-018](adr/018-credentials-instead-of-a-shared-key.md) says:

> The revoked device keeps whatever it already had — nothing can reach into it —
> but it **stops receiving announces, so it cannot follow anyone who moves**, and
> no member will establish a new tunnel with it.

That is false as written. It presumes announces are wrapped per recipient, so
that dropping a device from the roster drops its ability to decrypt.
Per-recipient wrapping was never built — [ADR-020](adr/020-one-announce-many-readers.md)
records the decision not to, and ADR-018's own scope note says so — but the
revocation section was never reconciled with it.

Announces are sealed under the shared network key alone. Revocation removes the
device from *our* roster and refuses *its* announces: the inbound direction only.
Nothing on the outbound side consults the roster or the revocation list.

So a revoked device — a stolen laptop, a phone handed on — keeps deriving the
rendezvous topic and decrypting every announce on the mesh: every peer's name,
overlay address, external endpoints, relay use, bound ports and service list,
refreshed every 45 seconds, **for as long as the mesh exists**. It cannot join a
tunnel, because no member will peer with an expired or revoked credential. It
can watch indefinitely.

`SECURITY.md` is honest about the network key exposing the control plane. ADR-018
is not, and it is the document somebody reads to find out what revocation buys.

## Why the obvious fix is expensive

Rotate the network key on revocation. Everything downstream derives from it —
the rendezvous topic, the announce seal, the overlay prefix, each device's
address — so a rotation is not a key change, it is a new mesh wearing the old
one's name. Every peer re-keys at once, every offline device strands, and the
recovery path for a stranded device is a fresh invite, in person.

That is a real cost to impose on every revocation, most of which are "I sold the
old phone" rather than "somebody has my laptop".

## The middle ground

**Separate the two things the network key is doing.** It is both the *identity*
of the mesh (prefix, addresses, topic) and the *reading key* for the control
plane. Only the second needs to change when somebody is revoked.

Announces already take an epoch: `Seal(nk, epoch, …)`, `topic.Current(nk, now)`.
The epoch is a clock, so the key already rotates in time — but any holder of the
network key derives every epoch, which is why that rotation buys nothing against
a former member.

Give the derivation a second input: a **membership generation**, which only
current members can learn.

### What the generation actually is

Not a counter. That is the first thing to get right, and it is easy to get
wrong: if the generation were an integer, a revoked device holding `nk` would
compute `HKDF(nk, epoch‖1)`, `‖2`, `‖3` and stay exactly as deaf as it is now,
which is not at all. **The generation must be unguessable to a holder of the
network key**, so it has to be a secret, not an index.

So it is a pair:

| | | |
|---|---|---|
| `N` | a small integer | **public.** Says *which* generation a message is under. Travels in the clear. |
| `S_N` | 32 random bytes | **secret.** Does all the work. Delivered wrapped to each current member. |

    announceKey = HKDF(nk ‖ S_N, epoch, "mesh/v1/announce")

`N` is a label so a reader knows which key to try, and so a node can tell "I am
behind" from "this is corrupt". `S_N` is what a revoked device does not have.

**Where `S_N` comes from.** The admin generates it at random when revoking. It
is not derived from anything — deriving it from `nk` or from the roster would
defeat the point, since a revoked device knows both. It does not need to be
derived from the admin key either, which means the admin stores nothing between
rotations: if the current value is ever lost, mint `S_{N+1}` and wrap that.

**How it reaches members.** Sealed to each current member's device key, one
small message per member, published on the rendezvous topic. That is the
per-recipient wrapping [ADR-020](adr/020-one-announce-many-readers.md) declined
— see below for why the objection does not apply here.

**What does *not* change.** The rendezvous topic stays `topic.Current(nk, now)`,
derived from the network key alone. It has to: a device that missed a rotation
must still be able to find the place where rekeys are published, and a device
being revoked is meant to lose the ability to *read*, not the ability to see
that a mesh exists. Splitting it this way also matches what `SECURITY.md`
already says — rendezvous genuinely needs a shared secret, because every member
must compute the same topic with no coordination.

The revoked device therefore still finds the topic, still sees sealed traffic on
it, and can no longer open any of it.

### It is a re-invite, to a degree — and that is the useful way to see it

Vaclav's observation, and it is the right one: a rekey is a secret sealed to each
device, which is structurally what an invite is. Worth being precise about where
the analogy holds, because both halves matter.

**Where it holds.** One small sealed message per device, to a key that device
alone can open. Same shape, and it should reuse the same sealing primitive
rather than growing a second one.

**Where it breaks — which is the whole argument for doing it.** An invite needs
a person running `shrooms invite`, a token carried to the other machine, and
both ends live inside fifteen minutes. It hands over a *new* identity: new
address, new DNS name, new credential. A rekey needs none of that. Nothing
renumbers, no credential is reissued, no human is present, and a device that was
asleep picks it up when it wakes. "Re-invite every device" is a weekend; this is
a keystroke and a fan-out.

### Who does the wrapping

The analogy pays off here. **Every current member already holds `S_N`** — so any
member can wrap it for any other member, and the admin does not have to.

That matters because the admin key is meant to be offline, and increasingly on a
card ([ADR-022](adr/022-keycard-for-the-admin-key.md)). A card can sign a short
statement. Asking it to perform key agreement against a dozen recipients is a
different proposition, and it would tie every rotation to having the card in
hand.

So split it:

- **The admin authorises**, with one signature over
  `{mesh_id, N, H(S_N), not_before}` — a statement, not a secret. This is what
  a card is for, and it is the same shape as a revocation.
- **Members distribute.** Any member seals `S_N` to any other member's device
  key. The recipient checks `H(S_N)` against the admin-signed commitment before
  accepting it, so a rogue member cannot inject a generation of its own
  choosing — the worst it can do is stay silent.
- **Members refuse non-members.** A revoked device asking a peer for `S_N` is
  refused by the same membership check that already rejects its announces
  (`checkMembership`).

**The honest cost of that split.** Confidentiality of the new generation now
rests on every member refusing correctly, rather than on the admin alone. One
buggy or malicious member re-admits the revoked device.

That is a real weakening and should be written down — but it is a small one,
because a malicious member can hand over the network key itself today, and
nothing in this design has ever defended against a member who wants to. It is
the difference between "a member can leak the mesh" and "a member can leak the
mesh", which is to say no difference at all.

### Catching up

A device that missed generations `N+1…N+3` and is handed `S_{N+4}` cannot read
the backlog, because the values are independent. That is fine for announces,
which are refreshed every 45 seconds and carry no history worth recovering — it
only needs the current one.

If reading the backlog ever matters, a reverse hash chain gives it: pick a seed,
let `S_i = H^(K−i)(seed)`, and release in reverse. Holding `S_{N+4}` then yields
every earlier value and no later one. The cost is that the admin must keep the
seed between rotations, and `K` bounds how many rotations a mesh can ever have.
Not worth it for announces; noted because it is the obvious next question.

- The mesh identity — prefix, addresses, DNS names — stays derived from `nk`
  alone, so **nothing renumbers** and a generation bump is invisible to users.
- On revocation the admin bumps the generation and publishes the new value
  **wrapped once per current member**, to each device's key. That is the
  per-recipient wrapping ADR-020 declined — but ADR-020 declined it *per
  announce*, tens of times a minute, forever. Per *rekey* it runs once per
  revocation, on a roster of a dozen devices. The cost that made it a bad idea
  is not present here.
- The revoked device holds `nk` and cannot derive the new generation, so it goes
  deaf at the next epoch. Which is what ADR-018 already claims.

**Stragglers.** A device that was offline for the rekey cannot read the new
generation, and cannot ask for it on a topic it can no longer derive. Two
mitigations, both needed:

1. **Honour the previous generation for a grace window** — a week, say. The
   revoked device keeps reading for that week, which is a deliberate trade: it
   is the difference between "revocation is slow" and "revocation strands your
   own devices". For the stolen-laptop case, see the override below.
2. **Rekey messages are republished** on the old generation's topic for the
   window, so a device that wakes on day three catches up without a human.

**`--rotate-now`**, for when the grace window is the wrong answer. Skips it,
accepts that anything offline needs a fresh invite, and says so before doing it.
That is the stolen-laptop path, and it should be a deliberate keystroke.

**A `--no-admin` mesh cannot do any of this.** There is nobody to sign a rekey,
and the network key *is* membership there ([ADR-008](adr/008-network-key.md)).
Revocation on such a mesh means re-inviting everyone onto a new mesh, and the
honest thing is to say so in `admin revoke` rather than implying otherwise.

## The concrete protocol

Enough of the pieces exist that this can be written down properly rather than
sketched.

### Which key does a member seal to

This is the one real decision, and it wants deciding before anything is built.
Three candidates, all already present:

**`wg_pub`** — already X25519, already in every credential, already known to
everyone. And already WireGuard's static key, used in Noise_IK with the same
private half. Sharing one static across two protocols is exactly the setting
cross-protocol attacks live in, and a domain separator in our KDF does not
constrain what the *other* protocol does with it. **No.**

**`device_pub`, mapped ed25519 → X25519.** Well-trodden — age and Signal both
live here — but it makes one key both sign announces and perform key agreement,
which is the pairing every guide tells you to avoid. Defensible with care.
Doable if we want zero new fields.

**A dedicated sealing key.** `identity.go` already derives every per-mesh key by
HKDF label (`m.expand("mesh/v1/identity", networkID, …)`), so this costs one new
label and *no new stored state* — it falls out of the seed each device already
has. The public half has to reach the sender somehow, which is where the cost
turned out to be; see below.

**Decided: the dedicated key** — `Identity.SealPriv`/`SealPub`, derived from the
same master under `mesh/v1/seal`. Nothing stored, nothing reused.

### How the public half reaches a sender: in the credential

Two ways to publish it were considered and both were wrong.

*In every announce* costs more than the credential does. An announce is padded to
512 or 1024 bytes, `Seal` refuses anything larger, and the sender trims
endpoints from the end until it fits. Measured through the real `Seal`:

| announce | today | key in the credential | key as its own field |
|---|---|---|---|
| no credential, no boot | 20 | 20 | 17 |
| credential, no boot | 9 | 7 | 6 |
| **credential and boot** (a Core relay) | **4** | **3** | **2** |

An earlier draft of this note said a separate field cut a Core relay to *one*
endpoint, losing its LAN address so that peers on the same wifi would stop
finding each other. That was a measuring error, and worth recording: the field
was simulated by padding `Name`, which sits in the inner JSON, and the envelope
base64-encodes that a second time — so 76 bytes read as about 101. It cuts a
relay to two endpoints, which keeps the public address and the LAN address.

The credential is still the better place: one endpoint against two, nothing at
all on a mesh with no credentials, and admin-bound rather than self-asserted.
But it wins on being admin-bound, not on averting a failure that was never
there. `TestEndpointBudget` pins the "today" column.

*Only while the device is behind*, triggered by the existing `Deaf` signal, does
not work at all. `Deaf` (`internal/mesh/mesh.go:494`) skips any peer without a
live WireGuard handshake — it detects "deaf to peers I already have tunnels
with". A device that missed a rotation has an empty roster (memory-only,
`ForgetAfter` 6h), therefore no configured WireGuard peers, therefore no
handshakes, therefore `Deaf` returns nothing. It would never advertise the key,
and the peers wanting to rekey it would have nothing to seal to. Worse, that
state trips `Silent` instead, which restarts the daemon hourly and takes every
other mesh in the process down with it.

**So it goes in the credential.** It is already admin-signed, already names
`DevicePub` and `WGPub`, and already rides in every announce. Adding 32 bytes to
a 181-byte binary credential costs about half what a separate JSON field does,
and it buys something a self-asserted announce field cannot: the sealing key
becomes **admin-bound**. Nobody can substitute their own.

A joining device supplies it in round two of the invite exchange, where it
already derives its per-mesh identity before asking for a credential (ADR-017).

### No generation number on the wire

`N` does not need to be public after all. At most two generations are live at
once — current and the one inside the grace window — so a reader tries `S_N`,
then `S_{N-1}`. Two AEAD attempts, and the second only on failure.

That is better than a cleartext `N`, which would have told an observer how many
times a mesh has revoked somebody. `N` instead travels *inside* the sealed
announce, where members can read it and nobody else can.

### Delivery: a standing envelope, not an event

Once per epoch, every member publishes `S_N` sealed to each roster member's
`seal_pub`. A dozen small messages an hour, against the eighty announces an hour
each device already sends.

This project has learned this exact lesson once already and written it down.
`republishRevocations` (`internal/mesh/mesh.go:1000`) exists because "a
revocation used to be relayed exactly once, by whoever first heard it…
Re-publishing on each epoch rotation makes the withdrawal a standing statement
rather than an event." The same reasoning applies here and for the same reason.

A device that wakes up waits at most one epoch and finds its own envelope
waiting. Nothing has to notice it, it does not have to ask, and there is no
request for anyone to replay.

**What this replaces, and why the alternatives were worse.** An earlier draft had
stragglers send a signed *catch-up request*. Signing stops forgery but not
replay: a revoked device holds `nk`, so it can read and copy one genuine request
verbatim and re-publish it, and every member then verifies a signature that
passes, performs a key agreement, and publishes a reply. One captured message
becomes M published ones, repeatable — on a shared public Waku shard, so the
mesh would DoS the fleet and ADR-028's rate limiting would take our own
publishers off the bus. The request also carried a credential and a timestamp,
sealed under `nk`, handing the revoked device a wake/sleep log for a named
device. A standing envelope has none of these properties because nobody has to
send anything to receive it.

It also fixes the multi-generation case. A device at `N-2` does not need
`S_{N-2}` to recover; it needs its envelope, which is for `S_N`. So "at most two
generations live" stops being load-bearing, and revoking twice in one week
— which `--rotate-now` makes easy — stops silently stranding anybody.

`--rotate-now` becomes simply: stop publishing the previous generation's
envelope.

### Ordering: a rotation is bound to the revocation it enforces

The admin's statement names the revocation serial it acts on, and a member must
hold that revocation before it may serve `S_N`. Without that rule the straggler
path guarantees the bad case: a device that was offline during the revoke comes
back, is rekeyed, and its own revocation list is stale until the next hourly
republish — during which it would answer the revoked device with the new secret,
having checked a list that does not yet contain it.

`N` must never repeat. An earlier draft tie-broke a collision on the lower
`H(S_N)`, which does not converge: a node hearing only the higher-hash statement
uses it, and nodes hearing both use the other — a permanent silent split, which
ADR-020 says is the failure this project fears most. Binding `N` to the
revocation serial makes reuse impossible instead of arbitrated.

### Monotonicity

Each node **persists** the highest `N` **whose secret it holds** and refuses
anything lower.

Both words are load-bearing. *Persists*, because an anchor held only in memory
is reset by every restart, and the revoked device — which legitimately holds
`S_{N-1}` through the grace window, and can replay the admin's public statement
for `N-1` — would win the race to pin a rebooting node back to a generation it
can read. *Whose secret it holds*, because anchoring on statements heard instead
would let anyone holding `nk` replay a statement for a generation whose secret
never arrives, leaving the node refusing `S_{N-1}` for ever with no fallback:
permanent silent deafness, inducible by an outsider.

(A revocation carries `Issued` and `NotAfter`, not `NotBefore` — an earlier
draft of this note said otherwise, which is the same mistake `cred.go:453`
already exists to correct.)

## Which messages move, and which do not

Four kinds of message ride the rendezvous topic, and an earlier draft of this
note moved only the first — which would have left the stated goal unmet. All
four are sealed under `nk` today (`internal/mesh/mesh.go:1789-1808`).

**Announces → `S_N`.** Names, addresses, endpoints, relay use. The original
target.

**Grants → `S_N`.** This is the worst of the four and the draft missed it. A
grant carries a whole credential — `DevicePub`, `WGPub`, `Name`, `Serial`,
`NotBefore`, `NotAfter` — and `admin renew` sweeps every device approaching
expiry and publishes one grant each, monthly, for ever. A revoked device reading
those reconstructs the complete current roster: every identity key, every tunnel
key, every name. From `DevicePub` and `nk` it then recomputes every overlay
address and every pair PSK. Rotating announces while leaving grants readable
would have bought almost nothing.

Safe to move because of the standing envelope: a device that is behind gets
`S_N` within an epoch, and the renewal window is days.

**Services → `S_N`.** Service names and bound ports — "the bound ports and
service list" this note set out to close. The *display* side is already gated:
`Services()` shows only devices on the roster, and revocation forgets them, so a
revoked device cannot inject names into anybody's view. But it can still read
everyone else's, which is the half that matters here.

**Revocations stay under `nk`. Deliberately.** A revocation has to reach every
node whatever generation it is on, including one that has not caught up yet —
and it is the message whose late arrival does the most damage. Rotating it would
mean the news of a revocation could not reach the nodes most likely to be out of
date.

The cost is real and small: a revoked device can read revocations, so it learns
which devices have been withdrawn, including its own. Learning it has been
revoked is arguably correct behaviour rather than a leak.

## Migration: generation zero is what we do today

The draft had no deployment story, and the obvious one is a trap: a reader that
tries `S_N`, then `S_{N-1}`, then bare `nk` restores exactly the property being
removed. This codebase refuses flag days — `PaddedSizes` exists so senders can
move after readers, and the revocation v1/v2 split does the same — and mobile
updates through F-Droid on its own schedule, so "upgrade everything at once" is
not available.

It does not need one. **Define generation zero as the empty secret**, so
`HKDF(nk ‖ "", epoch)` is exactly today's derivation. A node that has never
rotated is at generation 0 and nothing about its wire format changes. An updated
node reads 0 and reads `S_N`.

Deployment is then: update everything, then rotate. The first rotation is what
makes an un-updated node deaf — correctly, because that is the same thing that
makes a revoked device deaf, and the two cannot be distinguished by design. So
`admin revoke --rotate` should say so plainly before it runs, and the grace
window is what makes finding out survivable.

### Migrating the devices that already exist

New devices are straightforward: the invite carries the sealing key and the
credential they get back is version 2.

Existing devices are the awkward case, because the credential is where the key
lives — so an admin cannot put a device's key into its credential without
already knowing the key, and a device already on the mesh has no channel to
publish it. `admin renew` therefore keeps issuing version 1 for them, which is
correct rather than broken: a v1 credential works exactly as it always did.

**The tempting fix was rejected.** Announces could carry `seal_pub` as a
self-asserted field, purely so an admin could learn it, with the credential
staying authoritative for actual sealing. After the compact framing that costs
about one endpoint out of fourteen, so it is affordable. It was still declined:
it puts a second, unsigned copy of a signed value on the wire, and every reader
then has to know which copy is the real one. Vaclav's call, and the right one —
we are the only users, so a more troublesome migration is cheaper than a wire
format with a redundant field in it for ever.

**So the migration is manual, once.** On each device:

    shrooms keys

which now prints the sealing key and the exact `admin issue` line to run,
`--seal` included. Run that where the admin key is, then `shrooms credential set`
on the device. `shrooms keys` also says so unprompted when it finds a version 1
credential, because that is where somebody looks when a device is not behaving.

## What this does not fix

Worth stating, because the note is otherwise a list of things that work.

- **A revoked device keeps `nk`**, so it keeps deriving the topic, seeing that
  the mesh exists, counting members from the size of each epoch's envelope
  fan-out, and timing rotations by watching the bursts. Rotation removes reading,
  not observation.
- **A member who wants to leak can.** Confidentiality of `S_N` rests on every
  member refusing non-members, and a malicious member can hand over `nk` today
  regardless.
- **A `--no-admin` mesh cannot do any of this** — nobody signs the statement, and
  the network key *is* membership there (ADR-008). Revocation on such a mesh
  still means re-inviting everyone onto a new one, and `admin revoke` should say
  so rather than implying otherwise.

## What is cheap and should happen regardless

**Correct ADR-018 now.** The claim is false today and will stay false until the
above is built. It costs nothing to say what revocation actually buys — no new
tunnels, no data-plane access — and what it does not: the former member keeps
reading the control plane.

## Recommendation

Correct the ADR immediately. Build the generation counter when there is appetite:
it is a contained change — one extra input to a derivation, one new signed
message type, one grace window — and it converts a documented promise that is
currently untrue into one that is true.

Not urgent for the mesh as it stands, where every device belongs to one person.
It becomes urgent the first time a mesh has a member who might leave badly.
