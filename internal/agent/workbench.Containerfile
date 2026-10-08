# The workbench: what a caged Shrooms Agents session runs in (ADR-037,
# docs/agents-in-cages.md). Root inside, the owner outside; anything else the
# agent needs it installs itself, and keeps until its session is deleted.
#
# Claude Code is not installed here: the agent mounts the machine's own
# binary, so a cage runs the version the machine runs.
#
# Two images from it (Cages.builtin): the workbench, and the desktop — the
# workbench with an X server to drive and record apps in (a reviewer's).

FROM docker.io/library/node:22-bookworm-slim AS workbench
RUN apt-get update && apt-get install -y --no-install-recommends \
        git curl ca-certificates openssh-client build-essential pkg-config python3 python3-pip python3-venv \
        ripgrep jq less procps psmisc unzip xz-utils file \
    && rm -rf /var/lib/apt/lists/*
RUN npm install -g @mariozechner/pi-coding-agent && npm cache clean --force
# The GitHub CLI, for a cage given the owner's login (Cage.GitHub).
RUN curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg -o /usr/share/keyrings/githubcli.gpg \
    && echo "deb [signed-by=/usr/share/keyrings/githubcli.gpg] https://cli.github.com/packages stable main" > /etc/apt/sources.list.d/github-cli.list \
    && apt-get update && apt-get install -y --no-install-recommends gh && rm -rf /var/lib/apt/lists/*
LABEL xyz.vpavlin.shrooms.workbench="1"
CMD ["sleep", "infinity"]

FROM workbench AS desktop
# Xvfb and xdotool to run and drive a GUI, ImageMagick and ffmpeg to take
# pictures and recordings of it, and what Qt apps (Basecamp) and browsers
# load. AppImages unpack themselves: there is no FUSE in a cage.
RUN apt-get update && apt-get install -y --no-install-recommends \
        xvfb xauth xdotool x11-utils x11-xserver-utils imagemagick ffmpeg \
        fonts-dejavu-core fonts-noto-core fonts-noto-color-emoji \
        libgl1 libegl1 libopengl0 libglib2.0-0 libfontconfig1 libfreetype6 libdbus-1-3 libnss3 libasound2 \
        libxkbcommon0 libxkbcommon-x11-0 libxcb-cursor0 libxcb-icccm4 libxcb-image0 libxcb-keysyms1 \
        libxcb-randr0 libxcb-render-util0 libxcb-shape0 libxcb-xinerama0 libxcb-xkb1 libxcb-xfixes0 \
        libx11-xcb1 libxrender1 libxi6 libxcomposite1 libxdamage1 libxrandr2 libxtst6 libsm6 libice6 \
    && rm -rf /var/lib/apt/lists/*
ENV APPIMAGE_EXTRACT_AND_RUN=1
LABEL xyz.vpavlin.shrooms.workbench="desktop"
