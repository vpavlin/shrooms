#!/usr/bin/env python3
"""Writes docs/codemap.md: the repository's directories and top-level documents,
then every committed Go package with the first sentence of its own package doc
comment.

    python3 scripts/codemap.py          write it
    python3 scripts/codemap.py --check  fail if it is not what would be written

The packages are generated, not written, so they cannot say anything the code
does not: a package whose description is wrong is fixed in its doc comment, and
this is run again. The directories outside Go and the documents have no doc
comment, so they are described in DIRS and DOCS below; what is checked is that
every one that exists is described and every one described exists, not that
the words are right (strudelPi, #29: the map was only of Go packages).
CI runs --check, so a new or renamed package, or a changed doc comment, fails
the build until the map is regenerated (issue #22).
"""
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
OUT = os.path.join(ROOT, 'docs', 'codemap.md')

# Every committed directory at the top, and one level into those that are not
# Go (whose packages the table below lists). A new or removed one fails --check
# until it is described here or taken out.
DIRS = {
    '.github': 'GitHub configuration, and CODEOWNERS: who must review what.',
    '.github/workflows': 'CI, a nightly build against upstream liblogosdelivery, and the site\'s deployment.',
    'android': 'The Android app (Kotlin, Compose): Shrooms and Shrooms Agents, built from one project (ANDROID.md).',
    'android/app': 'The app module; its Go core comes from `mobile/` through gomobile.',
    'assets': 'Logos and icons, and the scripts that draw them.',
    'basecamp': 'The Shrooms Basecamp view (`Main.qml`) and what it is built from.',
    'basecamp/core': '`shrooms_core`, the C++ module behind both Basecamp views: it reaches the daemon and the agents.',
    'basecamp/test': 'The views\' test harnesses, run by `check.sh` in CI.',
    'basecamp-agents': 'The Shrooms Agents Basecamp view.',
    'basecamp-agents/test': 'Its QML harness.',
    'cmd': 'The commands; each is a Go package below.',
    'deploy': 'Running pieces of the mesh elsewhere.',
    'deploy/akash': 'A blind relay on Akash.',
    'deploy/home': 'A blind relay on your own connection.',
    'deploy/metrics': 'Prometheus and Grafana for the fleet (docs/metrics.md).',
    'deps': 'Native dependencies pinned by content (liblogosdelivery).',
    'docker': 'Container images: the daemon, the agent, and building liblogosdelivery.',
    'docs': 'Design notes, analyses and guides.',
    'docs/adr': 'Architecture decision records, indexed in its README.',
    'docs/agents': 'The Shrooms Agents guides: setup, using, together, cages.',
    'internal': 'The Go packages; each is listed below.',
    'mobile': 'A separate Go module, the core the Android app binds (gomobile).',
    'packaging': 'systemd units, shell completion, and installing a built release.',
    'scripts': 'Installers, build and release scripts, and the checks CI runs on the docs.',
    'scripts/screenshots': 'Taking the site\'s screenshots.',
    'site': 'The website, deployed from master; some pages are generated from `docs/`.',
    'site/shots': 'The screenshots it shows.',
}

# The documents at the top, by what they are for.
DOCS = {
    'AGENTS.md': 'For coding agents working here: layout, building, how work is done (CLAUDE.md imports it).',
    'ANDROID.md': 'The phone as a full mesh participant, and how the app is built.',
    'CLAUDE.md': 'Imports AGENTS.md.',
    'DESIGN.md': 'How the mesh works, and why.',
    'PROTOTYPE.md': 'The original build plan: what to prove, in order.',
    'README.md': 'What it does and how to run it.',
    'RELAYS.md': 'Blind relays: what they forward and what they cannot see.',
    'SECURITY.md': 'What is protected, what leaks, what is deferred.',
    'TESTING.md': 'Scenarios worth running by hand, and what counts as a pass.',
}
GO_TOPS = {'cmd', 'internal', 'mobile'}


def tracked_dirs():
    files = subprocess.run(['git', 'ls-files', '*.go'], cwd=ROOT, capture_output=True, text=True,
                           check=True).stdout.split()
    return {os.path.dirname(f) for f in files}


def packages(module_dir):
    """(path from the repository root, doc) for each package of the module."""
    env = dict(os.environ, CGO_ENABLED='0', GOFLAGS='-mod=mod')
    out = subprocess.run(['go', 'list', '-e', '-f', '{{.Dir}}\t{{.Doc}}', './...'], cwd=module_dir,
                         capture_output=True, text=True, env=env, check=True).stdout
    for line in out.splitlines():
        d, _, doc = line.partition('\t')
        yield os.path.relpath(d, ROOT), doc.strip()


def first_sentence(doc):
    m = re.match(r'(.+?[.!?])(\s|$)', doc)
    return (m.group(1) if m else doc) or '(no package doc comment)'


def tracked_layout():
    """The committed directories DIRS must describe, and the top-level documents."""
    files = subprocess.run(['git', 'ls-files'], cwd=ROOT, capture_output=True, text=True,
                           check=True).stdout.split()
    dirs, docs = set(), set()
    for f in files:
        parts = f.split('/')
        if len(parts) == 1:
            if f.endswith('.md'):
                docs.add(f)
            continue
        dirs.add(parts[0])
        if len(parts) > 2 and parts[0] not in GO_TOPS:
            dirs.add(parts[0] + '/' + parts[1])
    return dirs, docs


def layout_problems():
    dirs, docs = tracked_layout()
    out = [f'{d}/ is not described in scripts/codemap.py DIRS' for d in sorted(dirs - DIRS.keys())]
    out += [f'DIRS describes {d}/, which is not in the repository' for d in sorted(DIRS.keys() - dirs)]
    out += [f'{d} is not described in scripts/codemap.py DOCS' for d in sorted(docs - DOCS.keys())]
    out += [f'DOCS describes {d}, which is not in the repository' for d in sorted(DOCS.keys() - docs)]
    return out


def render():
    keep = tracked_dirs()
    rows = []
    for module in (ROOT, os.path.join(ROOT, 'mobile')):
        for path, doc in packages(module):
            if path in keep:
                rows.append((path, first_sentence(doc)))
    rows.sort()
    lines = ['# Code map', '',
             'Generated by `scripts/codemap.py`. Do not edit it by hand: the packages come',
             'from each Go package\'s own doc comment, the directories and documents from',
             'the script, and CI fails while this file is out of date or a directory is',
             'not described. That they are all here is checked; what they say is not.', '',
             '## Directories', '', '| Directory | What it is |', '|---|---|']
    lines += [f'| `{d}/` | {DIRS[d]} |' for d in sorted(DIRS)]
    lines += ['', '## Documents at the top', '', '| Document | What it is for |', '|---|---|']
    lines += [f'| `{d}` | {DOCS[d]} |' for d in sorted(DOCS)]
    lines += ['', '## Go packages', '', '| Package | What it is |', '|---|---|']
    # Escaped outside the f-string: a backslash inside one is a syntax error
    # before Python 3.12 (reviewer, #29).
    for p, d in rows:
        cell = d.replace('|', '\\|')
        lines.append(f'| `{p}` | {cell} |')
    return '\n'.join(lines) + '\n'


def main():
    problems = layout_problems()
    if problems:
        sys.exit('\n'.join(problems))
    text = render()
    if '--check' in sys.argv[1:]:
        old = open(OUT).read() if os.path.exists(OUT) else ''
        if old != text:
            sys.exit('docs/codemap.md is out of date: run python3 scripts/codemap.py')
        print('docs/codemap.md is current')
        return
    open(OUT, 'w').write(text)
    print(f'wrote {os.path.relpath(OUT, ROOT)}')


if __name__ == '__main__':
    main()
