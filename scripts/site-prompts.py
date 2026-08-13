#!/usr/bin/env python3
"""Move the shell prompt out of the text of every code block.

A `$ ` typed into a <pre> is part of the text, so triple-clicking a line
selects the prompt with it and pasting that into a terminal runs nothing.
Putting it in a ::before pseudo-element keeps it on screen and out of the
selection — pseudo-elements are not text, so they are not selected, not copied
and not read out by a screen reader as though they were something to type.

    python3 scripts/site-prompts.py

Idempotent: a line already wrapped is left alone.
"""
import pathlib
import re
import sys

# A command line inside a code block: optional leading spaces, "$ ", the rest.
LINE = re.compile(r'^(\s*)\$ (.+)$')
BLOCK = re.compile(r'(<pre>.*?</pre>)', re.S)


def convert(block: str) -> str:
    out = []
    for line in block.split("\n"):
        m = LINE.match(line)
        if m and 'class="cmd"' not in line:
            out.append(f'{m.group(1)}<span class="cmd">{m.group(2)}</span>')
        else:
            out.append(line)
    return "\n".join(out)


def main(paths):
    changed = 0
    for p in paths:
        text = p.read_text()
        new = BLOCK.sub(lambda m: convert(m.group(1)), text)
        if new != text:
            p.write_text(new)
            changed += 1
            print(f"  prompts moved out of the text in {p}")
    print(f"{changed} page(s) changed")


if __name__ == "__main__":
    root = pathlib.Path(__file__).resolve().parent.parent
    args = [pathlib.Path(a) for a in sys.argv[1:]]
    main(args or sorted((root / "site").glob("*.html")))
