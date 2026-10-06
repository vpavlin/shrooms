package xyz.vpavlin.shrooms

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject

/**
 * Who uses the agents, how much, and where (the agent's GET /v1/usage): per
 * turn, the device that asked — known by its mesh address, which WireGuard
 * makes unforgeable — the machine whose model answered, the model, its tokens,
 * cost and time. With a model on a machine of your own, the basis of sharing
 * it fairly, or of billing for it.
 */
data class UsageRow(
    val machine: String, val day: String, val session: String, val by: String, val model: String,
    val turns: Int, val input: Long, val cacheRead: Long, val cacheWrite: Long, val output: Long,
    val costUsd: Double, val busyMs: Long,
)

/** One line of the dashboard: a device, machine or model, and its sums. */
data class UsageLine(val name: String, val turns: Int, val output: Long, val input: Long, val costUsd: Double, val busyMs: Long)

object UsageView {
    enum class Measure(val label: String) { OUTPUT("tokens out"), TURNS("turns"), COST("cost"), BUSY("busy") }

    fun parse(machine: String, json: String): List<UsageRow> {
        val a = JSONObject(json).optJSONArray("rows") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            UsageRow(machine, o.optString("day"), o.optString("session"), o.optString("by"), o.optString("model"),
                o.optInt("turns"), o.optLong("input"), o.optLong("cache_read"), o.optLong("cache_write"),
                o.optLong("output"), o.optDouble("cost_usd", 0.0), o.optLong("busy_ms"))
        }
    }

    /**
     * Who asked, as a person reads it: "nothing.office" is "nothing"; "" is
     * the agent's own machine (Basecamp on it, say), counted under its name —
     * the same device as when it asks another machine.
     */
    fun device(r: UsageRow): String = if (r.by.isEmpty()) r.machine else r.by.substringBefore('.')

    /** Rows summed by [key], largest first by [measure]. */
    fun group(rows: List<UsageRow>, measure: Measure, key: (UsageRow) -> String): List<UsageLine> =
        rows.groupBy(key).map { (name, rs) ->
            UsageLine(name, rs.sumOf { it.turns }, rs.sumOf { it.output }, rs.sumOf { it.input + it.cacheRead + it.cacheWrite },
                rs.sumOf { it.costUsd }, rs.sumOf { it.busyMs })
        }.sortedByDescending { value(it, measure) }

    fun value(l: UsageLine, m: Measure): Double = when (m) {
        Measure.OUTPUT -> l.output.toDouble()
        Measure.TURNS -> l.turns.toDouble()
        Measure.COST -> l.costUsd
        Measure.BUSY -> l.busyMs.toDouble()
    }

    fun format(l: UsageLine, m: Measure): String = when (m) {
        Measure.OUTPUT -> count(l.output)
        Measure.TURNS -> "${l.turns}"
        Measure.COST -> "$" + String.format(java.util.Locale.ROOT, "%.2f", l.costUsd)
        Measure.BUSY -> hours(l.busyMs)
    }

    fun count(n: Long): String = when {
        n >= 1_000_000 -> String.format(java.util.Locale.ROOT, "%.1fM", n / 1e6)
        n >= 1_000 -> String.format(java.util.Locale.ROOT, "%.1fk", n / 1e3)
        else -> "$n"
    }

    fun hours(ms: Long): String {
        val m = ms / 60_000
        return if (m >= 60) "${m / 60}h ${m % 60}m" else "${m}m"
    }

    /** The first day of a period, as the agents' dates are written; "" for all. */
    fun since(days: Int, today: java.time.LocalDate = java.time.LocalDate.now()): String =
        if (days <= 0) "" else today.minusDays((days - 1).toLong()).toString()
}

/** The usage dashboard: every machine asked, summed by who, where and which model. */
@Composable
fun UsageScreen(hosts: List<AgentHost>, onBack: () -> Unit) {
    var days by remember { mutableStateOf(7) }
    var measure by remember { mutableStateOf(UsageView.Measure.OUTPUT) }
    // Each machine's answer as it comes, null for one not reached. Keyed by
    // which machines, not by the list itself: that is refreshed every ten
    // seconds while this is open, and restarting on each refresh never let a
    // machine that was away time out — the screen asked forever (2026-10-06).
    val targets = remember(hosts) { hosts.map { it.name to it.address }.distinct() }
    var got by remember { mutableStateOf<Map<String, List<UsageRow>?>>(emptyMap()) }
    LaunchedEffect(days, targets) {
        got = emptyMap()
        val since = UsageView.since(days)
        coroutineScope {
            for ((name, address) in targets) launch {
                val r = withContext(Dispatchers.IO) {
                    runCatching { UsageView.parse(name, AgentClient(address).usage(since)) }.getOrNull()
                }
                got = got + (name to r)
            }
        }
    }
    val waiting = targets.map { it.first }.filter { it !in got }
    val missing = got.filterValues { it == null }.keys.toList()
    val rows = got.values.filterNotNull().flatten().takeIf { it.isNotEmpty() || waiting.isEmpty() }
    Column(Modifier.fillMaxSize().background(Palette.Void).padding(horizontal = 16.dp, vertical = 12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("‹", color = Palette.Phosphor, style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.clickable(onClick = onBack).padding(end = 12.dp))
            Text("USAGE", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
        }
        Row(Modifier.padding(top = 10.dp), horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            for ((d, label) in listOf(1 to "today", 7 to "7 days", 30 to "30 days", 0 to "all")) {
                Chip(label, d == days) { days = d }
            }
        }
        Row(Modifier.padding(top = 6.dp), horizontalArrangement = Arrangement.spacedBy(14.dp)) {
            for (m in UsageView.Measure.entries) Chip(m.label, m == measure) { measure = m }
        }
        val r = rows
        Column(Modifier.verticalScroll(rememberScrollState()).padding(top = 8.dp)) {
            when {
                r == null -> Text("asking the machines…", style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                r.isEmpty() -> Text("nothing used in this period", style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                else -> {
                    UsageSection("WHO ASKED", UsageView.group(r, measure, UsageView::device), measure, Palette.Phosphor)
                    UsageSection("WHERE IT RAN", UsageView.group(r, measure) { it.machine }, measure, Palette.Sky)
                    UsageSection("MODEL", UsageView.group(r, measure) { it.model.ifEmpty { "?" } }, measure, Palette.Violet)
                }
            }
            if (rows != null && waiting.isNotEmpty()) Text("still asking: " + waiting.joinToString(", "),
                style = MaterialTheme.typography.labelSmall, color = Palette.Ash, modifier = Modifier.padding(top = 10.dp))
            if (missing.isNotEmpty()) Text("not reached: " + missing.joinToString(", "),
                style = MaterialTheme.typography.labelSmall, color = Palette.Amber, modifier = Modifier.padding(top = 10.dp))
        }
    }
}

@Composable
private fun Chip(label: String, on: Boolean, onClick: () -> Unit) =
    Text(label, style = MaterialTheme.typography.labelSmall, color = if (on) Palette.Phosphor else Palette.Ash,
        modifier = Modifier.clickable(onClick = onClick).padding(vertical = 6.dp))

@Composable
private fun UsageSection(title: String, lines: List<UsageLine>, measure: UsageView.Measure, colour: Color) {
    Text(title, style = MaterialTheme.typography.labelSmall, color = colour, modifier = Modifier.padding(top = 16.dp, bottom = 6.dp))
    val top = lines.maxOfOrNull { UsageView.value(it, measure) }?.takeIf { it > 0 } ?: 1.0
    for (l in lines) {
        Column(Modifier.padding(vertical = 4.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(l.name, style = MaterialTheme.typography.bodyMedium, color = Palette.Bone, maxLines = 1,
                    overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                Text(UsageView.format(l, measure), style = MaterialTheme.typography.labelMedium, color = Palette.Bone)
            }
            Box(Modifier.fillMaxWidth().padding(top = 3.dp).height(6.dp).background(Palette.Line, RoundedCornerShape(3.dp))) {
                Box(Modifier.fillMaxWidth((UsageView.value(l, measure) / top).toFloat().coerceIn(0f, 1f)).height(6.dp)
                    .background(colour, RoundedCornerShape(3.dp)))
            }
            Text("${l.turns} turns · ${UsageView.count(l.output)} out · ${UsageView.count(l.input)} in" +
                (if (l.costUsd > 0) " · $" + String.format(java.util.Locale.ROOT, "%.2f", l.costUsd) else "") +
                " · ${UsageView.hours(l.busyMs)}",
                style = MaterialTheme.typography.labelSmall, color = Palette.Ash, modifier = Modifier.padding(top = 2.dp))
        }
    }
    Spacer(Modifier.width(1.dp))
}
