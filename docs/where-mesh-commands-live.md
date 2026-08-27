# Where mesh commands live

**Status:** proposed, not done. Vaclav's question, 2026-08-27: *"we have shrooms
init, but then shrooms mesh remove — should init be shrooms mesh init? And if we
want to keep shrooms init, should it just prepare stuff?"*

Both halves are right. The second one has an answer that already exists.

## What is actually tangled

Two different things, and every command does some mixture of them:

| | sets up the DEVICE | acts on a MESH |
|---|---|---|
| `init` | yes | mints one |
| `init --mesh X` | no | mints one |
| `join KEY` | yes | joins one |
| `join --invite --mesh X` | no | joins one |
| `prepare` | yes | — |
| `mesh remove` / `list` / `rename` / `enable` / `disable` | no | yes |

Device setup is a name, a port, a preset, a mode and this machine's keys. It
happens once. Mesh membership happens as many times as there are meshes.

On a fresh machine both happen together, which is why one command doing both is
a good first run and is what `scripts/install.sh` documents. The trouble starts
at the second mesh: `init --mesh kc` is "add a mesh" wearing the word
*initialise*, and its opposite is `mesh remove`. Nothing about the pair says
they undo each other.

**This is the primary-mesh problem in another costume** — see
[one-kind-of-mesh.md](one-kind-of-mesh.md). The first mesh is special because it
is the one that arrived with device setup, and that specialness leaks into the
command names the same way it leaked into the config format.

## "Should init just prepare stuff?"

That command exists: **`prepare`**. It writes the config with the network key
left blank — name, port, relay, identity — and nothing else. It is device setup
with no mesh.

It is documented as a niche flow (bringing up a machine without the key passing
through anybody), which is why it does not read as the primitive it is.

## The shape this suggests

Two primitives and two conveniences:

    shrooms prepare                 the device: name, port, preset, mode, keys
    shrooms mesh new                mint a mesh on a prepared device
    shrooms mesh join --invite T    join one on a prepared device
    shrooms mesh remove             the opposite of `mesh new`, where it belongs
    shrooms mesh list|rename|enable|disable

    shrooms init                    prepare + mesh new, for a fresh machine
    shrooms join KEY                prepare + mesh join, likewise

Every mesh verb lives under `mesh` and each has its opposite beside it.
`init` and `join` stay exactly as they are for a first run, because "one command
on a new machine" is worth keeping and is what every install instruction says.

What goes away is `init --mesh X` and `join --mesh X`: the forms that do a mesh
operation under a device-setup name. They become `mesh new` and `mesh join`.

## What it costs

Almost entirely documentation. `install.sh`, the README, the website, the
guides, the completion and the e2e scripts all name these commands, and the
muscle memory is one person's.

The code cost is small: `addMeshWith` and the `--mesh` half of `cmdJoinInvite`
already exist and already do the work. `mesh new` and `mesh join` would be
thin wrappers, and `init --mesh` can keep working as an alias for as long as
anybody wants.

**Not decided:** whether `prepare` keeps its name. It says what it does for
the flow it was written for and undersells what it is. `setup` or `device init`
would read better as a primitive, and renaming it is the one part of this that
touches somebody's existing scripts.
