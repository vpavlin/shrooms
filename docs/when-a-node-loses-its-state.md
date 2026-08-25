# When a node loses its state

**Status:** written the evening it happened, 2026-08-25, from a node that
restarted 1385 times against an empty file.

A power cut left `minipc-k11` with `state.json` present and zero bytes. The
daemon refused to start, systemd restarted it every few seconds, and the mesh
saw a peer that had simply stopped announcing. This is what was in that
directory, what survives, and what to do.

## What is in the state directory

`/var/lib/shrooms`, root-owned, 0600 throughout.

| file | what it is | replaceable? |
|---|---|---|
| `state.json` | **the device identity**, its tunnel key, sequence number and credential | **No.** A new one is a different device |
| `state.json.bak` | the last copy that parsed | it is the safety net |
| `seqmarks-<net>.json` | the highest sequence accepted from each peer | yes — costs a replay window until it refills |
| `revocations-<net>.json` | withdrawals this node has verified | **losing it un-revokes devices** until they are heard again |
| `roster-<net>.json` | peers and their endpoints, for a fast restart | yes, entirely |
| `boot-peers.json` | rendezvous addresses learned from peers | yes |

**The admin key is not here.** It lives in `~/.config/shrooms/admin*.json` for
whoever minted the mesh, which may be a different machine or a different user —
`sudo` looks in root's home, not yours.

## What a power cut used to do

`Save` wrote a temp file and renamed it. A rename is atomic with respect to other
processes and says nothing about durability: on ext4 it can reach the journal
while the data blocks have not, so the new name appears with no contents. All
three writers in the package had this, including the revocation list.

Fixed by syncing the file before the rename and the directory after, in one
writer that everything now uses. And `state.json.bak` is written from the bytes
that last parsed, so an unreadable `state.json` recovers instead of ending a
device.

**Neither helps a node that has not been updated.** If `ls` shows no
`state.json.bak`, this node is still running the old behaviour.

## If `state.json` is empty or unreadable

**Do not delete it yet.** Starting without it mints a *new* identity, which has
no credential, cannot be given one without an admin, and joins at a different
address — the node comes up looking perfectly healthy and is invisible to the
mesh. That is the one irreversible move, and it is the obvious one.

1. `sudo systemctl stop shrooms` — the restart loop writes nothing, but stop it.
2. Look for `state.json.bak`. If it is there, the daemon will recover on its own.
3. Look for `state-*.json.tmp`. These are removed on a successful write, so one
   surviving means a write that did not finish — and it may be complete enough
   to copy over `state.json`.
4. Snapshots, if the filesystem has them. `findmnt -no FSTYPE /var/lib`, and on
   btrfs `sudo btrfs subvolume list /`.

If none of that turns anything up, the identity is gone and the device has to be
enrolled again.

## Enrolling it again

The config survives — it is in `/etc/shrooms/config.toml`, not the state
directory — so the node still knows the mesh. It does **not** need to re-join,
and `join` will refuse: the mesh is already configured. What it needs is a
credential for its new keys.

```
sudo rm /var/lib/shrooms/state.json     # deliberate now, not accidental
sudo systemctl start shrooms            # mints a fresh identity
sudo shrooms keys                       # prints the keys and the exact issue line
```

Then wherever that mesh's admin key lives — which may be another machine, and is
identified by matching `admin_keys` rather than by the mesh's name, see
[mesh-labels-are-local.md](mesh-labels-are-local.md):

```
sudo shrooms admin issue --mesh <label> --name <name> \
    --device <hex> --wg <hex> --seal <hex>
```

Omit `--serial`: it defaults to the current unix time, which is above whatever
the old credential used, and the replay guard only requires it to increase.
Passing `--seal` issues a version 2 credential, so the device can be rekeyed
after a revocation.

Back on the node:

```
sudo shrooms credential set <blob>
sudo systemctl restart shrooms
```

**The restart is required** — `credential set` writes the file and does not
touch the running daemon, which is holding its own copy of the state.

## Things that looked like faults and were not

**"In the mesh, but no credential."** Correct and expected: the node has the
network key from its config so it sees peers announcing, while peers refuse its
announces and never build a tunnel. `no handshake` on every peer with everything
else healthy is this, and the symptom presents on the peers rather than here.

**`shrooms keys` shows a credential and `status` says there is none.** Those read
different fields, and until 2026-08-25 `credential set` wrote one the daemon
never read: the top-level `credential`, while the daemon reads the per-mesh
entry that `MeshState` returns once it exists. Fixed; on an un-updated node the
credential has to be copied into the mesh entry by hand.

**A mesh with `admin_keys` and no admin key file anywhere.** Usually the label:
the file is named after whatever the *minting* machine called that mesh, which
may not be what this one calls it. `sudo shrooms admin show` lists every
authority a machine holds, and matching is by key, never by name.

## The one that has no recovery

Minting a mesh from inside a container without a volume for the admin directory
writes the authority to the ephemeral layer, and it is gone the next time the
container is recreated. The mesh keeps working — members hold the network key
and their credentials — and can never admit another device, because the admin
key set is fixed at mint. Members drop off as their credentials expire.

`admin init` refuses this now. The node compose file mounts
`/etc/shrooms/admin` for anyone who means to do it deliberately.
