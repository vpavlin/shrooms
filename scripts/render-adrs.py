#!/usr/bin/env python3
"""Render docs/adr/*.md into site/adr/*.html, in the site's own clothes.

The decision record is the most-read prose in this project and until now the
website sent people to GitHub to read it — a different typeface, a different
column width, and a navigation bar belonging to somebody else. These pages are
the same markdown, wrapped in the same template as every other page on the
site.

Standard library only, deliberately. The core has three direct dependencies and
a policy of doing wire formats by hand; a documentation renderer is a poor place
to start making exceptions, and this one only has to understand the markdown
these twenty-six files actually use. Everything supported is listed in
`inline()` and `blocks()` below, and anything else will come out as text rather
than as markup — which is the right failure for a file somebody is about to
read.

The output is generated, never committed (see .gitignore): the markdown is the
source, and a second copy in git is a second copy to forget.
"""

import html
import pathlib
import posixpath
import re
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SRC = ROOT / "docs" / "adr"
OUT = ROOT / "site" / "adr"

# Where a link that is not an ADR has to go. The site ships only the pages in
# site/, so a link to SECURITY.md or to a source file can only be a link to the
# repository — an absolute one, because the reader is no longer standing in
# docs/adr/ where the relative path was written.
REPO_BLOB = "https://github.com/vpavlin/shrooms/blob/master/"

# The path the markdown is written relative to, used to resolve `../../` out of
# a link before deciding what it points at.
SRC_REL = "docs/adr"

ADR_FILE = re.compile(r"^\d{3}-[a-z0-9.-]+\.md$")


# --- links --------------------------------------------------------------
#
# Three cases, and getting them wrong is worse than not rendering at all: a
# link that silently goes nowhere is harder to notice than a page that looks
# unstyled.


def rewrite_link(target):
    """docs/adr-relative markdown target -> a URL that works from site/adr/."""
    if "://" in target or target.startswith(("mailto:", "#")):
        return target

    path, sep, frag = target.partition("#")
    if not path:
        return target

    resolved = posixpath.normpath(posixpath.join(SRC_REL, path))
    base = posixpath.basename(resolved)

    if posixpath.dirname(resolved) == SRC_REL and ADR_FILE.match(base):
        # A sibling record, rendered right next to this page.
        out = base[:-3] + ".html"
    else:
        out = REPO_BLOB + resolved

    return out + sep + frag if sep else out


# --- inline -------------------------------------------------------------
#
# One pass, one alternation, tried in precedence order: code spans win over
# everything (they are how this project writes `<service>.<device>.mesh` and
# `admin_keys`, and neither should be read as markup), then links, then strong,
# then the two emphasis spellings.
#
# re.S because emphasis wraps across a line break — ADR-014 has *believed
# online* split over two lines, and a paragraph is joined before it gets here.

INLINE = re.compile(
    r"""
      (?P<ticks>`+)(?P<code>.+?)(?P=ticks)
    | \[(?P<ltext>[^\]]*)\]\((?P<ltarget>[^)\s]+)\)
    | \*\*(?P<strong>\S(?:.*?\S)?)\*\*
    | \*(?P<em>[^\s*](?:.*?[^\s*])?)\*
    # An underscore only opens and closes at a word edge, so x86_64,
    # SO_PEERCRED and admin_keys survive being written outside a code span.
    | (?<![0-9A-Za-z_])_(?P<uem>[^\s_](?:.*?[^\s_])?)_(?![0-9A-Za-z_])
    """,
    re.X | re.S,
)


def inline(text):
    out = []
    pos = 0
    for m in INLINE.finditer(text):
        out.append(html.escape(text[pos:m.start()]))
        if m.group("code") is not None:
            out.append("<code>%s</code>" % html.escape(m.group("code")))
        elif m.group("ltext") is not None:
            href = html.escape(rewrite_link(m.group("ltarget")), quote=True)
            out.append('<a href="%s">%s</a>' % (href, inline(m.group("ltext"))))
        elif m.group("strong") is not None:
            out.append("<strong>%s</strong>" % inline(m.group("strong")))
        elif m.group("em") is not None:
            out.append("<em>%s</em>" % inline(m.group("em")))
        else:
            out.append("<em>%s</em>" % inline(m.group("uem")))
        pos = m.end()
    out.append(html.escape(text[pos:]))
    return "".join(out)


def plain(text):
    """The same text with the markup taken off, for a meta description."""
    text = re.sub(r"\[([^\]]*)\]\([^)\s]+\)", r"\1", text)
    text = re.sub(r"[`*_]", "", text)
    return re.sub(r"\s+", " ", text).strip()


# --- blocks -------------------------------------------------------------

UL = re.compile(r"^(\s*)[-*+]\s+(?=\S)")
OL = re.compile(r"^(\s*)\d+[.)]\s+(?=\S)")
FENCE = re.compile(r"^\s*```\s*(\S*)\s*$")
HEADING = re.compile(r"^(#{1,4})\s+(.*?)\s*#*\s*$")
RULE = re.compile(r"^\s*(?:-{3,}|\*{3,}|_{3,})\s*$")
ALIGN = re.compile(r"^\s*\|?(?:\s*:?-{1,}:?\s*\|)+\s*:?-*:?\s*\|?\s*$")


def indent_of(line):
    return len(line) - len(line.lstrip())


def is_marker(line):
    return marker_type(line) is not None


def marker_type(line):
    return "ol" if OL.match(line) else "ul" if UL.match(line) else None


def is_table(lines, i):
    return (
        lines[i].lstrip().startswith("|")
        and i + 1 < len(lines)
        and ALIGN.match(lines[i + 1])
        and "-" in lines[i + 1]
    )


def starts_block(lines, i):
    line = lines[i]
    return bool(
        FENCE.match(line)
        or HEADING.match(line)
        or RULE.match(line)
        or line.lstrip().startswith(">")
        or is_marker(line)
        or is_table(lines, i)
    )


def slug(text, seen):
    s = re.sub(r"[^a-z0-9]+", "-", plain(text).lower()).strip("-") or "section"
    n = seen.get(s, 0)
    seen[s] = n + 1
    return s if not n else "%s-%d" % (s, n + 1)


def split_row(line):
    s = line.strip()
    if s.startswith("|"):
        s = s[1:]
    if s.endswith("|"):
        s = s[:-1]
    return [c.strip() for c in s.split("|")]


def alignments(line):
    out = []
    for c in split_row(line):
        left, right = c.startswith(":"), c.endswith(":")
        out.append("center" if left and right else "right" if right else "left")
    return out


def cell(tag, text, align):
    # style.css leaves every cell aligned left, which is every table in these
    # files. An attribute appears only where the markdown asked for something
    # else, so nothing is added to the stylesheet for a case that never occurs.
    attr = "" if align == "left" else ' style="text-align:%s"' % align
    return "<%s%s>%s</%s>" % (tag, attr, inline(text), tag)


def parse_table(lines, i):
    header = split_row(lines[i])
    align = alignments(lines[i + 1])
    align += ["left"] * (len(header) - len(align))
    i += 2

    rows = []
    while i < len(lines) and lines[i].lstrip().startswith("|"):
        rows.append(split_row(lines[i]))
        i += 1

    out = ["<table>"]
    out.append("<tr>%s</tr>" % "".join(
        cell("th", c, align[n] if n < len(align) else "left")
        for n, c in enumerate(header)))
    for row in rows:
        out.append("<tr>%s</tr>" % "".join(
            cell("td", c, align[n] if n < len(align) else "left")
            for n, c in enumerate(row)))
    out.append("</table>")
    return "\n".join(out), i


def parse_list(lines, i, seen):
    indent = indent_of(lines[i])
    kind = marker_type(lines[i])
    items = []

    while i < len(lines):
        line = lines[i]

        if not line.strip():
            # A blank line ends the list unless what follows is still inside
            # it — another item of the same kind, or an indented continuation.
            j = i
            while j < len(lines) and not lines[j].strip():
                j += 1
            if j >= len(lines):
                break
            if not (indent_of(lines[j]) > indent
                    or (indent_of(lines[j]) == indent
                        and marker_type(lines[j]) == kind)):
                break
            if items:
                items[-1].extend([""] * (j - i))
            i = j
            continue

        if indent_of(line) < indent:
            break

        m = UL.match(line) or OL.match(line)
        if m and indent_of(line) == indent:
            # A bullet where numbers were is a different list, not a further
            # item, and swallowing it would silently renumber somebody's steps.
            if marker_type(line) != kind:
                break
            items.append([line[m.end():]])
            i += 1
            continue

        if not items:
            break

        if indent_of(line) <= indent and starts_block(lines, i):
            break

        # A continuation of the current item: indented under it, or written
        # flush left on the next line, which markdown calls lazy and these
        # files do not do — but a paragraph that wraps is the same shape.
        items[-1].append(line.lstrip() if indent_of(line) <= indent else line)
        i += 1

    out = []
    for item in items:
        rendered = blocks(dedent(item), seen)
        # Tight by default: one paragraph in an item is just the text, and so
        # is the line above a nested list. Only an item that genuinely holds
        # several blocks gets <p> around them.
        one = re.fullmatch(r"<p>(.*)</p>", rendered, re.S)
        if one:
            rendered = one.group(1)
        else:
            rendered = re.sub(r"\A<p>(.*?)</p>\n\n(?=<[ou]l>)", r"\1\n",
                              rendered, count=1, flags=re.S)
        out.append("<li>%s</li>" % rendered)

    return "<%s>\n%s\n</%s>" % (kind, "\n".join(out), kind), i


def dedent(item):
    """Take the marker's indent off an item's continuation lines."""
    widths = [indent_of(l) for l in item[1:] if l.strip()]
    n = min(widths) if widths else 0
    return [item[0]] + [l[n:] if l.strip() else "" for l in item[1:]]


def blocks(lines, seen):
    out = []
    i = 0
    while i < len(lines):
        line = lines[i]

        if not line.strip():
            i += 1
            continue

        m = FENCE.match(line)
        if m:
            i += 1
            code = []
            while i < len(lines) and not FENCE.match(lines[i]):
                code.append(lines[i])
                i += 1
            i += 1  # the closing fence, or the end of the file
            # The shell prompt goes in a pseudo-element rather than in the
            # text, so a triple click selects the command and not the `$ `
            # in front of it. site/copy.js copies these lines and skips the
            # output between them.
            rendered = []
            for c in code:
                escaped = html.escape(c)
                stripped = escaped.lstrip()
                if stripped.startswith("$ "):
                    indent = escaped[:len(escaped) - len(stripped)]
                    rendered.append('%s<span class="cmd">%s</span>'
                                    % (indent, stripped[2:]))
                else:
                    rendered.append(escaped)
            out.append("<pre><code>%s</code></pre>" % "\n".join(rendered))
            continue

        m = HEADING.match(line)
        if m:
            level = len(m.group(1))
            text = m.group(2)
            # h1 is the page title and is placed by the template, so a second
            # one in the body would be a second title. There are none.
            if level == 1:
                out.append("<h1>%s</h1>" % inline(text))
            else:
                out.append('<h%d id="%s">%s</h%d>'
                           % (level, slug(text, seen), inline(text), level))
            i += 1
            continue

        if RULE.match(line):
            out.append("<hr>")
            i += 1
            continue

        if line.lstrip().startswith(">"):
            quoted = []
            while i < len(lines) and lines[i].lstrip().startswith(">"):
                quoted.append(re.sub(r"^\s*>\s?", "", lines[i]))
                i += 1
            out.append("<blockquote>\n%s\n</blockquote>"
                       % blocks(quoted, seen))
            continue

        if is_table(lines, i):
            rendered, i = parse_table(lines, i)
            out.append(rendered)
            continue

        if is_marker(line):
            rendered, i = parse_list(lines, i, seen)
            out.append(rendered)
            continue

        para = [line]
        i += 1
        while i < len(lines) and lines[i].strip() and not starts_block(lines, i):
            para.append(lines[i])
            i += 1
        out.append("<p>%s</p>" % inline("\n".join(para).strip()))

    return "\n\n".join(out)


# --- documents ----------------------------------------------------------


def paragraphs(lines):
    """The document's paragraphs, as raw markdown, for title and status."""
    para, fenced = [], False
    for line in lines:
        if FENCE.match(line):
            fenced = not fenced
        if not fenced and not line.strip():
            if para:
                yield para
                para = []
            continue
        para.append(line)
    if para:
        yield para


def read(path):
    """(title, status markdown or None, body lines, description)."""
    lines = path.read_text(encoding="utf-8").split("\n")

    title = ""
    for n, line in enumerate(lines):
        m = HEADING.match(line)
        if m and len(m.group(1)) == 1:
            title = m.group(2)
            lines = lines[:n] + lines[n + 1:]
            break

    status = None
    for para in paragraphs(lines):
        if para[0].startswith("**Status:"):
            status = "\n".join(para)
            break
    if status is not None:
        keep, dropped = [], status.split("\n")
        skip = 0
        for line in lines:
            if skip < len(dropped) and line == dropped[skip]:
                skip += 1
                continue
            keep.append(line)
        lines = keep

    description = ""
    for para in paragraphs(lines):
        if not HEADING.match(para[0]) and not is_marker(para[0]) \
                and not para[0].lstrip().startswith(("|", ">", "```")):
            description = plain("\n".join(para))
            break
    if len(description) > 190:
        description = description[:190].rsplit(" ", 1)[0] + "…"

    return title, status, lines, description


# --- the page -----------------------------------------------------------
#
# The same template as every other page on the site, with one difference: the
# assets are a directory up. Kept as one string rather than assembled from
# parts, because the thing it has to stay identical to is also one string.

PAGE = """<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{title}</title>
<meta name="description" content="{description}">
<link rel="icon" href="../favicon.svg" type="image/svg+xml">
<link rel="stylesheet" href="../style.css">
<meta property="og:title" content="{title}">
<meta property="og:description" content="{description}">
<meta name="theme-color" content="#07090b">
</head>
<body>
<div class="aurora" aria-hidden="true"><i></i><i></i><i></i></div>
<div class="lattice" aria-hidden="true"></div>
<canvas id="field"></canvas>
<div class="grain" aria-hidden="true"></div>

<div class="page">
<nav>
    <a class="mark" href="../index.html"><img class="nav-logo" src="../logo.svg" alt="" width="24" height="24">shrooms</a>
    <a href="../index.html">what &amp; why</a>
    <a href="../install.html">install</a>
    <a href="../guides.html">guides</a>
    <a href="../dev.html" class="here">dev notes</a>
    <span class="spacer"></span>
    <a href="https://github.com/vpavlin/shrooms">github</a>
</nav>

<header class="hero">
<h1>{heading}</h1>
{lead}<div class="cta">
{cta}
</div>
</header>

{body}

<footer>
    <p>
        shrooms is built in the open at
        <a href="https://github.com/vpavlin/shrooms">github.com/vpavlin/shrooms</a>,
        dual licensed Apache-2.0 or MIT.
        Part of the <a href="https://logos.co">Logos</a> ecosystem: it rides on
        Logos Delivery and ships as a Basecamp module.
    </p>
</footer>
</div>

<script src="../field.js"></script>
<script src="../copy.js"></script>
</body>
</html>
"""


def page(title, heading, description, lead, cta, body):
    return PAGE.format(
        title=html.escape(title, quote=True),
        description=html.escape(description, quote=True),
        heading=heading,
        lead=lead,
        cta="\n".join("    " + c for c in cta),
        body=body,
    )


def render_adr(path):
    title, status, lines, description = read(path)
    seen = {}
    body = blocks(lines, seen)

    # The status is the first thing a reader of an ADR wants and the last thing
    # a renderer would put anywhere sensible by default — it is a paragraph in
    # the middle of the prose. Lifted out into the callout the rest of the site
    # already uses for "read this before the section under it".
    lead = ""
    if status:
        lead = '<div class="note">\n<p>%s</p>\n</div>\n' % inline(status)

    return page(
        title="shrooms — %s" % title,
        heading=inline(title),
        description=description,
        lead=lead,
        cta=['<a href="index.html">← every decision</a>',
             '<a href="../dev.html">dev notes</a>'],
        body=body,
    )


def render_index(path):
    title, _, lines, description = read(path)
    seen = {}
    return page(
        title="shrooms — %s" % title,
        heading=inline(title),
        description=description,
        lead="",
        cta=['<a href="../dev.html">← dev notes</a>',
             '<a href="https://github.com/vpavlin/shrooms/tree/master/docs/adr">the markdown</a>'],
        body=blocks(lines, seen),
    )


def main():
    if not SRC.is_dir():
        sys.exit("no %s — run this from the repository" % SRC)

    OUT.mkdir(parents=True, exist_ok=True)

    written = {}
    for path in sorted(SRC.glob("*.md")):
        if path.name == "README.md":
            written["index.html"] = render_index(path)
        else:
            written[path.stem + ".html"] = render_adr(path)

    for name, text in sorted(written.items()):
        target = OUT / name
        # Idempotent: the same input writes the same bytes, and an unchanged
        # page is left with its mtime alone so a rebuild is not a change.
        if target.exists() and target.read_text(encoding="utf-8") == text:
            print("  unchanged  site/adr/%s" % name)
            continue
        target.write_text(text, encoding="utf-8")
        print("  wrote      site/adr/%s" % name)

    # Anything left over is a record that was renamed or deleted. Only .html in
    # this directory, which nothing but this script writes.
    for stale in sorted(OUT.glob("*.html")):
        if stale.name not in written:
            stale.unlink()
            print("  removed    site/adr/%s (no longer in docs/adr)" % stale.name)

    print("%d pages in site/adr/ (%d records and the index)"
          % (len(written), len(written) - 1))


if __name__ == "__main__":
    main()
