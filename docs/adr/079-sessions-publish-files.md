# 079. Sessions publish files: a link on the mesh, in a fixed set of places

**Status:** accepted 2026-11-02 by the owner ("this is a good idea"); built.

## Context

Agents kept reaching for email or other outside services to hand Václav a
file, or wrote to the office file server (dufs on pi5) by hand, wherever they
liked: three weeks after it was tidied into `releases/`, `reviews/`,
`notes/`, `projects/` and `tools/` (2026-10-13), its top level had grown back
to eighty entries. A skill would have reached Claude Code only; pi and
OpenCode sessions get their tools from the agent (`shrooms-agent mcp`), as
`send_file` and `ask_agent` already are.

The owner asked whether the file server should be part of Shrooms or Shrooms
Agents. It is not part of Shrooms: the network daemon stays the boring,
privileged part. Nor is a particular server Agents' business: "anyone can
bring anything".

## Decision

- **An MCP tool, `publish_file`**, for every session but a sealed one: a file,
  an area (`projects`, `reviews`, `releases`, `notes`, `tmp`) and a folder in
  it (default: the session's name), never the top level. It returns the link.
- **The agent does the upload**, through its socket or the cage's proxy, as
  the session itself: a session publishes through its own machine's agent
  only, and a sealed session not at all (its results leave through its
  outbox, ADR-045).
- **Any server that takes an HTTP PUT** (dufs, a WebDAV share), named by the
  agent's `--files-url` (`$SHROOMS_AGENT_FILES_URL`; `install-agent.sh
  --files-url`). The agent checks the copy arrived whole (its size, by HEAD)
  before it returns a link.
- **Without one, the agent keeps the files itself** and serves them on its
  mesh addresses at `/files/`, behind the same gate as everything else it
  serves: someone setting up Shrooms Agents has publishing with nothing more
  to run.
- **`tmp/` is emptied after two weeks** where the agent keeps the files; on a
  separate server that is the server's own job (on pi5, a timer).
- **What is published is read by the whole mesh**, with no password: the
  tool's description says never to publish secrets, keys or private data.
- At most 100 MB a file, as for `send_file`: a cage's proxy holds the file in
  memory. Bigger files go to the server by hand.

## Consequences

- Agents get a link without leaving the mesh, and the server keeps its
  layout.
- An agent with no file server serves what it published as long as it runs;
  the URL names its mesh address.
