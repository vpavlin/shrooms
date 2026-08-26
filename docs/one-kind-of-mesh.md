# One kind of mesh

**Status:** proposed, not done. Vaclav's question, 2026-08-26: *"why do we even
have primary and secondary meshes? Do we need the distinction? I'd just name all
the meshes — we could even alias one as default, but we should treat them all
the same."*

Short answer: no, we do not need it, and it has been costing us. This is what
it would take.

## What the distinction actually is

A config written before ADR-015 describes one mesh in top-level fields —
`network_key`, `relay`, `listen_port`, `admin_keys`. Multi-mesh added
`[mesh.<label>]` tables beside them. Rather than migrate, the top-level form
stayed and became "the primary mesh", labelled `default`.

So a node has one mesh described in one shape and every other mesh described in
another, and every piece of code that touches meshes has to know which it is
holding. There are **87 sites** referencing `DefaultLabel`, `legacy` or
`original` outside tests.

## What it has cost

Not hypothetical — each of these was a real bug:

| symptom | cause |
|---|---|
| `shrooms paths` reports `we announce` for the primary mesh while listing peers from all of them | `out.Announced` is set from `primary`, and there is one field for a per-mesh fact |
| a credential written to the top level but not to the matching mesh entry, so a restart lost it | two homes for one value; fixed by `SetOwnCredential` writing both |
| `MeshState` identifying the original mesh by pointer comparison against `s.Identity` | replaced by an explicit `original` flag |
| `mesh remove` needing a special refusal for the top-level mesh | it is the device's whole config, not an entry |
| `ifaceAndPort` special-casing "the top-level form is always position zero" | interface assignment depends on which shape a mesh is in |
| `admin.json` vs `admin-<label>.json` | two naming schemes for one kind of file |

The `paths` one cost an hour on 2026-08-26 diagnosing why a phone would not
connect on a second mesh: `we announce 192.168.0.151:51821` was read as that
mesh's endpoint when it was the primary's, and the mesh in question was on
`51824`.

The pattern is always the same. A per-mesh fact gets stored or reported in a
place that predates there being more than one mesh, and the answer looks
right — it *is* right, for one mesh — while being silently wrong for the rest.

## The one that settles it

Found 2026-08-26, after the list above was written, while diagnosing why a
phone and a laptop **on the same LAN** could not connect on a new mesh:

    meshCfg := cfg
    meshCfg.NetworkKey = m.NetworkKey
    meshCfg.AdminKeys = m.AdminKeys
    meshCfg.Relay = m.Relay
    ...
    // meshCfg.ListenPort is never set

Both callers — the daemon and the phone — worked out the nth mesh's port, bound
WireGuard to it, and handed the mesh package a config still carrying the
**device's** port. `candidates()` builds every local address from
`m.cfg.ListenPort`, so a mesh listening on 51824 announced `192.168.0.151:51820`.

A peer on the same LAN dials that, reaches the **first mesh's** WireGuard
socket, and its handshake is rejected without comment because the keys belong to
another mesh. Both devices retry forever. It presents as *"two devices one hop
apart cannot find each other without a relay"*, and it was reported that way
twice — once here and once by somebody else who had never seen this config.

Only the first mesh escaped, **because its port is the device's port**. Every
mesh after it was broken from the day multi-mesh shipped, on both front-ends,
and it stayed invisible because the meshes that were tested all had relays —
which route around the wrong address entirely.

That is the argument in one field. Not that the distinction is untidy: that a
config with one mesh and a config with four are read by the same code, and the
code is correct for the first and quietly wrong for the rest. Nobody wrote a bug
here. Somebody wrote `cfg` and it meant two different things.

Fixed by `Config.ForMesh`, which both callers now use, and which cannot omit the
port because there is nowhere left to omit it from. That is a patch on the
symptom. The cause is that `Config` describes a device and a mesh at once.

## What "treat them all the same" means

Every mesh is a `[mesh.<label>]` table. There is no top-level `network_key`,
no top-level `relay`, no top-level `listen_port`. `default` is a label like any
other, and only the default *name* when none is given.

Status, announces, admin files, relays and interfaces are all per mesh, with no
"which one is this" branch anywhere.

## The one asymmetry that has to survive

**Device identity.** The original mesh keeps the identity the device already
had; every later mesh derives its own:

    st.MeshState(networkID, cfg.NetworkKey != "" && mc.Label == DefaultLabel)

That is not cosmetic. Changing the identity of an existing mesh changes this
node's overlay address and invalidates its credential — every peer would see a
stranger. So one mesh on an upgraded device genuinely is different from the
others, and pretending otherwise would break every node that has ever run.

The fix is to make it **explicit and per-mesh** rather than implied by shape:

    [mesh.default]
    inherits_device_identity = true

One boolean, on the mesh it applies to, that new meshes never set. It replaces
an implicit rule — "the mesh in the old-shaped fields is the one with the old
identity" — with a stated fact. `MeshState.original` already is this flag; it
just is not written down anywhere a person can see.

## Migration

The risk is entirely here, and it is why this is a proposal rather than a
commit.

1. **Read both shapes.** Loading a config with top-level fields synthesises
   `[mesh.default]` with `inherits_device_identity = true`. This is roughly what
   `Meshes()` already does.
2. **Write only the new shape**, once, on the first config write after upgrade,
   with the old file kept as `config.toml.pre-flatten`.
3. **Delete the branches** — the 87 sites — after a release that only reads.

Steps 1 and 2 are mechanical. Step 3 is the payoff and cannot be rushed: a node
that half-migrates has two meshes claiming the same identity, which is worse
than the thing being fixed.

**Not decided:** whether `relay_blind` and `relay_token` become per-mesh in the
same pass. They are top-level today and inherited by every mesh, which is
convenient and is also the reason a blind relay can be configured for a mesh
that has no idea it is using one. Probably yes, with the top-level kept as a
default that each mesh may override — but that is a second decision and it does
not have to ride along with this one.
