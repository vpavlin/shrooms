# Security and privacy status

What is protected, what leaks, and what is deliberately deferred. Kept honest
so nothing quietly ships as "done" when it is not.

Threat model (DESIGN §7): a personal mesh of 5–10 devices, one owner, all peers
mutually trusted. The adversary is a network observer, someone subscribed to
the public Waku shard, or an opportunistic scanner. **Not** a global passive
adversary — if that is your threat model, do not use this.

**Blind relays add a party the model did not have** (docs/blind-relays.md). A
relay run by somebody else is not a peer, holds no key of ours, and is trusted
with nothing — but it is deliberately placed in the traffic path, which is a
different thing from an observer who happens to be there. What it can and cannot
do is set out under *Relays run by other people* below. Using one is a choice,
and the default is still a mesh whose relays are its own members.

---

## Closed

| | Status |
|---|---|
| **Waku control messages** | Encrypted, XChaCha20-Poly1305 under a per-epoch key derived from the network key. Signature is *inside* the ciphertext, so the libp2p layer's `StrictNoSign` sender anonymity is not undone. |
| **Message size** | Padded to one of two fixed plaintext sizes, 512 or 1024 bytes. "Came online", "changed IP" and "nothing happened" are indistinguishable within a size. Credentials (ADR-018) do not fit beside endpoints in 512, so a node that carries one uses 1024 — which means length now distinguishes a credentialled mesh from one that is not, though not which device or what changed. |
| **Announce rate** | Fixed 45 s heartbeat, with two deliberate exceptions: a node answers a peer it has no tunnel to (bounded by a per-peer cooldown), and a router port mapping publishes immediately, since an address nobody has been told about is one nobody can dial (ADR-024). The fixed padding still hides *what* changed; the timing of those two says that something did. |
| **Rendezvous topic** | Rotating hourly, derived from the network key via HMAC. An observer without the key cannot find the mesh's traffic. Verified (spike S3) to stay on one shard, so rotation emits no gossipsub subscription churn. |
| **Store archival** | Announces are published `ephemeral: true`, so they are not persisted. Without this an observer who later learned the topic could query history retroactively. |
| **Disco probes** | Encrypted, not merely authenticated. Constant 168 bytes regardless of type or address family, so ping and pong are indistinguishable by length. (173 on the wire, with the 5-byte demultiplexing header.) Was 104 before ADR-029 added the per-device signature; this line said so for longer than it was true. |
| **Replay / rollback** | Monotonic per-device sequence numbers. A public bus lets anyone re-publish a captured message they cannot decrypt; without this they could roll a peer's endpoint back to a stale address. |
| **Impersonation** | The announce signature is bound to the `DevicePub` named inside it, so holding the network key is not sufficient to impersonate a device. |
| **On-disk material** | `config.toml` 0600, state dir 0700, `state.json` 0600. |

---

## Open — inherent to the substrate, cannot be fixed here

These are properties of Waku and WireGuard, not of this code.

**The content topic and timestamp are cleartext.** Waku Message v1 leaves both
visible. Our topic is an HMAC tag so it names nothing, but the string is on the
wire and its unlinkability rests entirely on rotation.

**The shard is visible** (`/waku/2/rs/2/3`). Shared with other applications,
which is the only crowd cover there is.

**Directly-connected relay peers see your IP.** Waku's own analysis concedes
only *weak* sender anonymity: a multi-node adversary can attribute a publisher.
Nothing we do changes this. Note the mesh's anonymity set on its own private
topic is its own devices, so publisher anonymity would be worth little anyway.

**WireGuard traffic is identifiable as WireGuard.** Handshake packets have
distinctive sizes (148/92/32). An ISP can tell you run a VPN, though not what
is inside. Obfuscation is a different project; do not conflate it with this.

---

## Relays run by other people

A blind relay forwards for meshes it is not a member of. It is the only
component here deliberately operated by somebody outside the trust boundary, so
what it can reach is worth stating precisely rather than reassuringly.

**It cannot read anything it carries.** The payload is WireGuard, encrypted
between two devices whose keys it does not hold. That is arithmetic rather than
a promise about the operator, and it is the only guarantee in this section that
does not depend on the code being right.

**It cannot read the control plane.** It never holds the network key, so
announces, topics and payload keys are all beyond it. Frames authenticate under
its own token, or under a public key when it is open — a separate key doing a
separate job (ADR: two keys, two questions).

**It cannot be pointed at a third party.** A registration installs nothing until
the registrant echoes a nonce sent to the address it claims, so an attacker
registering somebody else's address never receives it. Relaying is one packet in
and one packet out, so there is no amplification to be had; the challenge and
the MTU echo are both *smaller* than what prompts them.

**It cannot take a device's handle from it.** First claim wins and does not
move while the entry lives. Weaker than the roster check a member relay does —
an attacker who claims a handle before its owner ever does keeps it — and
stronger than nothing, since a stranger cannot compute the handle at all.

**It can see a traffic pattern**, and this is the real cost. Opaque per-relay
tags, the addresses they connect from, which pairs exchange packets, how much
and when. The tags are derived from the mesh key *and the relay's own address*,
so they are meaningless on any other relay and two operators comparing notes see
unrelated values — a property that was claimed here before it was true, and is
now enforced in `relay.Tag`.

**It can drop your traffic**, silently and selectively, and you would see it as
a slow network. Keeping several configured is the answer, and a device
registers with at most two.

**An open relay authenticates nothing at the frame layer**, deliberately: its
key is public by construction. Every real property above comes from the
registrant's signature, first-claim-wins and the routability check instead. One
consequence reaches the client: a device using an open relay will accept and
unwrap relay frames from anybody who knows its address, and hand the payload to
WireGuard, which rejects it. That is a few CPU cycles per forged packet and not
an injection — but before blind relays existed, only a mesh member could make a
node do that much.

---

## Open — deliberately deferred, with a plan

**The network key is a bearer credential.** Anyone holding it is a member,
permanently. There is no per-device revocation and no expiry, so removing a
device means rotating the key and re-enrolling everyone. Acceptable at 3–5
machines you control; not acceptable beyond that.
→ **Largely addressed.** A mesh with `admin_keys` set admits devices by
admin-signed credential with a 30-day expiry and gossiped revocation, and
`shrooms invite` enrols one device at a time with a token good for fifteen
minutes. Expiry is renewed by a sweep — `shrooms admin renew` signs a fresh
credential for every device near its expiry and puts them on the mesh, where
each one travels to the device it names and is verified there against the same
admin keys. That closes the obvious hole in expiry, which is that a guarantee
nobody can maintain gets turned off. What remains is that the network key still derives the topics, payload
key and PSKs, so a leak of it still exposes the control plane to reading and
still means rotating for everyone — it is no longer what *membership* is, but it
is still a shared secret. ADR-020 explains why the per-recipient rewrite that
would remove it is not built.

**The control socket's group is a real grant.** `socket_group` exists so a
desktop app — and `shrooms status` — need not run as root, and it is not a
read-only permission: that group may change this device's settings, switch a
mesh off, and leave one.

What it cannot do is admit anybody — including by invite, which an earlier
version of this note said it could. Both halves of the exchange (`/invite/hold`
and `/invite/reply`) are root-gated by SO_PEERCRED, and the `shrooms invite`
command reads the network key from a 0600 root-owned config, so a group member
who is not root cannot mint one. On a mesh with `admin_keys` it would not help
if they could: membership is a credential signed by a key the daemon has never
held.
→ Deliberate, and documented rather than minimised: the same shape as the
`docker` group, with a smaller blast radius. Set it to a group you would trust
with your mesh's metadata, which on a personal machine means your own login, and
leave it unset on a shared one ([ADR-025](docs/adr/025-control-from-a-desktop-app.md)).

**A service bound to the mesh address is reachable by every member, and by
nobody else.** Worth stating as the positive case, because it is the one
arrangement here where the network does the access control rather than the
application: only members can route to the mesh prefix, so `sshd` on that
address is off the LAN and off the internet entirely, with no firewall rule to
get wrong. `announce_bound` lists such ports for members
([ADR-026](docs/adr/026-announce-what-is-bound.md)); it discloses names, not
access, and is off by default because those ports are discovered rather than
declared.

**A published service is reachable by every member.** `services` forwards a
mesh connection to a loopback port, and a great many applications treat "bound
to 127.0.0.1" as their access control — no password, on the reasoning that only
a local user can connect. Publishing one makes every device holding the network
key a local user, which is a larger change than the config line looks. The
forwarder itself adds no authentication and is not a place to add it: what is
missing is authentication in the application.
→ Bounded by M5 credentials only in the sense that mesh membership itself
becomes revocable. Nothing here makes an unauthenticated application safe to
publish.

**~~Disco authenticates mesh membership, not device identity.~~** Closed
(ADR-029). Disco packets and relay registrations both carry an ed25519 signature
from the device that sent them, so a member can no longer send a probe claiming
another device's key, nor tell a relay that another device's WireGuard key is
reachable at its own address.
→ Resolved, and not by credentials: the announce already binds a device to its
WireGuard key, so both checks read the roster instead.

**No forward secrecy on the control plane.** Compromising the network key
decrypts every captured announce — past, present and future, not one epoch's
worth. Each epoch key is derived from the network key by HKDF, nothing is
deleted, and every node holds that key permanently, so anyone with it
recomputes them all. Per-epoch rotation buys unlinkability between epochs, not
forward secrecy; an earlier version of this note claimed a one-hour window,
which was wrong.

Two things bound what that costs. Only control-plane metadata is under this key
— announces, revocations, grants, service lists — and the traffic itself is
forward-secret already, because WireGuard does ephemeral ECDH per handshake and
rekeys every couple of minutes without touching the network key. And an
adversary holding the network key can decrypt everything current and join the
mesh regardless, so the marginal gain is historical metadata against somebody
who already has the present.
→ Deliberate (ADR-020). Revisit if credentials become the whole of membership
and the network key is demoted to rendezvous only, which removes the reason a
ratchet cannot be used: rejoining would stop depending on key derivation.

**Pairwise rendezvous is not implemented.** A single group secret means one
leaked key exposes the whole mesh's rendezvous. DESIGN §7 documents the
Briar-style alternative (per-pair topics from a DH secret, no group secret at
all) at the cost of N² topics.
→ Optional refinement, worth it if metadata privacy becomes a priority.

---

## Roadmap

The destination is [ADR-018](docs/adr/018-credentials-instead-of-a-shared-key.md):
no shared key at all, membership by admin-signed credential, and revocation that
costs one device rather than the whole mesh. Credentials and
[ADR-017](docs/adr/017-invite-tokens.md)'s invites are built; the shared key
remains, for the reasons ADR-020 sets out.


The bearer key is the weakest part of the system and it is not a design
position — it is a v1 shortcut with a planned replacement. Sequenced by
value-per-effort, not by tidiness.

### Phase 1 — one-time invite tokens ✅ built

**Problem:** the artifact you copy between machines is a permanent credential.
It lands in shell history, clipboard managers, and whatever you pasted it into.
Anyone who ever sees it is a member forever.

**Change:** `shrooms invite` emits a token good for a window — fifteen minutes
by default, up to two hours with `--ttl`. The joining device generates its own
keys, redeems the token, and receives the network key — and, since phase 2
landed alongside it, a credential signed for those keys — over the resulting
channel.

**What it does not fix**, stated more carefully than it was:

- **The token *is* the authorisation.** Whoever photographs it inside the window
  can join. That is minutes and one device against what used to be permanent and
  unlimited.
- **"Single redemption" applies to the credential, not to the key.** One
  non-deferred request wins the single held slot and gets a credential. On a
  mesh with **no** admin keys, where the network key *is* membership, the
  deferred answer hands that key to every requester inside the window, each
  sealed to its own ephemeral key — so one observed token can produce unlimited
  members there.
- **The window is the inviter's clock, not a cryptographic bound.** The token
  carries no expiry; it stops working when the inviting node stops holding the
  topic open. A node that is killed mid-invite is what actually closes it.
- **The response is not authenticated.** It is protected by the token and an
  ephemeral exchange, but the trust anchor — the mesh's admin keys — arrives
  *inside* that response. A token-holder who answers first can therefore plant
  the joiner into a mesh of their choosing. Consistent with "the token is the
  authorisation", and worth saying out loud.

Both halves of the exchange are padded to a constant, so the bus sees two
fixed-size ciphertexts on a shard it cannot distinguish from the mesh's own
traffic.

`shrooms join <NETWORK-KEY>` still exists for bootstrapping and recovery, and
carries the old exposure when used.

### Phase 2 — admin-signed credentials ✅ built

Built as `internal/cred` and the `admin` commands, and the wire format is binary
rather than CBOR — a credential rides an announce padded to a fixed size, and
the JSON form did not fit at the time it was measured.

Renewal is built too, as a sweep rather than a ceremony per device:
`shrooms admin renew` asks a running node who is on the mesh, signs a fresh
credential for everyone inside ten days of expiry, and hands them back for
delivery over a control message any member may relay. What is deliberately not
built is renewal with nobody present — that needs a signing key that is online,
which is a different posture from an admin key used a handful of times a year,
and the fixed authority set already allows for a separate renewal key if it is
ever wanted.

**Problem:** holding the network key makes you a member, so there is no
per-device revocation and no expiry.

**Change:** split authorization out of the network key.

- `K_rdv` keeps deriving the topic, payload key and per-pair PSKs — rendezvous
  genuinely needs a shared secret, since every member must compute the same
  topic with no coordination.
- Membership becomes an admin-signed credential over `{version, mesh_id,
  device_pk, wg_pk, serial, not_before, not_after, name}`, verified against
  `admin_pk`, which is a **public** value in config.

  Two differences from what this document described before it was built.
  `overlay_ip` is not signed and does not need to be: it is derived from the
  signed `device_pk`, so signing it would authenticate a value that cannot
  disagree. And there is **no `caps` field** — no capability model exists
  anywhere in the code, so nothing can express or enforce one. `mesh_id` and
  `serial` were added instead: the first stops a credential being replayed onto
  another mesh, the second is what revocation withdraws.
- 7–30 day expiry, auto-renewed while the admin is reachable.

**Why the expiry matters more than the signature:** a gossip bus lets an
attacker *suppress* a revocation message even though it cannot forge one.
Expiry is what bounds that; the gossiped revocation is only the fast path.
Implementing revocation without renewal and expiry builds the half that can be
defeated by dropping packets.

**Effort:** medium. **Depends on:** phase 1 for the enrolment channel.

### Phase 3 — revocation ✅ built

Revocations are signed, gossiped on the control plane, verified by each node
against the admin keys itself, and kept until the credential they withdraw would
have expired anyway.

**Change:** `shrooms revoke <name>` publishes a signed revocation with a
monotonic serial, republished on every epoch rotation and on join. Peers **tear
down live tunnels** on receipt.

**Why tearing down matters:** Nebula's documented gap is that its blocklist is
not distributed at all — you push it with Ansible — and its `disconnect_invalid`
is opt-in, so a revoked host keeps working tunnels for the remaining certificate
lifetime. We have a gossip bus, so we can do better; not doing so would be
choosing their weakness deliberately.

**Effort:** small once phase 2 exists. **Depends on:** phase 2.

### Phase 4 — per-device disco authentication — **done** (ADR-029)

**Problem:** disco probes authenticated mesh *membership*, not device
*identity*, so any member could send a probe claiming any sender key — and the
same gap let a member register another device's WireGuard key at a relay.

**Change, as built:** disco packets and relay register frames each carry the
sender's device key and an ed25519 signature over the body. The receiver checks
the signature, then checks the roster: the announce already bound that device to
that WireGuard key, so ownership is a lookup rather than a credential check.

**Not** keyed off phase-2 credentials, which is what this section used to say.
The relay would have had to hold an authority, the register frame would have had
to carry a credential, and a mesh with no admin keys would have had no check at
all. Reading the roster instead is smaller and protects every mesh. ADR-029's
amendment records why the credential was the wrong reach.

**Cost:** a disco wire-format change (104 → 168 bytes, version 2) and a longer
register frame. A flag day for a mesh mid-upgrade, spent while every mesh in
existence belonged to the people making the change.

### Deliberately not planned

- **Pairwise rendezvous** (per-pair topics from a DH secret, no group secret at
  all — DESIGN §7). Removes the last shared secret, at the cost of N² topics.
  Worth it only if metadata privacy becomes a priority over simplicity.
- **Forward secrecy on the control plane.** Not provided, and not by accident:
  epoch keys are derived from the long-lived network key, so rotation gives
  unlinkability rather than forward secrecy. Providing it means a ratchet, which
  replaces a derived key with held state — a device that lost it could not
  rejoin from the network key alone. The condition for revisiting is credentials
  becoming the whole of membership, at which point rejoining no longer depends
  on deriving anything.
- **Hardware-backed device keys.** The architecture already permits it — this is
  exactly why the device key is separate from the WireGuard key (ADR-007) — but
  nothing uses a TPM or Secure Enclave yet.

### Where this sits against the rest of the plan

The milestone order is M2 (traversal) → M3 (relay) → M4 (seamless) → M5
(security). That ordering assumes a mesh that does not reliably connect is not
worth securing.

**Phase 1 jumped the queue, as planned, and phases 2, 3 and 4 followed it.**
What is left of the plan is the part ADR-020 argues should wait: removing the
shared key itself, which is a control-plane encryption redesign rather than a
membership change.

## Before running this in anger

1. **Do not treat the network key as low-value.** It no longer decides who is a
   member on a mesh with `admin_keys`, but it still decrypts the control plane
   and derives every PSK. Use `shrooms invite` to move it — do not paste it into
   chat, tickets, or shell history you keep.
2. **The control socket is mode 0660.** Anyone in its group can read the roster
   and every peer's endpoints. Check the group on a multi-user host.
3. **Revoke a lost device** — `shrooms admin revoke --device <hex>` — which
   costs that device and nothing else. Rotating the network key
   (`shrooms key rotate`) is still the answer if the *key* leaked rather than a
   device, and it re-enrols everyone: the key derives the mesh prefix, so every
   overlay address changes. It is closer to creating a new mesh than to
   rotating a credential, which is precisely what credentials fixed.
4. **`shrooms paths` reports reflexive addresses.** More than one distinct
   value means endpoint-dependent NAT — useful diagnostically, but it also
   means peers learn several of your external addresses.
