#!/usr/bin/env python3
"""Checks the files agents read (AGENTS.md, docs/glossary.md) against the
repository, so a claim in them that stops being true fails CI instead of
misleading the next agent (issue #22). Agents take these files as given and
rarely check; so every concrete claim in them has to be one a script can:

- a path in `code` exists, or is a build output git ignores;
- `make TARGET` names a target the Makefile has;
- ADR-NNN has its record in docs/adr;
- `pkg.Symbol` (agent.LocalAddr, mesh.QualifiedDNSName) is defined in
  internal/pkg or cmd/pkg.

Prose that no script can check (why something is so, how to work) is allowed;
names, paths, commands and numbers are what get checked. Run it with no
arguments; it prints each failure with its file and line and exits 1.
"""
import glob
import os
import re
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
FILES = ['AGENTS.md', 'CLAUDE.md', 'docs/glossary.md']
PATHY = re.compile(r'^\.?/?[\w.@-]+(/[\w.@*-]*)*$')
EXTS = ('.md', '.go', '.sh', '.py', '.yml', '.yaml', '.json', '.toml', '.qml', '.kts', '.html', '.txt')


def targets():
    return set(re.findall(r'^([A-Za-z0-9][\w.-]*):', open(os.path.join(ROOT, 'Makefile')).read(), re.M))


def ignored(path):
    return subprocess.run(['git', 'check-ignore', '-q', path], cwd=ROOT).returncode == 0


def defined(pkg, name):
    """Whether name is declared at package level in internal/pkg or cmd/pkg."""
    dirs = [d for d in (os.path.join(ROOT, 'internal', pkg), os.path.join(ROOT, 'cmd', pkg)) if os.path.isdir(d)]
    if not dirs:
        return None  # no such package of ours
    decl = re.compile(r'^(func|type|var|const)\s+(\([^)]*\)\s*)?' + re.escape(name) + r'\b|^\t' + re.escape(name) + r'\b',
                      re.M)
    for d in dirs:
        for f in glob.glob(os.path.join(d, '*.go')):
            if not f.endswith('_test.go') and decl.search(open(f).read()):
                return True
    return False


def check(path, make):
    fails = []
    text = open(os.path.join(ROOT, path)).read()
    in_fence = False
    for n, line in enumerate(text.splitlines(), 1):
        if line.startswith('```'):
            in_fence = not in_fence
            continue
        spans = [line.strip()] if in_fence and line.strip() else re.findall(r'`([^`]+)`', line)
        for adr in re.findall(r'\bADR-(\d{3})\b', line):
            if not glob.glob(os.path.join(ROOT, 'docs', 'adr', adr + '-*.md')):
                fails.append(f'{path}:{n}: ADR-{adr} has no record in docs/adr')
        for span in spans:
            words = span.split()
            if not words or words[0] in ('#', '//'):
                continue
            if words[0] == 'make':
                for t in words[1:]:
                    if not t.startswith('-') and '=' not in t and t not in make:
                        fails.append(f'{path}:{n}: `make {t}`: no such Makefile target')
                continue
            # A command: its program, when it is a path of ours.
            word = words[0]
            m = re.fullmatch(r'([a-z][a-z0-9]*)\.([A-Z]\w*)', word)
            if m:
                # Written as pkg.Symbol, it is a claim about one of our
                # packages: a misspelt package fails as surely as a misspelt
                # symbol (reviewer, #29: `meshx.LocalAddr` used to pass).
                ok = defined(m.group(1), m.group(2))
                if ok is None:
                    fails.append(f'{path}:{n}: `{word}`: no package {m.group(1)} in internal/ or cmd/')
                elif not ok:
                    fails.append(f'{path}:{n}: `{word}`: {m.group(2)} is not defined in package {m.group(1)}')
                continue
            if PATHY.match(word) and ('/' in word or word.endswith(EXTS)) and not word.startswith(('http', '-')) \
                    and '*' not in word:
                p = word.lstrip('./') if word.startswith('./') else word
                if not os.path.exists(os.path.join(ROOT, p)) and not ignored(p):
                    fails.append(f'{path}:{n}: `{word}`: no such path')
    return fails


def main():
    make = targets()
    fails = []
    for f in FILES:
        if os.path.exists(os.path.join(ROOT, f)):
            fails += check(f, make)
    for f in fails:
        print(f)
    if fails:
        sys.exit(f'{len(fails)} claim(s) in the agent docs do not match the repository')
    print('agent docs: every path, make target, ADR and symbol checked is real')


if __name__ == '__main__':
    main()
