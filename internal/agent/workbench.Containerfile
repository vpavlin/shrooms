# The workbench: what a caged Shrooms Agents session runs in (ADR-037,
# docs/agents-in-cages.md). Root inside, the owner outside; anything else the
# agent needs it installs itself, and keeps until its session is deleted.
#
# Claude Code is not installed here: the agent mounts the machine's own
# binary, so a cage runs the version the machine runs.
FROM docker.io/library/node:22-bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates openssh-client build-essential python3 python3-pip python3-venv \
        ripgrep jq less procps unzip xz-utils file \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g @mariozechner/pi-coding-agent && npm cache clean --force
LABEL xyz.vpavlin.shrooms.workbench="1"
CMD ["sleep", "infinity"]
