# Working in this repository

Short on purpose: everything here is either checked against the repository
by `scripts/check-agent-docs.py` in CI (paths, `make` targets, ADR numbers,
package symbols such as `agent.LocalAddr`), or generated from the code (`docs/codemap.md`). If
something here and the code disagree, the code is right; fix this file, and
CI will tell you when it no longer matches.

## What this is

Shrooms is an overlay mesh: WireGuard between a person's own devices, found
and joined through Logos Delivery (Waku), with no account and no server of
ours. Shrooms Agents runs coding-agent sessions (Claude Code, pi) on each
machine and serves them to the person's other devices over that mesh, or on
one machine without it (ADR-056).

- `README.md`: what it does and how to run it.
- `DESIGN.md`: how it works.
- `docs/adr/README.md`: every decision, with its status; the records are
  `docs/adr/NNN-*.md`.
- `docs/agents.md`: Shrooms Agents.
- `docs/codemap.md`: every Go package and what it is (generated).
- `docs/glossary.md`: the words this project uses.

## Layout

- `cmd/shrooms`: the mesh daemon and its CLI.
- `cmd/shrooms-agent`: the agent (sessions, cages, the A2A tools).
- `cmd/shrooms-relay`: the relay.
- `internal/`: the packages; `docs/codemap.md` lists them.
- `mobile/`: a separate Go module for the Android app (`gomobile bind`);
  `android/` is the app itself.
- `basecamp/`: the Shrooms Basecamp view; `basecamp/core` is `shrooms_core`,
  the C++ module behind it; `basecamp-agents/` is the Shrooms Agents view.
- `site/`: the website, deployed from master by
  `.github/workflows/pages.yml`. Some of its pages are generated from
  `docs/` at deploy (`scripts/render-adrs.py`) and ignored by git, such as
  `site/agents-using.html`: edit the markdown, not the HTML.
- `scripts/install.sh` and `scripts/install-agent.sh`: the installers.

## Building and testing

Most packages are plain Go. The ones that reach `internal/waku` link
liblogosdelivery through cgo; `scripts/test-packages.sh linked` lists them,
and `scripts/test-packages.sh pure` the rest.

- `make test-unit`: the pure packages, with the race detector, no library
  needed.
- `make test`: everything; needs liblogosdelivery in `docker/build/lib`
  (ignored by git). `make deps` builds it, `make deps-basecamp` reuses the
  copy Basecamp installed, and `make check-lib` says what is missing.
- `make fmt`: gofmt. CI and `make test` fail on unformatted code.
- The agent alone, without the library:
  `CGO_ENABLED=0 go test ./internal/agent/ ./cmd/shrooms-agent/`.
- `internal/keycard` needs cgo for its own dependencies, even without the
  library: test it with cgo on.

## How work is done here

- A settled decision, or the reason something shipped the way it did, goes
  into an ADR in `docs/adr/` and a row in `docs/adr/README.md`, in the same
  change. Amend an existing ADR rather than contradicting it silently.
- Comments say why, not what; the history of a mistake that shaped the code
  belongs in the comment next to it.
- Stage files by name. `git add -A` has committed a 32 MB store before.
- Phones run their delivery node as Edge (`state.ModeEdge`, set in
  `mobile/support.go`; ADR-035); desktops default to Core.

## Words

`docs/glossary.md` has the project's terms, each with where it is defined.
