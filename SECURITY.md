# Security and privacy status

What is protected, what leaks, and what is deliberately deferred. Kept honest
so nothing quietly ships as "done" when it is not.

Threat model (DESIGN §7): a personal mesh of 5–10 devices, one owner, all peers
mutually trusted. The adversary is a network observer, someone subscribed to
the public Waku shard, or an opportunistic scanner. **Not** a global passive
adversary — if that is your threat model, do not use this.

---

## Closed

| | Status |
|---|---|
| **Waku control messages** | Encrypted, XChaCha20-Poly1305 under a per-epoch key derived from the network key. Signature is *inside* the ciphertext, so the libp2p layer's `StrictNoSign` sender anonymity is not undone. |
| **Message size** | Padded to one of two fixed plaintext sizes, 512 or 1024 bytes. "Came online", "changed IP" and "nothing happened" are indistinguishable within a size. Credentials (ADR-018) do not fit beside endpoints in 512, so a node that carries one uses 1024 — which means length now distinguishes a credentialled mesh from one that is not, though not which device or what changed. |
| **Announce rate** | Fixed 45 s heartbeat. Endpoint changes ride the next scheduled announce rather than triggering an out-of-band publish. |
| **Rendezvous topic** | Rotating hourly, derived from the network key via HMAC. An observer without the key cannot find the mesh's traffic. Verified (spike S3) to stay on one shard, so rotation emits no gossipsub subscription churn. |
| **Store archival** | Announces are published `ephemeral: true`, so they are not persisted. Without this an observer who later learned the topic could query history retroactively. |
| **Disco probes** | Encrypted, not merely authenticated. Constant 105 bytes regardless of type or address family, so ping and pong are indistinguishable by length. |
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
mesh off, leave one, and mint an invite, which hands the network key to whoever
redeems it. What it cannot do is admit anybody. On a mesh with `admin_keys`,
membership is a credential signed by a key the daemon has never held, so an
invite minted this way produces a device every peer refuses.
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

**Disco authenticates mesh membership, not device identity.** Any mesh member
can send a probe claiming any sender key. The consequence is bounded: disco only
selects a *candidate path*, and WireGuard's own handshake is what actually
authenticates the peer and encrypts traffic. A hostile mesh member could steer
path selection, not read traffic.
→ Resolved by M5 credentials.

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

**Change:** `shrooms invite` emits a token valid 15 minutes, single
redemption. The joining device generates its own keys, redeems the token, and
receives the network key — and, since phase 2 landed alongside it, a credential
signed for those keys — over the resulting channel.

**What it does not fix:** the token *is* the authorisation, so whoever
photographs it inside the window can join. That is minutes and one device
against what used to be permanent and unlimited. Both halves of the exchange are
padded to a constant, so the bus sees two fixed-size ciphertexts on a shard it
cannot distinguish from the mesh's own traffic.

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
- Membership becomes ~100 bytes of admin-signed CBOR over `{device_pk, wg_pk,
  name, overlay_ip, not_before, not_after, caps}`, verified against `admin_pk`,
  which is a **public** value in config.
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

### Phase 4 — per-device disco authentication

**Problem:** disco probes authenticate mesh *membership*, not device *identity*,
so any member can send a probe claiming any sender key.

**Bounded today:** disco only selects a candidate path. WireGuard's own
handshake is what authenticates the peer and encrypts traffic, so a hostile
member could steer path selection, not read traffic.

**Change:** key disco off per-device credentials from phase 2.

**Effort:** small. **Depends on:** phase 2.

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

**Phase 1 jumped the queue, as planned, and phases 2 and 3 followed it.** What
is left of the plan is the part ADR-020 argues should wait: removing the shared
key itself, which is a control-plane encryption redesign rather than a
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
