# 015. Multiple meshes in one daemon

**Status:** accepted; design agreed, implementation staged

## Context

Alice has a mesh for her own machines. Bob has one for his. They also want a
shared mesh so Bob can reach Alice's NAS, without either gaining access to the
other's private mesh.

The cryptography already supports this cleanly. Everything is derived from the
network key — the /48 prefix, the rendezvous topics and their payload key, the
pairwise WireGuard PSKs, the disco key, the relay key — so two meshes are
independent by construction, with no shared mutable state to get wrong.

Running one daemon per mesh therefore almost works today. It was rejected: it
multiplies configs, state directories, TUN interfaces, ports, control sockets
and Logos Messaging nodes, and the last of those is the expensive one. The
requirement is that `shrooms join` can be run more than once and that be the
end of it.

## Decision

**One daemon, one TUN device, one UDP port, one messaging node, many meshes.**

### Per-mesh identity, derived from one master secret

```
device_key(m) = HKDF(master_secret, "mesh/v1/identity" || mesh_id)
wg_key(m)     = HKDF(master_secret, "mesh/v1/wg"       || mesh_id)
```

This is load-bearing, not hygiene. WireGuard identifies peers by public key
within a device and allows **one preshared key per peer**, while our PSK is
per-mesh. A device that shares two meshes with us therefore cannot be
represented twice on one WireGuard device — unless it presents a different
public key in each, which per-mesh derivation gives for free.

It also closes a linkability leak. `OverlayAddr` mixes the network key into the
prefix but **not** into the host bits, which are `SHA256("mesh/v1/addr" ||
device_pub)`. A device reusing one identity therefore carries the same 80-bit
suffix in every mesh it joins, so anyone in two meshes can correlate devices
across them. The same is true of `DevicePub` in the announce. Per-mesh
identities remove both, and mean joining a shared mesh reveals nothing about
membership of a private one.

`mesh_id` is derived from the network key, not chosen, so it needs no agreement:

```
mesh_id = SHA256("mesh/v1/meshid" || NK)[0:8]
```

### Mesh names are local labels

```
shrooms join <KEY> --mesh shared
```

Optional; defaults to a short rendering of `mesh_id`. Names are **local to the
node**, and deliberately not carried in the announce.

There is no authenticated channel to distribute a mesh's name. Putting it in the
announce would make it self-asserted, exactly like device names (ADR-008), so
any member could rename the mesh for everyone else. Alice calling it `shared`
while Bob calls it `alice-nas` is not a defect — it is the only thing that can
be true without an authority. `mesh_id` is the identifier; the name is a label
over it.

Names must be unique on the node, and `join` rejects a collision.

### Naming: `$peer.$mesh.$suffix`

`vps.mesh` is ambiguous the moment two meshes contain a `vps`. So the qualified
form is canonical:

```
vps.home.mesh
nas.shared.mesh
```

The short form `vps.mesh` is **also** emitted, but only when that peer name is
unambiguous across every joined mesh. A single-mesh node — which is every node
today — therefore sees no change, and ambiguity degrades by removing the short
name rather than by silently resolving it to one of the candidates.

This is the same rule already applied to duplicate device names within a mesh
(`hosts.Render` appends a piece of the address rather than letting the last
writer win), extended one level out.

### Configuration

Meshes are flat, prefixed keys rather than TOML tables:

```toml
mesh.home.key    = "P27KNQ2..."
mesh.home.relay  = "true"
mesh.shared.key  = "D4R5TBD..."
```

The config parser is a hand-rolled TOML subset with no table support. Prefixed
keys need no parser rewrite and give per-mesh options for free. The single-mesh
`network_key = "..."` form stays valid and means one mesh named `default`.

### State

`state.json` grows a per-mesh map holding the derived identity and, critically,
the announce sequence number — `Seq` must persist per mesh, since it is per
device *per mesh* that peers' replay guards track.

**Existing state is migrated by keeping its keys, not by re-deriving them.** A
node that regenerated its identity would change its overlay address and
WireGuard public key, breaking every established tunnel and appearing to its
peers as a new device while the old one lingered until it timed out. The
existing identity is written under the existing mesh's id verbatim.

## Consequences

**Joining a mesh does not bridge it to another.** Each node is an endpoint, not
a router: there is no IP forwarding, and `AllowedIPs` bounds what each peer may
send. Bob cannot reach Alice's private mesh through a machine that is in both
unless she deliberately enables forwarding. This is the property the whole
arrangement rests on and it is worth stating explicitly, because the obvious
fear — "I joined a shared mesh and exposed my home network" — is not what
happens.

**One NAT mapping serves every mesh.** Sharing the socket was already required
for traversal (ADR-002); the side effect is that hole punching and reflexive
discovery amortise across meshes rather than being repeated per mesh.

**Rendezvous cost is sub-linear.** All meshes' topics hash to the same shard
(spike S3), so N meshes add N×3 content-topic subscriptions but no additional
gossipsub mesh.

**Inbound control packets must be tried against each mesh's keys.** Announces,
disco probes and relay frames carry no cleartext mesh identifier — deliberately,
since one would be a membership oracle for a passive observer. Demultiplexing is
therefore trial decryption, O(meshes) per packet. Fine at the scale this is for;
if it ever isn't, the fix is a per-mesh listen port, which costs privacy.

**Sharing a mesh key still shares everything.** The network key is a bearer
credential (ADR-008): giving Bob the key to mesh C makes him a full member who
can enrol further devices, with no revocation until M5. "Let Bob reach this one
service" is not expressible and is squarely M5 work. Accepted deliberately —
this is the position today, and multi-mesh at least contains the blast radius to
one mesh rather than requiring Alice to share her private one.

## Staging

1. **Mesh naming and per-mesh hosts blocks.** Independently useful: the
   `/etc/hosts` marker is currently a fixed string, so two writers fight.
2. **Config and state** — prefixed keys, per-mesh identity, migration.
3. **Runtime** — per-mesh roster/prober/announce loop, trial decryption, one TUN
   carrying an address and route per mesh.
