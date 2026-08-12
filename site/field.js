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
