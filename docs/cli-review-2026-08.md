# Shrooms CLI — independent UX review

**Reviewer:** Claude (model: Claude Fable 5), acting as an outside reviewer with no prior involvement in the project.
**Date:** 2026-08-27
**Method:** Part 1 (the standard) was written before reading any shrooms source, help text, or documentation. Part 2 reviews the tool against that standard, using the built binary's help output, the source of the command dispatch, the completion script, and the published docs. Part 3, written last, compares my findings with the project's own internal restructuring proposal, which I did not read until Parts 1 and 2 were complete. No state-changing command was run.

---

## Part 1 — What good CLI UX is

This section was written before looking at shrooms, so that the standard is not shaped by the tool being judged. It draws on the conventions of git, docker, kubectl, systemctl, gh, aws, ip/iproute2, ssh, and apt, and on published guidance (clig.dev — the Command Line Interface Guidelines; GNU and POSIX option conventions; Heroku's CLI style guide; Julia Evans' writing on why tools confuse people).

### 1.1 The ranked list

Not everything matters equally. Ranked by how much each one costs a real user when it is missing:

**1. Errors that say what to do next.** The single highest-leverage property. A user in an error state is a user actively asking the tool for help; most tools answer with a stack trace or an errno. The gold standard is git's "did you mean", or `gh`'s "To get started with GitHub CLI, please run: gh auth login". Every error should carry three things: what failed, why (in the user's vocabulary, not the implementation's), and the literal next command to type. A tool can have a baroque command structure and still be loved if its errors teach; a tool with a beautiful structure and mute errors gets abandoned. Related: exit codes must be honest (0 only on success) so scripts can trust the tool.

**2. Safety around destructive and irreversible operations.** Cost of failure is unbounded — lost data, a broken network, a revoked credential. The conventions that work: (a) destructive verbs are distinct and unambiguous (`rm`, `remove`, `revoke`, `delete` — never overloaded onto something that also creates); (b) interactive confirmation by default with an explicit `--force`/`--yes`/`-f` escape hatch for scripts; (c) high-stakes confirmations require typing the name of the thing (as GitHub does for repo deletion) rather than just `y`; (d) `--dry-run` exists for anything that fans out (apt, rsync, kubectl all do this); (e) the destructive path never shares a prefix with a common safe path, so tab-completion and muscle memory cannot betray you. systemctl's split of `stop` (transient) from `disable` (persistent) from `mask` (emphatic) is a model of graded severity with graded names.

**3. Consistency of vocabulary, including symmetry between opposites.** A CLI is a language; users build a mental grammar from the first three commands they learn and then extrapolate. Every extrapolation that fails is a small betrayal that forces them back to the docs. Concretely: one name per concept everywhere (if the credential is a "cert" in one command it must not be a "credential" in another and a "key" in a third); opposite operations get symmetric names of matching shape (`add`/`remove`, `start`/`stop`, `enable`/`disable`, `login`/`logout` — never `add` paired with `revoke`, or `up` paired with `disconnect`); the same flag means the same thing under every subcommand (`-o json` everywhere or nowhere); the same argument shape in the same position (if one command takes `NAME` positionally, its sibling should not take `--name`). kubectl is the strong example: `get`/`describe`/`delete`/`apply` compose with every resource noun, so learning one verb teaches dozens of commands. Asymmetry is worse than mere inconsistency because the user's confident guess is *wrong*, not just absent.

**4. Discoverability: the tool teaches itself.** A first-time user should be able to go from `tool` (no args) to productive without opening a browser. That requires: (a) bare invocation prints a short usage overview, not an error, and not a wall; (b) `--help`/`-h` works at every level, and `tool help <cmd>` works too; (c) help text leads with one-line descriptions and *examples* — users copy examples far more than they parse synopses (Heroku's guide is emphatic on this); (d) the overview groups commands by task and puts the two or three commands a new user actually needs first, or names them explicitly ("To get started, run..."); (e) shell completion ships and is installable in one line; (f) misspellings get suggestions. The measure of discoverability: what fraction of the tool can be learned from the tool itself?

**5. Structure: verbs, nouns, and when groups earn their place.** Small tools should be flat verbs (`ssh`, `apt install`). Group commands under a noun only when there are several nouns that each take several verbs — that is when `docker container start` / `kubectl get pods` structure pays for itself. A group with one member is pure tax. Two structural sins: (a) mixed levels — some operations at the top, their siblings buried in a group, so the user cannot predict depth (`git` is the perennial offender: `git branch` manages branches but `git checkout` also creates them); (b) the same verb meaning different things at different levels. Also: the top level should read as a story. If someone reads only the command list, the lifecycle of the tool (set up → join → use → inspect → leave) should be visible in it.

**6. Flags versus positionals, and defaults that respect what the machine already knows.** Positionals for the one or two mandatory objects of the verb (`git clone URL`, `kubectl delete pod NAME`); flags for everything optional, orthogonal, or hard to remember the order of. Never require a flag for the thing the command is *about* — `tool remove --node foo` is worse than `tool remove foo`. And never require the user to supply a value the machine could compute: if there is one config file in the standard location, do not demand `--config`; if the interface can be detected, do not demand `--iface`. Required flags are a design smell (clig.dev): each one is a failed default. The inverse sin also exists: magic defaults for *dangerous* parameters. Default what is safe and recoverable; ask for what is not.

**7. Output that respects both audiences.** Human-first prose by default, `--json` (or `-o json`) for scripts — and once any command has it, all inspection commands should. Success is quiet or one line (Unix rule of silence); progress and diagnostics go to stderr, data to stdout, so pipes work. No emoji-as-status-encoding that a script must parse. Tables for lists, detail views for single objects (the `get` vs `describe` split).

**8. Teachability as a whole: the first five minutes.** This is the integral of everything above, but it deserves its own line because it is testable: put the binary in front of someone who has never seen it, with a one-sentence description of what it is for, and watch. They will type the tool's name bare, then `help`, then guess a verb from the domain ("connect"? "status"? "join"?). Every guess that works builds trust. The README and the `--help` should agree with each other and with the completion script; a tool whose docs, help, and completion describe three slightly different tools has told the user it does not know itself.

### 1.2 Cross-cutting conventions worth holding a tool to

- **GNU/POSIX basics:** `--long` and `-s` forms; `--version`; `--` ends options; flags before or after positionals both work (git got this right, older tools did not).
- **Do not prompt when not a TTY**; fail with a clear message instead, so CI does not hang.
- **Verbs from the user's domain, not the implementation's.** A VPN user thinks "join the network", "who's online", "leave" — not "provision credential state".
- **State-changing commands say what they did** ("Added node X; 4 nodes total"), ideally with the follow-up command ("run `tool status` to watch it connect").
- **Names should be typeable.** Frequent commands short, rare commands can be long. Reserve short forms for common operations.
- **One obvious way.** Two spellings of the same operation doubles docs and halves the community's shared vocabulary.
- **Respect the ecosystem's expectations:** on a systemd Linux box, daemon lifecycle should defer to or integrate with systemctl vocabulary rather than inventing a parallel one.

### 1.3 What matters *most*, compressed

If a tool gets only three things right, they should be: (1) errors that tell you the next command, (2) destructive operations that cannot be triggered by a confident guess, and (3) a vocabulary consistent enough that a confident guess is usually right. Structure, output formats, and completion are refinements; those three are the trust relationship.

---

## Part 2 — Shrooms against that standard

Evidence base: `./bin/shrooms` (build `deps-v1-536-gd8bee6f-dirty`) run bare, with `help`, and with `-h` at every level; the dispatch in `cmd/shrooms/main.go` and the group files (`mesh.go`, `configcmd.go`, `admin.go`, `keycard.go`, `services.go`, `setup.go`, `status.go`, `args.go`, `keys.go`); `packaging/shrooms.bash`; `README.md`; the published pages under `site/`. Read-only commands (`status`, `paths`, `bound`) were run against the live daemon. Nothing state-changing was run.

Findings first, ranked by what they cost a real user (frequency × severity). What is genuinely good comes after, and it is a substantial list — this is a much better CLI than most first meetings suggest, and the biggest problems are about *meeting* it, not using it.

### 2.1 Findings, ranked

#### F1 — The front page hides a third of the tool (severity: high, hit by everyone)

`shrooms` with no arguments, `shrooms help`, and `shrooms -h` all print the same usage — and it does not mention the `mesh`, `services`, or `keycard` groups at all, nor `config set`/`config settings`/`config flatten` (only `config validate` appears). Yet these are not fringe features: `mesh` is the only way to see and re-enable a switched-off mesh (the file comment in `mesh.go` says a mesh once vanished into exactly this hole), `services add` is the flagship "publish Immich by name" feature the README leads with, and the bash completion happily completes all of them (`packaging/shrooms.bash` lists `mesh config services keycard` in `$commands`).

So the tool's own front page describes a smaller tool than the one that ships. A first-time user who wants to publish a service, or who switched a mesh off and wants it back, cannot get there from `--help`; they must already know, or find it in the README, or notice it in tab completion — the one surface that is complete. The help, the completion, and the docs currently describe three slightly different tools, which is the classic sign (per the standard, §1.1.8) that the tool has lost track of its own shape.

Compounding it: the usage that *is* printed has an odd internal order (`version` sits between `join` and `prepare`; the enrolment trio `keys`/`credential set`/`admin *` is indented differently from its neighbours, which reads as accidental rather than as grouping) and it contradicts itself — see F3.

**Suggestion.** One usage text, complete, grouped by task with headers, front-loaded with the getting-started pair:

```
Getting started:   init, invite, join, status
Running:           daemon, reload, config, hosts
Several meshes:    mesh list|enable|disable|rename|remove
Services:          services list|add|remove
Membership:        admin issue|renew|revoke|show, keys, credential, key
Hardware:          keycard ...
Diagnostics:       paths, bound, version
```

Ideally generate it from the same table the dispatch switches on, so it cannot drift again. If some commands are deliberately "plumbing" (bound, credential set), say so with a `Plumbing:` header rather than omitting them — git's porcelain/plumbing split is the precedent for hiding *without* lying.

#### F2 — The standard help gesture fails on exactly the hidden groups (severity: high, hit by everyone who gets past F1)

`shrooms mesh -h` → `error: unknown mesh command "-h"; try: list, enable, disable, rename` (exit 1). Same for `admin -h` and `keycard -h`. `shrooms mesh help` fails identically. Meanwhile `shrooms config -h` works, and `shrooms services -h` prints a good usage block but then appends `error: no such subcommand` and exits 1 even though the user asked for exactly what they got.

Worse, bare `shrooms mesh` does not show help at all — it defaults to `mesh list`, which reads the root-owned config, so an unprivileged newcomer probing the group gets `permission denied ... try: sudo shrooms mesh list`. The error is well written (it explains *why* root: the file holds network keys), but the first contact with the group is a sudo demand instead of a syllabus. `-h` is the most universal gesture in the entire CLI world; it must never be parsed as a subcommand name.

**Suggestion.** One shared group-dispatch helper: recognise `-h`/`--help`/`help`/no-args as "print this group's usage to stdout, exit 0" before anything touches config or socket. Keep bare-`mesh`-means-`list` if you like the convenience, but only after `-h` is handled, and consider making bare group = help, list = explicit, which is what docker/kubectl/gh all do.

#### F3 — `key rotate`'s recovery instructions teach a command this same binary removed (severity: critical when hit, rarely hit)

`rotateKey` in `setup.go` ends with:

```
On every other device:
  shrooms join %s --name <NAME>
```

— i.e. `shrooms join <NETWORK-KEY>`. But `cmdJoin` in the same file explicitly rejects a positional key with "`shrooms join <KEY>` has been removed", and `set-key` got a tombstone in `main.go` saying the same. So the most destructive command in the tool (rotating the network key: every address changes, every device disconnects) prints, as its recovery procedure, a command that every device will refuse. Somebody who rotates on the advice of that output strands their whole mesh with instructions that lecture them about a removed feature at the exact moment they are most stressed. The removal messages themselves are exemplary — which makes the stale reference in rotate's success text a pure oversight, and a dangerous one.

Related staleness in the same feature: the top-level usage still says `key rotate    replace it (the only revocation before M5)` four lines below `admin revoke    withdraw one before it expires`. The README says credentials (ADR-018) are built. The help is arguing with itself about whether revocation exists.

**Suggestion.** Rewrite rotate's epilogue around the invite flow (or whatever the supported re-enrolment path after rotation actually is now — if there is none, rotate should say so *before* the confirmation, not after the write). Drop or rephrase the "only revocation before M5" line. Add a test that greps command output for `shrooms ...` strings and asserts each one parses — the tool already has the discipline of `config settings --names` for exactly this class of drift.

#### F4 — Group usage is a different genre in every group, and the lists disagree with themselves (severity: medium, hit often)

Four groups, four help styles: `services` has a proper usage block with three worked examples (the best leaf help in the tool); `config` has a decent four-line block; `admin` and `keycard` give a bare `usage: shrooms admin {init|issue|renew|revoke|show} [flags]` with no descriptions; `mesh` has no usage text at all, only the error string. And the lists are wrong in both directions: `keycard`'s bare usage names `{status|init|pair|readers|free-slots|forget|reset}` but its unknown-command hint offers only `status, readers, pair, free-slots, forget`; `mesh`'s hint says `try: list, enable, disable, rename` — omitting `remove`, so the one operation a leaver needs is absent from the only in-tool place its group's commands are enumerated (it does appear in `site/` docs and in completion). Whether the omissions are deliberate (destructive commands hidden from casual view) or drift, the effect is a user typing `shrooms mesh remove` on faith or not at all.

**Suggestion.** Same fix as F2: one usage function per group, complete, with one-line descriptions, used by bare invocation, `-h`, and unknown-subcommand errors alike. If `reset`/`remove` deserve a warning label, label them (`remove NAME    leave a mesh (asks for confirmation)`) — a labelled danger is safer than an invisible one, because the invisible one gets discovered by experiment.

#### F5 — `key` vs `keys`, and a credential you can set but not show (severity: medium)

`shrooms keys` prints this device's public keys; `shrooms key` manages the network secret and is one subcommand away from `rotate`, the most destructive operation in the tool. One letter separates them; they share no meaning. A user who remembers "something about keys" and types the wrong one gets, at best, a confusing usage line (`key {show|rotate}` — "rotate my public keys?") and at worst goes exploring near `rotate`. The standard (§1.1.3) says frequently-confused names should differ in word, not in number.

Similarly asymmetric: `credential set BLOB` exists, but there is no `credential show` — the "show" half lives, unlabelled, at the bottom of `shrooms keys` output (which does print the held credential's name, serial and expiry, and even warns when it is version 1 or expired — good content, unguessable address). Opposite halves of one lifecycle should be visible from each other.

**Suggestion.** Rename toward distinct words: e.g. `shrooms identity` (or `device keys`) for the public halves, and keep `key`/`network-key` for the shared secret — with a transition alias and one of those excellent tombstone messages. Add `credential show` (delegating to the same code `keys` uses today).

#### F6 — Two mechanisms, two vocabularies, for the same state changes (severity: medium, hit by multi-mesh and headless users)

`shrooms config set mesh on --mesh home` (through the daemon socket; needs a running daemon; group-readable socket suffices) and `shrooms mesh enable home` (edits the root-owned file directly; needs sudo; works with the daemon down) both switch a mesh on. `config set services immich:2283,...` and `services add immich --port 2283` both edit the published list — the services group's own comment says the config-set form "replaces the whole list at once", which means the older spelling silently deletes what the newer one added if a user mixes them. `config set relay on` similarly overlaps `init --relay`'s domain. The *reasons* for the split are written down and are good (mesh.go's comment: the file is the authority when the daemon is down); but the user is given two dialects and no signpost saying which one is the primary, and the two fail in different ways (no daemon vs. no sudo) that look like the operation itself being broken.

**Suggestion.** Pick a primary spelling per operation and have the secondary one point at it. Cheapest fix: `config settings` help annotates `mesh` and `services` with "prefer `shrooms mesh enable`" / "prefer `shrooms services add`", and `mesh enable`'s permission error mentions that with a daemon running, `config set mesh on --mesh X` works without sudo — the tool already loves teaching in error messages; teach the crossover there.

#### F7 — Undocumented synonyms everywhere (severity: low-medium)

`meshes` for `mesh`; `mesh ls|on|off|rm|leave`; `services rm`; `config check|list`. None appear in any help text, and completion (correctly) offers only the canonical forms. Aliases are kind to muscle memory, but undocumented ones split the community's vocabulary — two people's shell histories stop being mutually readable, and docs/issues get written in a dialect the help cannot confirm exists. `mesh leave` is the interesting one: "leave" is the *user's* word for the operation (the README's own table says "leave it"), and it is hidden behind an alias of a hidden subcommand of a hidden group (F1+F4). A first-timer wanting out will type `shrooms leave` — today that earns `unknown command "leave"` and the full usage dump, with no pointer.

**Suggestion.** Keep the aliases, list the good ones in the group usage (`remove (alias: leave, rm)`), and consider promoting the leave workflow to the same visibility as the join workflow — the usage footer teaches how to get in; nothing teaches how to get out.

#### F8 — Leaf help is a bare flag dump; requested help goes to stderr; help exit codes vary (severity: low-medium)

`shrooms init -h` prints `Usage of init:` and thirteen flags — no sentence saying what init *does*, no synopsis, no example (the stdlib `flag` default). The one-line descriptions live only in the top-level usage, so at exactly the moment a user drills into a command, the description disappears. Meanwhile explicitly-requested help (`shrooms help`, `shrooms -h`) prints to stderr — so `shrooms help | less` shows nothing — and help-ish exits are inconsistent: top-level `-h` exits 0, `config -h` 0, leaf `-h` 0, but `join -h`, `mesh -h`, `admin -h`, `services -h` exit 1. Cosmetic mismatch, same family: flags are single-dash in `-h` output (`-name`) but double-dash in all prose and completion (`--name`); Go accepts both, but the help disagreeing with the docs makes careful readers doubt one of them.

**Suggestion.** Set `fs.Usage` per command to print a one-paragraph description and one example above the flag dump; route requested help to stdout, exit 0, uniformly.

#### F9 — No "did you mean" (severity: low)

`shrooms stauts` prints `unknown command "stauts"` followed by the entire ~40-line usage. The information the user needs ("status") is in there, but they must diff it by eye. Every unknown token gets the same full dump, which trains people to stop reading it. **Suggestion:** closest-match suggestion (edit distance over the dispatch table plus aliases), and print only the suggestion plus "run `shrooms help` for the list" instead of the wall.

#### F10 — Opaque diagnostic names: `bound`, and `prepare` (severity: low)

`shrooms bound    what announce_bound would tell peers` is help text written in config-key jargon; a user who has not read ADR-023 cannot parse it, and `bound` as a bare verb suggests nothing ("bound for where?"). It would cost nothing as `services announce --preview` or `config show bound`. `prepare` is likewise vague — it stages a machine for enrolment, part of a four-command remote-enrolment workflow (`prepare` → `keys` → `admin issue` → `credential set`) that is scattered across the top level with nothing naming the workflow; each piece's help gestures at the next (good), but only the site docs show the whole dance. **Suggestion:** group them visually in the usage under an "enrolling a machine without moving secrets" header; consider `enrol`-flavoured naming long-term.

### 2.2 What is genuinely good — and some of it is exceptional

Calibration matters: judged against the standard in Part 1, shrooms scores *at or near the top of the field* on the two criteria ranked most important, and the faults above are concentrated in discoverability, the fourth.

- **Error messages are the best I have seen in a tool this size.** `status` on a permission error explains that the daemon is fine and *you* are not root, gives the sudo form *and* the permanent fix (`socket_group`) — "sudo is the answer once and the setting is the answer every time after", as the source comment puts it. `services` against an old daemon explains the version skew instead of relaying a baffling HTTP error, and even predicts which *misleading* error you might be looking at ("if that error looks like it is about the port rather than the type..."). The `set-key` and `join <KEY>` tombstones are model removals: what it did, why it is gone, what replaced it, the exact commands to type. `hintPermission` exists specifically because a wrapped EACCES once suppressed the hint — somebody here audits their own error paths. This is criterion #1 on my list and shrooms is exemplary at it, F3's stale rotate epilogue being the one bad apple.

- **Destructive operations are guarded with graded, GitHub-class ceremony.** `mesh remove` prints the network key *before* deleting it ("it is the one thing here that cannot be recovered"), explains what it deliberately leaves behind, and requires typing the mesh's name; `key rotate` lists four concrete consequences and requires typing the device's name; `keycard reset` requires typing "wipe this card", and asks for the PIN only *after* confirmation "so a mistyped yes costs no PIN attempt". All take `--yes` for scripts. `admin renew` and `config flatten` have `--dry-run`. Criterion #2: near-perfect (marred only by these commands being hidden, F1/F4).

- **State changes narrate themselves and hand you the next command.** `mesh enable` ends with "It takes effect on the next restart: `sudo systemctl restart shrooms`" — deferring to systemd's vocabulary rather than inventing `shrooms restart`. `mesh remove` offers to restart the daemon itself and says what happens if it cannot. `keys` prints the exact `admin issue` invocation to run on the admin machine, prefilled with this device's hex keys. `invite` prints the exact `join --invite` line (and a QR by default). `status` flags stale /etc/hosts entries with the repair command inline.

- **`status` is a model inspection command:** dense but readable tables, per-mesh grouping, services with the literal URL you would type, `--json` for machines — and the completion script pointedly parses the JSON, "the table is for people and is allowed to change".

- **The completion script is unusually conscientious:** peer names from the live daemon, mesh labels from two sources with the failure mode reasoned about, reader names quoted because they contain spaces, durations offered because Go's format rejects "30d" unhelpfully, and setting names pulled from `config settings --names` so completion cannot drift from the binary. That flag exists *for* completion — a machine-readable enumeration kept beside the human one. More tools should steal this.

- **`splitArgs` quietly fixes Go's worst CLI trap** (flags after positionals silently ignored), motivated in a comment by an actual CI incident. `services add immich --port 2283` works in the order people type it.

- **Flags respect what the machine knows** (criterion #6): `--serial` defaults to now, `--name` to hostname, config/state/socket to standard paths, `admin revoke` takes `--name` resolved via the daemon so nobody hand-copies hex. I found no required flag that a machine could have defaulted.

- **`invite`/`join` is a textbook opposite pair,** the network key never crossing a screen; `pair`/`forget`, `enable`/`disable` likewise.

The pattern across the faults is one pattern, not ten: the individual commands are crafted with unusual care, but the *map* of the tool — front page, group help, cross-references between old and new spellings — has fallen behind the territory. Everything in F1–F4 is the same fix wearing four hats: a single source of truth for "what commands exist", rendered consistently everywhere the tool talks about itself.

---

## Part 3 — Against the project's own proposal

Read only after Parts 1 and 2 were written: `docs/where-mesh-commands-live.md` (status: proposed, 2026-08-27), which proposes splitting device setup from mesh membership — `prepare` as the device primitive, `mesh new`/`mesh join` as the mesh primitives with `mesh remove` beside them, and `init`/`join` kept as fresh-machine conveniences that compose the two.

**Where I agree.**

- The central diagnosis is correct and is a sharper cut of something my review only grazed. I flagged the join/leave visibility asymmetry (F7) and `prepare`'s underselling name (F10), but the proposal names the underlying disease: `init --mesh X` is "add a mesh wearing the word *initialise*", and its opposite is `mesh remove` — a create/destroy pair that shares neither name nor level, which is exactly the asymmetry my Part 1 §3 calls the worst kind. Credit where due: an outside reviewer running `-h` can see the two commands; only someone who has lived the second-mesh flow feels that they are a pair. I underweighted this.
- `mesh new` beside `mesh remove`, every mesh verb under `mesh`, opposites adjacent: yes. This is the kubectl-shaped move and the group has enough verbs to earn it (§1.1.5).
- Keeping `init` and `join` as top-level conveniences for the fresh machine: yes, emphatically. "One command on a new machine" is the first five minutes (§1.1.8), and the proposal is right that install docs and muscle memory hang off it.
- The migration instinct — aliases kept "for as long as anybody wants", tombstones for what moves — matches the discipline the tool already shows with `set-key`, which is the right way to restructure a CLI people script against.

**Where I disagree, or would reorder.**

1. **The proposal fixes my finding #5-by-rank while #1 through #4 stay open.** Restructuring which group `init` lives in is structure (Part 1 criterion 5); the front page that hides the `mesh` group entirely (F1), the `-h` gesture failing on it (F2), and `key rotate` teaching a removed command (F3) are criteria 1, 2 and 4, and all are afternoon-sized fixes. Worse, the restructure *raises* the stakes on F1: it moves the primary creation verb (`mesh new`) into a group that today's help text does not admit exists. Shipping `mesh new` before fixing the usage text would make the tool's map more wrong, not less. Sequence: complete-and-uniform help first, then move the furniture.
2. **The proposal itself contains a stale command.** Its convenience row is `shrooms join KEY — prepare + mesh join`, but join-by-key was removed (per the README, on the same date this proposal is stamped; `cmdJoin` in `setup.go` now rejects it with a tombstone). A structure proposal that teaches a form the binary refuses is the same failure mode as F3, in the design docs instead of the output. Presumably it predates the removal by hours — but it should be updated before anyone builds from it, and it is evidence for the F3 suggestion of mechanically checking every `shrooms ...` string the project emits.
3. **`mesh join --invite` beside top-level `join` needs a signpost, not just an alias.** Two live spellings of the most-taught operation in the tool is an F6-style dialect split. Fine if each one's help names the other and the docs pick one voice; corrosive if they drift.
4. **On renaming `prepare`: rename, but not to `device init`.** The tool already has `init`, `admin init`, and `keycard init` meaning three different ceremonies; a fourth `init` deepens the overload. `setup` is better; `enrol`-family naming would tie it to the workflow it anchors (F10). Whatever the name, keep `prepare` as a tombstoned alias — the proposal's own standard.
5. **One addition the proposal stops short of:** it puts every mesh verb under `mesh` but leaves the *leave* verb's discoverability unaddressed — `mesh remove` is still absent from its own group's unknown-command hint (F4) and "leave" exists only as an undocumented alias (F7). If the restructure happens, that is the moment to make the way out as legible as the way in.

Net: the proposal is a good second step described as a first step. It shares this review's values — symmetry, opposites adjacent, conveniences preserved — and none of its moves conflict with anything in Part 2. But the cheap, high-rank repairs (one complete usage text everywhere, `-h` on groups, the rotate epilogue) should land first, both because they cost users more today and because the restructure depends on the help system being trustworthy enough to announce it.
