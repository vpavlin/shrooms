# 052. Backups on our own Logos Storage nodes, encrypted to a Keycard

**Status:** superseded by ADR-077 (2026-11-01). Never built; re-checked
against current Logos Storage, `--block-ttl 0` expires blocks at once rather
than never, so this design as written would have lost every backup. The
inventory below still stands.

## Context

Nothing on the fleet is backed up. The few `~/backups` folders are copies
made by hand before a risky change. An inventory of every machine on
2026-10-13 found what a lost disk would take with it:

- **jimmy-crib** holds the release keystores of every Android app (Foray,
  KYM, Perun, Qaku, Kith, Freequencies) and the keystores of the F-Droid
  repositories. It also holds the only copy of the key that signs the
  Shrooms app (`~/fdroid.bak-dropped-…/keystore.p12`, alias `jimmy-crib`).
  Without that key, no phone can install an update over the app it has.
  It also holds the publisher's state and the bare git repositories
  (`foray.git`).
- **pi5** holds Forgejo (793 MB), the file drop (1.2 GB), `drop-private`,
  the Scala repository's signing key and the drop's TLS CA.
- **proteus** holds the Foray lists: 336 KB and 300 KB of JSON lines,
  which are small and cannot be replaced.
- **the VPS** holds, as root, nginx, Grafana and Prometheus, TeslaMate
  (1.7 GB), and the scala hub and collaboration server with their `.env`
  files.
- **Every machine** holds its mesh identity (`/etc/shrooms`, under 20 KB),
  its daemon state (`/var/lib/shrooms`), the agent's state
  (`~/.local/share/shrooms-agent`, 3–670 MB, mostly uploads), provider keys
  (`~/.pi/agent/.env`) and the units and drop-ins written by hand.

Without the large items (Forgejo, the drop, TeslaMate), the set worth
keeping is under 50 MB per machine. Source checkouts with a remote, build
output, models (`~/.local/share/whisper`, 397 MB) and browser profiles
(Foray's `.browser`, 2.8 GB) can all be fetched or rebuilt, so they are not
backed up.

The off-the-shelf answer is restic to a server on two machines: it has
encryption, deduplication, snapshots and pruning already built. We build
on Logos Storage instead. It is the stack this project exists to use, a
backup is a demanding first real job for it, and (below) it is the only
route to a backup that does not live on our own machines.

What Logos Storage gives us today, from its source (the jimmy-crib
checkout of 2026-09-17):

- content-addressed upload (`POST /api/storage/v1/data`, which returns a
  CID);
- fetching a CID from other nodes into the local store
  (`POST /data/{cid}/network`);
- a DHT that can be bootstrapped from chosen nodes (`--bootstrap-node`,
  `--no-bootstrap-node`; that checkout already has a private-DHT
  discovery test);
- a storage quota (default 20 GiB).

Blocks expire after `--block-ttl`, 30 days by default, unless that is set
to 0. The current code has no erasure coding and no marketplace, so the
only redundancy is the copies we place ourselves.

## Decision

**Three storage nodes on our own office mesh: the VPS, proteus and
scribe.** They have 88 GB, 943 GB and 905 GB free. pi5 and jimmy-crib are
left out because their disks are small or nearly full. Each node:

- listens for its peers only on its office mesh address;
- is bootstrapped only from the other two (`--no-bootstrap-node` plus
  their SPRs), with no public bootstrap nodes;
- runs with `--block-ttl 0`, so nothing expires behind our backs and
  retention is a deliberate delete;
- has an explicit quota (40 GiB on the VPS, 200 GiB on the others);
- keeps its REST API on localhost.

**A keeper beside each node** is the only part reachable over the mesh. It
does three things for fleet machines: it accepts an encrypted archive and
stores it, it fetches a CID from the other nodes when told to, and it
appends to its copy of the catalog. It has no delete call. A mesh member
can add to the backups but cannot remove any.

**Every machine, twice a day** (a systemd timer at about 03:00 and 15:00,
randomised):

1. Tars its own list of paths. The list is per machine, from the
   inventory, kept in `/etc/shrooms-backup/paths`.
2. Compresses it with zstd and encrypts it with `age` to the backup
   recipient.
3. Hands it to the nearest keeper, which returns a CID.
4. Tells the other two keepers to fetch that CID.
5. Waits until all three report it with `/exists`.
6. Records `{machine, time, cid, size, sha256}` in all three catalogs.

A run counts only when it is complete on all three. A partial run is
reported, not counted. The large items (Forgejo, the drop, TeslaMate's
database dump) run daily, as separate archives.

**The key comes from a Keycard.** A card does not hand out signing keys,
but it does derive secrets for other uses.

- **The derivation:** `ExportBIP85` (keycard-go v4) derives 32 bytes at a
  path reserved for this backup. Those bytes are the `age` X25519
  identity. If the card's firmware lacks BIP85, the fallback is an
  EIP-1581 export (`m/43'/60'/1581'/…`, the path the card allows out for
  encryption), passed through HKDF-SHA256 with the label
  `shrooms-backup age v1`.
- **When the card is needed:** once at setup, to derive the identity and
  write its public recipient to every machine, and again at every restore.
  Backups encrypt to the recipient and never need the card.
- **What restores the key:** the card's recovery phrase, or a second card
  made from the same seed. Losing both the card and the phrase makes every
  backup unreadable, by design.

**Retention:** every run for 14 days, then one a week for 12 weeks. The
keepers prune together; whatever falls out of the catalog's retention is
deleted from all three.

**Checking:** each day, a keeper fetches a random catalog entry from
another node and checks its sha256 and its `age` header. That much runs
without the card. Once a month, a person holding the card restores one
machine's latest archive into a scratch directory and compares it with the
live files. A backup nobody has restored does not count.

## Consequences

- Losing any two of the three nodes loses nothing. Each node holds every
  backup in full, so a restore works from whichever node is left, even
  with the mesh down: its own keeper reads its local store.
- The VPS is in a data centre, a different place from the rest. That is
  the only off-site copy until the next point. The VPS sees only
  ciphertext.
- The fleet becomes a real user of Logos Storage. Its rough edges (API
  changes between versions, DHT discovery on a private network, quota
  behaviour) become ours to report upstream.
- More to run: three nodes, three keepers, a timer on every machine, and
  a key ceremony when a card is added or replaced.

## Beyond our own mesh

The same design reaches past our machines without changing its shape.
Archives are encrypted before they leave the machine, so a holder we do
not trust is already accounted for. CIDs are content addresses, so they
stay valid wherever the blocks live. And the catalog is only a list of
CIDs.

When our nodes bootstrap from the public Logos Storage network, and
storage has a dispersal mechanism again, two things change and nothing
else. A dispersal mechanism means erasure coding across independent
providers, with storage requests that are paid for and proven, as Codex
had. The changes:

- the nodes' bootstrap configuration;
- step 4 above: instead of asking our own keepers to fetch, the job asks
  the network to keep the CID, coded into pieces spread across many
  providers.

The backup then survives the loss of every machine we own. That is the
point of building on Logos Storage rather than restic. Until then the
nodes stay private, because nothing would keep our blocks on anyone
else's disk.

## What would change our mind

- If Logos Storage cannot keep three private nodes in step reliably (CIDs
  that never become fetchable, discovery that needs the public network,
  stores that corrupt), back up with restic to proteus and scribe. Keep
  the Keycard-derived key and the catalog, and come back when storage is
  ready.
- If restore drills keep failing for reasons in the backup itself rather
  than the store, the design is wrong, not the store.
