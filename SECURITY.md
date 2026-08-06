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
| **Message size** | Padded to a fixed 512-byte plaintext → constant 552 bytes on the wire, confirmed in a live run. "Came online", "changed IP" and "nothing happened" are indistinguishable. |
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
→ **M5** replaces this with admin-signed per-device credentials, short expiry,
gossiped revocation, and one-time-use invite tokens so the copy/pasted artifact
stops being a permanent secret. `admin_pk` and the announce `credential` field
already exist (empty) so the migration is not a wire-format break.

**Disco authenticates mesh membership, not device identity.** Any mesh member
can send a probe claiming any sender key. The consequence is bounded: disco only
selects a *candidate path*, and WireGuard's own handshake is what actually
authenticates the peer and encrypts traffic. A hostile mesh member could steer
path selection, not read traffic.
→ Resolved by M5 credentials.

**No forward secrecy on the control plane.** Compromising the network key
decrypts any captured announce for the epoch. Per-epoch derivation limits the
window to one hour, but the key itself is long-lived.
→ Follows the network key's lifecycle; revisit with M5.

**Pairwise rendezvous is not implemented.** A single group secret means one
leaked key exposes the whole mesh's rendezvous. DESIGN §7 documents the
Briar-style alternative (per-pair topics from a DH secret, no group secret at
all) at the cost of N² topics.
→ Optional refinement, worth it if metadata privacy becomes a priority.

---

## Before running this in anger

1. **Do not treat the network key as low-value.** Until M5 it is the whole
   security of the mesh. Do not paste it into chat, tickets, or shell history
   you keep.
2. **The control socket is mode 0660.** Anyone in its group can read the roster
   and every peer's endpoints. Check the group on a multi-user host.
3. **Rotate the network key if a device is lost**, and re-enrol the rest. That
   is the only revocation available until M5.
4. **`logos-vpn paths` reports reflexive addresses.** More than one distinct
   value means endpoint-dependent NAT — useful diagnostically, but it also
   means peers learn several of your external addresses.
