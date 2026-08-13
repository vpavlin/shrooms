// Mycelium, growing.
//
// The app draws a spore field while a device is joining a mesh, and the mark is
// a mushroom, and the whole conceit is that a mesh is grown rather than
// configured. So the site does not decorate with a spore field — it runs one,
// properly: spores drift, hyphae reach out between the ones that come close,
// thicken while they hold and wither when they part, and the colour breathes
// through the palette rather than sitting on one note.
//
// Three rules, all of them learned somewhere else in this project:
//
//   Whole cycles only. The phase runs 0..2π and restarts, so anything that is
//   not an integer multiple of it lands somewhere else when it wraps. On the
//   phone that produced a teleport every nineteen seconds which read as a
//   rendering bug rather than as the animation's own doing.
//
//   Decoration must never cost a reader anything. Fixed counts, no per-frame
//   allocation, additive blending done by the compositor rather than by us, and
//   the whole thing skipped for anybody who has asked for reduced motion.
//
//   It sits behind text that has to stay legible. Everything here is drawn at
//   low alpha under a page that owns its own background; the psychedelia is in
//   the movement and the colour, not in the contrast.

(function () {
    const canvas = document.getElementById("field");
    if (!canvas) return;
    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) return;

    const ctx = canvas.getContext("2d", { alpha: true });

    // The palette, as triples so alpha can vary per stroke. Status colours are
    // deliberately absent: green means connected everywhere else in this
    // project, and a background that flashes it means nothing at all.
    const HUES = [
        [53, 240, 160],   // phosphor
        [154, 123, 255],  // violet
        [90, 169, 255],   // sky
        [255, 111, 181],  // blossom
        [200, 230, 74],   // chartreuse
    ];

    // A fixed seed: the same growth on every visit, so the page has a face
    // rather than being different noise each time.
    let seed = 0x5eed;
    const rnd = () => {
        seed ^= seed << 13; seed ^= seed >>> 17; seed ^= seed << 5;
        return ((seed >>> 0) % 1e5) / 1e5;
    };

    const small = window.innerWidth < 700;
    const COUNT = small ? 16 : 34;

    const spores = Array.from({ length: COUNT }, () => ({
        x: rnd(),
        y: rnd(),
        r: 0.04 + rnd() * 0.13,          // how far it wanders
        phase: rnd() * Math.PI * 2,
        kx: 1 + Math.floor(rnd() * 2),   // integer harmonics: see above
        ky: 2 + Math.floor(rnd() * 2),
        size: 1.3 + rnd() * 3.2,
        hue: Math.floor(rnd() * HUES.length),
        // How fast this one's own pulse runs, in whole cycles again.
        beat: 1 + Math.floor(rnd() * 3),
    }));

    let w = 0, h = 0;
    function resize() {
        const dpr = Math.min(window.devicePixelRatio || 1, 2);
        w = window.innerWidth;
        h = window.innerHeight;
        canvas.width = Math.floor(w * dpr);
        canvas.height = Math.floor(h * dpr);
        canvas.style.width = w + "px";
        canvas.style.height = h + "px";
        ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    }
    resize();
    window.addEventListener("resize", resize, { passive: true });

    // --- the fractal ---------------------------------------------------
    //
    // Triangles inside triangles, which is the shape people describe most
    // often after the honeycomb, and the one that is genuinely ours as well:
    // a Sierpinski gasket is a mesh subdividing. Every vertex is a node and
    // every edge a link, so what is drawn here is the same figure the graph
    // draws, taken to the depth a graph never goes.
    //
    // Depth breathes rather than sits: the innermost level fades in and out on
    // its own whole cycle, so the thing looks like it keeps subdividing
    // forever without ever costing more than five levels of drawing.
    // Five levels is 243 triangles a frame, which a laptop does not notice and
    // a phone does. Four is 81 and looks the same at that size.
    const DEPTH = small ? 4 : 5;

    // A continuous walk through the palette rather than a step from one colour
    // to the next. Stepping was visible as an abrupt change every few seconds,
    // which reads as a repaint rather than as colour moving.
    function walk(offset, t) {
        const x = offset + (t / (Math.PI * 2)) * HUES.length;
        const i = Math.floor(x) % HUES.length;
        const j = (i + 1) % HUES.length;
        const f = x - Math.floor(x);
        return [
            HUES[i][0] + (HUES[j][0] - HUES[i][0]) * f,
            HUES[i][1] + (HUES[j][1] - HUES[i][1]) * f,
            HUES[i][2] + (HUES[j][2] - HUES[i][2]) * f,
        ];
    }

    function gasket(ax, ay, bx, by, cx, cy, depth, t, rot) {
        if (depth === 0) return;

        // Each level sits one step further along the palette, and the whole
        // thing walks continuously, so the figure is vivid without any one hue
        // owning it and without a visible step when it changes.
        const rgb = walk(DEPTH - depth, t);

        // The deepest level breathes; the rest hold, or the whole figure would
        // pulse in unison and look like a heartbeat rather than a fractal.
        let alpha = 0.06 + 0.04 * (depth / DEPTH);
        if (depth === 1) alpha *= 0.35 + 0.65 * (0.5 + 0.5 * Math.sin(2 * t));

        ctx.strokeStyle = `rgba(${rgb[0] | 0}, ${rgb[1] | 0}, ${rgb[2] | 0}, ${alpha})`;
        ctx.lineWidth = 0.4 + depth * 0.22;
        ctx.beginPath();
        ctx.moveTo(ax, ay);
        ctx.lineTo(bx, by);
        ctx.lineTo(cx, cy);
        ctx.closePath();
        ctx.stroke();

        // The three midpoints are where a mesh would put its next nodes.
        const abx = (ax + bx) / 2, aby = (ay + by) / 2;
        const bcx = (bx + cx) / 2, bcy = (by + cy) / 2;
        const cax = (cx + ax) / 2, cay = (cy + ay) / 2;

        gasket(ax, ay, abx, aby, cax, cay, depth - 1, t, rot);
        gasket(abx, aby, bx, by, bcx, bcy, depth - 1, t, rot);
        gasket(cax, cay, bcx, bcy, cx, cy, depth - 1, t, rot);
    }

    function fractal(t) {
        const r = Math.min(w, h) * (small ? 0.42 : 0.34);
        const cx = w * 0.5, cy = h * 0.46;
        // One rotation per cycle: a whole number, like everything else here,
        // so the wrap is invisible.
        const rot = t;

        const v = [];
        for (let i = 0; i < 3; i++) {
            const a = rot + (i * 2 * Math.PI) / 3 - Math.PI / 2;
            v.push(cx + r * Math.cos(a), cy + r * Math.sin(a));
        }
        gasket(v[0], v[1], v[2], v[3], v[4], v[5], DEPTH, t, rot);
    }

    const PERIOD = 23000;               // one full cycle of everything
    const pts = spores.map(() => ({ x: 0, y: 0 }));

    // A hypha is drawn as a curve rather than a line, because nothing that
    // grows goes straight. The control point bows perpendicular to the pair,
    // and the bow itself breathes — one whole cycle, so it closes.
    function hypha(a, b, bow, alpha, rgb, width) {
        const mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2;
        const dx = b.x - a.x, dy = b.y - a.y;
        ctx.strokeStyle = `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${alpha})`;
        ctx.lineWidth = width;
        ctx.beginPath();
        ctx.moveTo(a.x, a.y);
        ctx.quadraticCurveTo(mx - dy * bow, my + dx * bow, b.x, b.y);
        ctx.stroke();
    }

    function frame(ms) {
        const t = ((ms % PERIOD) / PERIOD) * Math.PI * 2;
        ctx.clearRect(0, 0, w, h);

        for (let i = 0; i < spores.length; i++) {
            const s = spores[i];
            pts[i].x = (s.x + s.r * Math.cos(s.kx * t + s.phase)) * w;
            pts[i].y = (s.y + s.r * Math.sin(s.ky * t + s.phase)) * h;
        }

        // Additive, so crossing hyphae brighten where they meet the way light
        // does. This is the single thing that makes it look alive rather than
        // drawn.
        ctx.globalCompositeOperation = "lighter";

        // The fractal underneath everything, turning slowly.
        fractal(t);

        const near = Math.min(w, h) * (small ? 0.34 : 0.28);
        for (let i = 0; i < pts.length; i++) {
            for (let j = i + 1; j < pts.length; j++) {
                const d = Math.hypot(pts[i].x - pts[j].x, pts[i].y - pts[j].y);
                if (d > near) continue;
                // Strongest as the pair passes closest, which is what makes the
                // field read as connecting rather than merely moving.
                const k = 1 - d / near;
                const rgb = HUES[(spores[i].hue + spores[j].hue) % HUES.length];
                const bow = 0.10 * Math.sin(t + i + j);
                hypha(pts[i], pts[j], bow, 0.05 + 0.22 * k * k, rgb, 0.6 + 1.8 * k);
            }
        }

        for (let i = 0; i < pts.length; i++) {
            const s = spores[i];
            const rgb = HUES[s.hue];
            // Each spore breathes on its own whole-cycle beat, so the field
            // shimmers instead of pulsing in unison like a warning light.
            const pulse = 0.55 + 0.45 * Math.sin(s.beat * t + s.phase);
            const r = s.size * (0.85 + 0.5 * pulse);

            const glow = ctx.createRadialGradient(pts[i].x, pts[i].y, 0, pts[i].x, pts[i].y, r * 9);
            glow.addColorStop(0, `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${0.16 * pulse})`);
            glow.addColorStop(1, `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, 0)`);
            ctx.fillStyle = glow;
            ctx.beginPath();
            ctx.arc(pts[i].x, pts[i].y, r * 9, 0, Math.PI * 2);
            ctx.fill();

            ctx.fillStyle = `rgba(${rgb[0]}, ${rgb[1]}, ${rgb[2]}, ${0.35 + 0.35 * pulse})`;
            ctx.beginPath();
            ctx.arc(pts[i].x, pts[i].y, r, 0, Math.PI * 2);
            ctx.fill();
        }

        ctx.globalCompositeOperation = "source-over";
        requestAnimationFrame(frame);
    }
    requestAnimationFrame(frame);
})();
