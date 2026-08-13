#!/usr/bin/env python3
"""Generate the Shrooms mark: fruiting bodies above ground, mycelium below.

The first version put the mesh *inside the cap*, as gills. It looked good and
it said the wrong thing twice.

Mycelium is not in the mushroom. It is the network under the soil, and a
mushroom is only its fruiting body — the part that pushes up where you can see
it. Which is exactly this project's architecture: your devices are what you
look at, and the mesh is the thing connecting them that you never see. So the
mark now draws the ground: mushrooms above it, a network of hyphae below,
every stem rooted in a node of that network.

The mushrooms are also shaped like the ones the project is named after. A
psilocybe has a slender stem and a tall bell or conical cap, often with a small
point at the apex — not the wide flat saucer of a storybook toadstool. And the
bruising is real: psilocybin mushrooms stain blue where handled, which is why
the stems go violet at the base. That violet is already in the palette, where
it means "relayed", so the mark uses it for the part that touches the network.

One geometry, three outputs — a PNG with real glow, an Android VectorDrawable
for the adaptive icon, and an SVG for documents and the website. Generated
rather than drawn so the launcher icon, the package icon, the favicon and the
website cannot drift apart, which is exactly what happens when someone edits
one of four exported files.

    python3 assets/logo.py

Deterministic: fixed seeds, so regenerating produces the same mark. A logo that
reshuffles itself on every build is not a logo.
"""
import math
import os
import random
import xml.sax.saxutils as sx

from PIL import Image, ImageDraw, ImageFilter

# Adaptive-icon geometry: a 108-unit viewport whose middle 72 units are the
# safe zone. Everything meaningful stays inside that circle or the launcher
# will crop it.
VIEW = 108.0
CX = 54.0

# How far the adaptive launcher foreground is scaled down so the drawing lands
# in the 72-unit safe zone. Nothing else is scaled: the SVG, the site logo and
# the legacy icons are square and uncropped, and sizing the mark for the one
# consumer that crops would waste the other three.
SAFE = 72.0 / 108.0

# The soil line. High enough that the mycelium gets real room — it is the
# subject as much as the mushrooms are — and low enough that the caps are not
# crammed against the top of the safe zone.
HORIZON = 58.0

VOID = (7, 9, 11)
EARTH = (26, 20, 15)          # under the soil, barely lighter than the void
EARTH_LINE = (58, 42, 31)     # the soil line itself
# Tan, because that is what these mushrooms are, and muted because phosphor is
# the only colour in this project allowed to shout.
# The caps.
#
# Tan was tried first, on the grounds that it is what these mushrooms are, and
# it was the only warm colour in a mark made otherwise of phosphor, violet and
# bone — it read as belonging to a different drawing. Violet is what the spores
# already are, what "relayed" is everywhere else in this project, and the
# colour phosphor is paired with on both front-ends. Two colours, both of them
# already meaning something.
#
# One per fruiting body, so the alternative is one line: give them the three
# mesh tints the graph uses to group peers —
#
#   CAPS = [(154, 123, 255), (90, 169, 255), (255, 111, 181)]
#
# which says "three devices, three meshes" and is more vivid at the cost of
# being three colours in a logo.
CAPS = [(154, 123, 255), (154, 123, 255), (154, 123, 255)]
PHOSPHOR = (53, 240, 160)     # the living network
VIOLET = (154, 123, 255)      # spores, and "relayed" everywhere else
BONE = (214, 221, 227)

# Three fruiting bodies, and none of them standing to attention.
#
# The first version of this was read as "a castle with three towers", which was
# fair and is worth recording because every part of it was a decision:
#
#   Straight vertical stems of even width are columns. Real ones bend, so these
#   bend — each has a sway as well as a lean, and no two the same.
#
#   A cone is a tower roof. The cap profile is a dome now, widest low down,
#   with a rim that curls under; the overhang is the thing that says mushroom,
#   and a straight-sided cap has none.
#
#   Tall-in-the-middle-flanked-by-two is a castle silhouette before it is
#   anything else. They cluster off-centre instead, at three different heights,
#   with one leaning across another.
#
#   x, cap half-width, cap height, stem height, lean, sway
SHROOMS = [
    (47.0, 15.5, 15.0, 32.0, 0.26, -0.26),
    (82.0, 11.0, 10.5, 21.0, -0.30, 0.28),
    (25.0, 8.6, 8.0, 13.0, 0.20, 0.32),
]


def stem_axis(x0, stem_h, lean, sway, y):
    """Where a stem's centre line is at height y.

    One function, used by the stem outline and by whatever sits on top of it.
    They were two expressions that had to agree, and the day they stopped
    agreeing a cap ended up beside its stem rather than on it.
    """
    top = HORIZON - stem_h
    t = 0.0 if stem_h <= 0 else (y - top) / (HORIZON - top)
    t = max(0.0, min(1.0, t))
    # Lean tilts the top away from the foot; sway bows the middle, which is
    # what stops it reading as a drawn line rather than a grown thing.
    return (x0
            + lean * (1.0 - t) * stem_h * 0.55
            + sway * math.sin(math.pi * t) * stem_h * 0.34)

def curve(a, b, bend, n=10):
    """A gently bowed line from a to b, as a polyline.

    Everything that connects two points in this mark is drawn with this, which
    is the point: the stems curved and the links were dead straight, so the two
    read as different kinds of object and every stem visibly broke where it
    entered the soil.

    It is also what both front-ends do — the graph draws its links as
    quadratic curves "because mycelium does not grow in straight lines" — so
    the mark and the app now bow their connections the same way.
    """
    mx, my = (a[0] + b[0]) / 2.0, (a[1] + b[1]) / 2.0
    dx, dy = b[0] - a[0], b[1] - a[1]
    cx, cy = mx - dy * bend, my + dx * bend
    pts = []
    for i in range(n + 1):
        t = i / n
        u = 1.0 - t
        pts.append((u * u * a[0] + 2 * u * t * cx + t * t * b[0],
                    u * u * a[1] + 2 * u * t * cy + t * t * b[1]))
    return pts


def gasket(a, b, c, depth, out):
    """Collect the inverted triangles of a Sierpinski gasket.

    The holes rather than the filled parts, because that is what gets drawn:
    one triangle in the cap's colour with these punched out of it. Three levels
    is 13 holes, which is as much as survives being 20 pixels wide.
    """
    if depth <= 0:
        return
    ab = ((a[0] + b[0]) / 2.0, (a[1] + b[1]) / 2.0)
    bc = ((b[0] + c[0]) / 2.0, (b[1] + c[1]) / 2.0)
    ca = ((c[0] + a[0]) / 2.0, (c[1] + a[1]) / 2.0)
    out.append([ab, bc, ca])
    gasket(a, ab, ca, depth - 1, out)
    gasket(ab, b, bc, depth - 1, out)
    gasket(ca, bc, c, depth - 1, out)


def cap_triangles(x0, w, h, base_y, depth):
    """A cap as a triangle with a gasket in it.

    Two things this project already had, put together. The site draws a
    Sierpinski gasket because triangles and fractals are what people actually
    report seeing, and because a gasket is a mesh subdividing — every vertex a
    node, every edge a link. The graph on both front-ends draws the same
    figure, one level deep.

    So the cap is that figure rather than a picture of a mushroom cap. It reads
    as a mushroom because of what is under it, which is how a mark works: the
    silhouette carries the meaning and the detail carries the character.

    Returns the outer triangle and the holes to punch out of it.
    """
    apex = (x0, base_y - h)
    left = (x0 - w, base_y)
    right = (x0 + w, base_y)
    holes = []
    gasket(apex, left, right, depth, holes)
    return [apex, left, right], holes


def stem_line(x0, stem_h, lean, sway, top_y, node, n=14):
    """The stem, from under the cap all the way down to its node.

    One line, not two. It used to stop at the soil and a separate straight
    segment ran from there to the nearest node, which put a visible kink at
    exactly the place the mark is about — where what grew meets what grew it.

    Below the soil it continues as a curve that leaves the foot going the way
    the stem was already going, so there is no corner at the surface either.
    """
    pts = []
    for i in range(n + 1):
        t = i / n
        y = top_y + (HORIZON - top_y) * t
        pts.append((stem_axis(x0, stem_h, lean, sway, y), y))

    # The descent. Its control point sits along the direction the stem was
    # already travelling, which is what makes the join smooth: a quadratic
    # leaves its start heading straight at its control point, so putting that
    # point on the incoming tangent means the curve continues the stem rather
    # than setting off somewhere of its own.
    #
    # Bowing perpendicular to the chord — which is what this did — happens to
    # look right when the node lies the way the stem was already leaning, and
    # visibly kinks at the soil when it does not. Two of the three did not.
    foot, prev = pts[-1], pts[-2]
    tx, ty = foot[0] - prev[0], foot[1] - prev[1]
    tl = math.hypot(tx, ty) or 1.0
    tx, ty = tx / tl, ty / tl

    dx, dy = node[0] - foot[0], node[1] - foot[1]
    dl = math.hypot(dx, dy) or 1.0
    # How far along that tangent to reach. Shortened when the node lies against
    # the stem's direction, or the curve overshoots and comes back — which is a
    # different ugly shape, not a fix for this one.
    align = max(0.15, (tx * dx + ty * dy) / dl)
    cx, cy = foot[0] + tx * dl * 0.55 * align, foot[1] + ty * dl * 0.55 * align

    for i in range(1, 13):
        t = i / 12
        u = 1.0 - t
        pts.append((u * u * foot[0] + 2 * u * t * cx + t * t * node[0],
                    u * u * foot[1] + 2 * u * t * cy + t * t * node[1]))
    return pts


def exit_tangent(x0, stem_h, lean, sway, eps=0.6):
    """The direction a stem is travelling as it reaches the soil."""
    a = stem_axis(x0, stem_h, lean, sway, HORIZON - eps)
    b = stem_axis(x0, stem_h, lean, sway, HORIZON)
    d = math.hypot(b - a, eps) or 1.0
    return ((b - a) / d, eps / d)


def pick_node(foot, tan, nodes):
    """Which node a stem joins: near, and in front of it.

    Nearest alone is not enough, and this is the whole of why two of the three
    stems had a corner at the soil. A stem leaving the surface heading left,
    attached to the nearest node — which happened to be to its right — has to
    turn around to get there, and no amount of care at the join can hide a
    reversal. Weighting by alignment picks a node the stem is already going
    towards, and then the curve simply continues.

    A node twice as far but dead ahead beats one close behind, which is the
    right trade for a drawing: the line is prettier and the graph is no less
    true, since which node a stem happens to touch means nothing.
    """
    def score(n):
        dx, dy = n[0] - foot[0], n[1] - foot[1]
        d = math.hypot(dx, dy) or 1e-6
        # Sideways agreement only. Every node is below the foot, so a dot
        # product against the whole tangent is dominated by the downward
        # component and calls everything "ahead" — which is why the first
        # version of this changed nothing. What makes a stem double back is
        # horizontal, so that is what is measured.
        turn = 0.0
        if tan[0] * (dx / d) < 0.0:
            turn = abs(tan[0])
        return d * (1.0 + 1.4 * turn)
    return min(nodes, key=score)


def mycelium():
    """The network under the soil: nodes and the links between them.

    Spread across the whole width and down to the bottom edge, because the
    drawing sat in the middle of its square with a band of nothing above and
    below it. The adaptive launcher icon is the one output that cannot take
    that, and it gets a scale transform instead of the whole mark being sized
    for the strictest consumer.

    Links come back as bowed polylines rather than pairs of endpoints, so they
    are the same kind of thing as a stem and can be drawn by the same code.
    """
    nodes = [
        (10.0, 74.0), (24.0, 86.0), (40.0, 95.0), (58.0, 97.0),
        (76.0, 90.0), (95.0, 79.0), (33.0, 72.0), (69.0, 74.0),
    ]
    pairs = [
        (0, 1), (1, 2), (2, 3), (3, 4), (4, 5),
        (0, 6), (6, 2), (6, 3), (3, 7), (7, 4), (7, 5),
    ]
    # Alternating bows, so the field looks grown rather than combed one way.
    links = [curve(nodes[i], nodes[j], 0.055 if (k % 2) else -0.055)
             for k, (i, j) in enumerate(pairs)]
    return nodes, links


def spores(n=6):
    """Spores drifting off the tallest cap — how a mesh gains a member."""
    rnd = random.Random(7)
    x0, w, h, sh, lean, sway = SHROOMS[0]
    x0 = stem_axis(x0, sh, lean, sway, HORIZON - sh)
    top = HORIZON - sh - h
    return [
        (x0 + rnd.uniform(-1.4, 2.6) * w * 0.8,
         top + rnd.uniform(-9.0, 5.0),
         rnd.uniform(0.8, 1.7))
        for _ in range(n)
    ]


# At 48 pixels the full drawing turns to mush: three thin stems become three
# scratches and fourteen nodes become a green smear. So small renders get a
# simplified version of the same idea — one fruiting body, four nodes — rather
# than a second drawing that would drift from this one.
# Chunkier than the full drawing on purpose: at this size a tall thin stem
# under a narrow cap reads as an exclamation mark, not a mushroom.
SHROOMS_SMALL = [(54.0, 30.0, 26.0, 22.0, 0.0, 0.0)]
NODES_SMALL = [(14.0, 72.0), (38.0, 92.0), (70.0, 90.0), (94.0, 68.0)]
EDGES_SMALL = [(0, 1), (1, 2), (2, 3), (0, 2)]


def _geometry(simple=False):
    """Everything the three renderers draw, computed once.

    Two kinds of thing come out of here now: filled shapes (the caps and their
    holes) and polylines (the stems and the links). Everything that connects is
    a polyline and is drawn the same way, which is what stops the stems and the
    network reading as two different drawings sharing a square.
    """
    shrooms = SHROOMS_SMALL if simple else SHROOMS
    if simple:
        nodes = NODES_SMALL
        links = [curve(nodes[i], nodes[j], 0.06 if (k % 2) else -0.06)
                 for k, (i, j) in enumerate(EDGES_SMALL)]
    else:
        nodes, links = mycelium()

    caps, stems = [], []
    for k, (x0, w, h, sh, lean, sway) in enumerate(shrooms):
        base = HORIZON - sh
        # The tallest gets three levels, the others two and one. Depth costs
        # nothing to draw and everything to read: a small cap subdivided three
        # times is a smudge, and all three at one depth looks printed.
        depth = 2 if simple else (3, 2, 1)[min(k, 2)]
        caps.append(cap_triangles(stem_axis(x0, sh, lean, sway, base), w, h, base, depth))
        foot = (stem_axis(x0, sh, lean, sway, HORIZON), HORIZON)
        stems.append(stem_line(x0, sh, lean, sway, base - h * 0.02,
                               pick_node(foot, exit_tangent(x0, sh, lean, sway), nodes)))

    return caps, stems, nodes, links


def _poly(points):
    """A polygon as SVG/VectorDrawable path data."""
    d = "M %.2f %.2f " % points[0]
    d += " ".join("L %.2f %.2f" % p for p in points[1:])
    return d + " Z"


def _lines(polylines):
    """Polylines as path data, for everything that connects two points."""
    out = []
    for pts in polylines:
        out.append("M %.2f %.2f " % pts[0]
                   + " ".join("L %.2f %.2f" % p for p in pts[1:]))
    return " ".join(out)


# --- PNG ---------------------------------------------------------------------

SMALL = 128


def render_png(path, size, background=True):
    big = size >= SMALL
    caps, stems, nodes, links = _geometry(simple=not big)
    s = size / VIEW

    img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    d = ImageDraw.Draw(img)

    def P(x, y):
        return (x * s, y * s)

    def poly(points, fill):
        d.polygon([P(*p) for p in points], fill=fill)

    if background:
        d.rounded_rectangle([0, 0, size - 1, size - 1], radius=size * 0.22, fill=VOID)
        # The soil, as a wash rather than a hard block: a mark with a visible
        # rectangle in it stops being a mark.
        d.rectangle([0, HORIZON * s, size, size], fill=EARTH)

    # Everything that glows is drawn twice: once wide and blurred, once thin
    # and bright. Both the links below the soil and the stems above it, because
    # a stem is a connection too — it joins what the network grew to the
    # network — and drawing it in a different idiom is what made the old mark
    # look like a picture beside the app instead of part of it.
    glow = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    gd = ImageDraw.Draw(glow)
    width = max(1, int(size * (0.022 if big else 0.045)))
    for pts in links + stems:
        gd.line([P(*p) for p in pts], fill=PHOSPHOR + (155,), width=width, joint="curve")
    glow = glow.filter(ImageFilter.GaussianBlur(size * 0.016))
    img.alpha_composite(glow)

    thin = max(2, int(size * (0.008 if big else 0.022)))
    for pts in links:
        d.line([P(*p) for p in pts], fill=PHOSPHOR + (235,), width=thin, joint="curve")

    # The soil line under the stems and over the links, so the ground reads as
    # a surface that the stems pass through rather than stand on.
    d.line([P(4, HORIZON), P(VIEW - 4, HORIZON)],
           fill=EARTH_LINE, width=max(1, int(size * 0.012)))

    stem_w = max(2, int(size * (0.011 if big else 0.026)))
    for pts in stems:
        d.line([P(*p) for p in pts], fill=PHOSPHOR + (255,),
               width=stem_w, joint="curve")

    # Nodes after the stems, because a stem ends at one and would otherwise be
    # drawn across it — the join is the point of the whole mark.
    r = size * (0.017 if big else 0.045)
    for x, y in nodes:
        cx, cy = P(x, y)
        d.ellipse([cx - r, cy - r, cx + r, cy + r], fill=BONE)

    # The caps last. Tallest drawn last so it sits in front.
    for k in ([0] if len(caps) == 1 else [1, 2, 0]):
        # The cap: one triangle with a gasket punched out of it. The holes are
        # filled with the background rather than a darker tint, so the figure
        # is the same subtraction the site draws and not a decoration on top.
        poly(caps[k][0], CAPS[k % len(CAPS)])
        for hole in caps[k][1]:
            poly(hole, VOID)

    if big:
        for x, y, rr in spores():
            cx, cy = P(x, y)
            rr = rr * s
            d.ellipse([cx - rr, cy - rr, cx + rr, cy + rr], fill=VIOLET + (220,))

    img.save(path)
    return path


# --- Android VectorDrawable --------------------------------------------------

def render_vector(path):
    caps, stems, nodes, links = _geometry()

    def hexa(c):
        return "#%02X%02X%02X" % c

    parts = ['<?xml version="1.0" encoding="utf-8"?>',
             "<!-- Generated by assets/logo.py. Do not edit by hand; edit the generator. -->",
             '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
             '    android:width="108dp" android:height="108dp"',
             '    android:viewportWidth="108" android:viewportHeight="108">',
             ""]
    # Everything goes inside one group, scaled about the centre so the whole
    # drawing lands inside the adaptive icon's safe zone. This file is the
    # launcher foreground and a launcher may mask anything outside the middle
    # 72 of 108, while the SVG and the PNGs are not cropped at all and want the
    # full square. One drawing, one transform, rather than a composition sized
    # for the tightest crop and loose everywhere else.
    #
    # The soil is inside it too. It was outside, which left the horizon at full
    # size while everything that meets it shrank — a mark whose ground line
    # does not touch its stems.
    parts.append('    <group android:pivotX="54" android:pivotY="54" '
                 f'android:scaleX="{SAFE:.3f}" android:scaleY="{SAFE:.3f}">')
    parts.append("    <!-- the soil -->")
    parts.append(f'    <path android:pathData="M -20 {HORIZON} L 128 {HORIZON} '
                 f'L 128 148 L -20 148 Z" android:fillColor="{hexa(EARTH)}"/>')
    parts.append("")
    parts.append("    <!-- mycelium: the mesh, where mycelium actually lives -->")

    hyphae = _lines(links)
    parts.append(f'    <path android:pathData="{hyphae}" android:strokeColor="{hexa(PHOSPHOR)}" '
                 f'android:strokeWidth="2.6" android:strokeAlpha="0.35" android:strokeLineCap="round"/>')
    parts.append(f'    <path android:pathData="{hyphae}" android:strokeColor="{hexa(PHOSPHOR)}" '
                 f'android:strokeWidth="1.1" android:strokeLineCap="round"/>')
    for x, y in nodes:
        parts.append(f'    <path android:pathData="M {x:.2f} {y - 1.55:.2f} '
                     f'a 1.55 1.55 0 1 0 0.01 0 Z" android:fillColor="{hexa(BONE)}"/>')

    parts.append("")
    parts.append(f'    <path android:pathData="M 4 {HORIZON} L 104 {HORIZON}" '
                 f'android:strokeColor="{hexa(EARTH_LINE)}" android:strokeWidth="1.4"/>')
    parts.append("")
    parts.append("    <!-- fruiting bodies: what the network grew -->")
    parts.append("")
    # The stems, as strokes rather than filled shapes, so they read as the same
    # kind of thing as the links below them.
    for k in (1, 2, 0):
        stem = _lines([stems[k]])
        parts.append(f'    <path android:pathData="{stem}" '
                     f'android:strokeColor="{hexa(PHOSPHOR)}" android:strokeWidth="2.6" '
                     f'android:strokeAlpha="0.35" android:strokeLineCap="round"/>')
        parts.append(f'    <path android:pathData="{stem}" '
                     f'android:strokeColor="{hexa(PHOSPHOR)}" android:strokeWidth="1.3" '
                     f'android:strokeLineCap="round"/>')
        parts.append(f'    <path android:pathData="{_poly(caps[k][0])}" '
                     f'android:fillColor="{hexa(CAPS[k % len(CAPS)])}"/>')
        # The gasket, punched out in the background colour. A vector drawable
        # has no even-odd subtraction worth relying on across API levels, so
        # the holes are drawn as shapes in the colour behind them — which is
        # flat here, above the soil, and therefore exact.
        for hole in caps[k][1]:
            parts.append(f'    <path android:pathData="{_poly(hole)}" '
                         f'android:fillColor="{hexa(VOID)}"/>')

    parts.append("")
    parts.append("    <!-- spores: how a mesh gains a member -->")
    for x, y, r in spores():
        parts.append(f'    <path android:pathData="M {x:.2f} {y - r:.2f} '
                     f'a {r:.2f} {r:.2f} 0 1 0 0.01 0 Z" '
                     f'android:fillColor="{hexa(VIOLET)}"/>')
    parts.append("    </group>")
    parts.append("</vector>")

    with open(path, "w") as f:
        f.write("\n".join(parts) + "\n")
    return path


# --- SVG ---------------------------------------------------------------------

def render_svg(path):
    caps, stems, nodes, links = _geometry()

    def hexa(c):
        return "#%02X%02X%02X" % c

    hyphae = _lines(links)

    parts = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 108 108" '
             'width="512" height="512">',
             '<defs><filter id="g" x="-50%" y="-50%" width="200%" height="200%">'
             '<feGaussianBlur stdDeviation="1.8"/></filter></defs>',
             f'<rect width="108" height="108" rx="24" fill="{hexa(VOID)}"/>',
             f'<path d="M 0 {HORIZON} L 108 {HORIZON} L 108 108 L 0 108 Z" fill="{hexa(EARTH)}"/>',
             f'<g filter="url(#g)" opacity="0.85"><path d="{sx.escape(hyphae)}" '
             f'stroke="{hexa(PHOSPHOR)}" stroke-width="2.6" fill="none" stroke-linecap="round"/></g>',
             f'<path d="{sx.escape(hyphae)}" stroke="{hexa(PHOSPHOR)}" stroke-width="1.0" '
             f'fill="none" stroke-linecap="round"/>']
    for x, y in nodes:
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="1.55" fill="{hexa(BONE)}"/>')
    parts.append(f'<path d="M 4 {HORIZON} L 104 {HORIZON}" stroke="{hexa(EARTH_LINE)}" '
                 f'stroke-width="1.4" stroke-linecap="round"/>')
    for k in (1, 2, 0):
        stem = _lines([stems[k]])
        parts.append(f'<g filter="url(#g)" opacity="0.85"><path d="{sx.escape(stem)}" '
                     f'stroke="{hexa(PHOSPHOR)}" stroke-width="2.6" fill="none" '
                     f'stroke-linecap="round"/></g>')
        parts.append(f'<path d="{sx.escape(stem)}" stroke="{hexa(PHOSPHOR)}" '
                     f'stroke-width="1.2" fill="none" stroke-linecap="round"/>')
        parts.append(f'<path d="{sx.escape(_poly(caps[k][0]))}" fill="{hexa(CAPS[k % len(CAPS)])}"/>')
        for hole in caps[k][1]:
            parts.append(f'<path d="{sx.escape(_poly(hole))}" fill="{hexa(VOID)}"/>')

    for x, y, r in spores():
        parts.append(f'<circle cx="{x:.2f}" cy="{y:.2f}" r="{r:.2f}" '
                     f'fill="{hexa(VIOLET)}"/>')
    parts.append("</svg>")

    with open(path, "w") as f:
        f.write("\n".join(parts))
    return path


if __name__ == "__main__":
    here = os.path.dirname(os.path.abspath(__file__))
    root = os.path.dirname(here)
    res = os.path.join(root, "android/app/src/main/res")

    render_svg(os.path.join(here, "logo.svg"))
    render_vector(os.path.join(res, "drawable/ic_mesh.xml"))

    # The website uses the same mark rather than an approximation of it. It
    # gets both the file and the favicon, because a site that draws its own
    # mushroom drifts from the app's within a week — which is exactly what
    # happened before this line existed.
    site = os.path.join(root, "site")
    if os.path.isdir(site):
        render_svg(os.path.join(site, "logo.svg"))
        render_svg(os.path.join(site, "favicon.svg"))

    # Legacy launcher bitmaps, the Basecamp package icon, and a big one for
    # F-Droid, which cannot extract an adaptive icon from an APK.
    for dpi, px in [("mdpi", 48), ("hdpi", 72), ("xhdpi", 96), ("xxxhdpi", 192)]:
        d = os.path.join(res, f"mipmap-{dpi}")
        os.makedirs(d, exist_ok=True)
        render_png(os.path.join(d, "ic_launcher.png"), px)
        render_png(os.path.join(d, "ic_launcher_round.png"), px)
    render_png(os.path.join(root, "basecamp/icon.png"), 512)
    render_png(os.path.join(here, "logo.png"), 512)
    print("wrote the mark: svg, vector drawable, mipmaps, basecamp icon, site")
