package xyz.vpavlin.shrooms

import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.unit.dp
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.hypot
import kotlin.math.min
import kotlin.math.sin
import kotlin.random.Random

/** One drifting spore, with its own slow circular wander. */
private class Spore(rnd: Random) {
    val x = rnd.nextFloat()
    val y = rnd.nextFloat()
    val radius = 0.03f + rnd.nextFloat() * 0.09f   // how far it wanders
    val phase = rnd.nextFloat() * 2f * PI.toFloat()
    val speed = 0.6f + rnd.nextFloat() * 0.8f
    val size = 1.6f + rnd.nextFloat() * 2.4f
    val violet = rnd.nextFloat() < 0.28f
}

/**
 * Spores drifting and finding each other — the joining state.
 *
 * The same idea as the mark: a mesh is something that grows rather than
 * something that is configured. Edges appear between spores that drift close
 * enough and fade as they part, so the network assembles and dissolves without
 * ever settling, which is honest about what "joining" means — nothing is
 * connected yet.
 *
 * Deliberately cheap: a dozen points and one repeating float, no physics and no
 * per-frame allocation. This runs while a phone is doing the actual work of
 * bringing up a tunnel, and an idle animation that costs battery on the
 * connecting screen would be its own small betrayal.
 */
@Composable
fun SporeField(
    modifier: Modifier = Modifier,
    label: String = "",
    count: Int = 14,
) {
    // Fixed seed: the same drift every time, so the screen has a character
    // rather than being different noise on each launch.
    val spores = remember(count) { Random(0x5EED).let { r -> List(count) { Spore(r) } } }

    val t by rememberInfiniteTransition(label = "spores").animateFloat(
        initialValue = 0f,
        targetValue = 2f * PI.toFloat(),
        animationSpec = infiniteRepeatable(
            animation = tween(durationMillis = 14_000, easing = LinearEasing),
            repeatMode = RepeatMode.Restart,
        ),
        label = "drift",
    )

    Column(
        modifier = modifier,
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Canvas(Modifier.fillMaxSize().padding(bottom = 8.dp).height(180.dp)) {
            drawSpores(spores, t)
        }
        if (label.isNotEmpty()) {
            Text(label, style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
        }
    }
}

private fun DrawScope.drawSpores(spores: List<Spore>, t: Float) {
    val w = size.width
    val h = size.height
    if (w <= 0f || h <= 0f) return

    // Positions first, then edges, so a link is never drawn over a spore it
    // does not touch.
    val pts = spores.map { s ->
        val a = t * s.speed + s.phase
        Offset(
            (s.x + s.radius * cos(a)) * w,
            (s.y + s.radius * sin(a * 0.7f)) * h,
        )
    }

    // Link spores that have drifted close. The threshold is a fraction of the
    // smaller dimension so it behaves the same on any screen.
    val near = min(w, h) * 0.34f
    for (i in pts.indices) {
        for (j in i + 1 until pts.size) {
            val d = hypot(pts[i].x - pts[j].x, pts[i].y - pts[j].y)
            if (d > near) continue
            // Fade with distance: a link is strongest as they pass closest.
            val strength = 1f - (d / near)
            drawLine(
                color = Palette.Phosphor.copy(alpha = 0.10f + 0.45f * strength * strength),
                start = pts[i],
                end = pts[j],
                strokeWidth = 1f + 1.2f * strength,
            )
        }
    }

    for ((i, p) in pts.withIndex()) {
        val s = spores[i]
        val c = if (s.violet) Palette.Violet else Palette.Phosphor
        // A soft halo, then the spore itself.
        drawCircle(color = c.copy(alpha = 0.14f), radius = s.size * 3.2f, center = p)
        drawCircle(color = c.copy(alpha = 0.85f), radius = s.size, center = p)
    }
}
