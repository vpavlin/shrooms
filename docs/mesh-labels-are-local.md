# A mesh label is local, and that has consequences

**Status:** the behaviour is built and described here; one decision at the end
is Vaclav's.

Every device chooses its own name for a mesh. Nothing on the wire carries it,
because there is no authenticated channel to distribute one — putting a name in
the announce would make it self-asserted like device names (ADR-008), so any
member could rename the mesh for everybody. `mesh_id` is the identifier; the
label is a note you wrote on it.

That is right, and it is also how one mesh came to be called `home` on a phone,
`test` on a laptop, and nothing at all on the node in between — which took an
evening to untangle on 2026-08-25.

## What the label is, and is not

**It is not** the mesh's identity. Two devices agreeing on a label proves
nothing; two devices disagreeing proves nothing either.

**It is** how you address a specific mesh on *this* device: `--mesh <label>`,
`vps.<label>.internal`, `admin-<label>.json`.

So to answer *are these two machines on the same mesh?*, compare something the
mesh actually derives from:

| compare | where to see it |
|---|---|
| the mesh's `admin_keys` | `shrooms admin show`, and `admin_keys` in the config |
| the overlay prefix | `shrooms status`, the `fd…::/48` per mesh |
| the network id | the name of `seqmarks-<id>.json` in the state directory |

Two of those match iff it is one mesh. The label tells you nothing.

## The qualified name uses the resolver's label

`vps.home.internal` asks *this* device's resolver for the mesh *this* device
calls `home`. On a device that calls the same mesh `test`, that name is not a
qualified name at all — it is a device called `vps` followed by a label that
matches nothing.

This is easy to get wrong and invisible when it happens to work. It worked here
for months against a resolver bug: a device name used to match with any trailing
labels, so `k11.home.mesh` returned `k11` while reading nothing from `home`.
`k11.banana.mesh` would have done the same. Fixing that made a name somebody had
used for months stop working, correctly.

## The short form picks, and may pick wrong

`vps.internal` with no label resolves through the first mesh in config order
that has a `vps`. It used to be refused when more than one mesh answered, and
that was changed deliberately ([`fc12b75`](../adr/015-multiple-meshes-one-daemon.md)):
ambiguity is the normal case for your own devices, so refusing removed exactly
the short names most worth having, and looked like DNS being broken for one
host.

**The reasoning held; the justification was wrong.** It said picking either was
fine because both addresses reach the same machine. They reach the same machine
and **not the same service**: an sshd bound to one mesh's address is not
listening on the other's, so the short name can resolve somewhere the thing you
want is not running.

**Use the qualified form wherever the answer has to be a particular mesh.** Put
it in ssh configs and bookmarks; leave the short form for typing.

## Renaming

`shrooms mesh rename <old> <new>`, and it is a command rather than two lines of
`sed` because the label was deciding more than it looked:

- **the admin key file** is `admin-<label>.json`, so a renamed mesh would stop
  finding its own authority;
- **the interface and port** come from the mesh's position in a label-sorted
  list, so renaming re-sorts it and moves both — for every mesh at or after the
  new position.

Rename pins every mesh's current interface and port before it moves any label,
so nothing the network can see changes. Afterwards the config says which
interface and port each mesh uses, rather than deriving it from a nickname.

Restart the daemon afterwards. Other devices keep their own names; there is
nothing to coordinate.

## The decision that is open

**Should the qualified form be mandatory?**

Vaclav asked for that originally and was talked out of it, on the argument that
the qualified form was available for anyone who needed it. The counter-argument
now is that a short name resolving to the wrong mesh's address is silent, and
the case where it matters — a service bound to one mesh — is exactly the case
somebody would not think to check.

The shape would be a config option, off by default so single-mesh nodes are
unaffected, that refuses a short name answered by more than one mesh instead of
picking one. That is the old rule, scoped to people who have asked for it: it
was removed because ambiguity is normal, and if you have turned this on, being
told about it is the point rather than the bug.
