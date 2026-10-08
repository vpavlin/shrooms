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

/** One window of a Claude subscription: the share used (0..1) and when it starts again. */
/** One window; [renewed]: its reset has passed since the reading, so its share is no longer known — 0 until a turn says. */
data class PlanWindow(val name: String, val utilization: Double, val resetsAt: Long, val renewed: Boolean = false,
                      /** At the pace it is used (the agent's forecast): the share at the reset, and when it runs out, if before; 0 if not said. */
                      val projected: Double = 0.0, val runsOutAt: Long = 0)

/**
 * Where a Claude subscription stands, as Claude Code last reported it on
 * [machines] (several when they share one account): the newest request's
 * [status] and the [window] it was about, and every window.
 */
data class PlanLimits(
    val machines: List<String>, val at: Long, val status: String, val window: String,
    val overage: Boolean, val windows: List<PlanWindow>,
)

/**
 * The newest subscription reading of each agent, by address, as its session
 * list brings it — what the usage link shows at a glance.
 */
object PlanLive {
    val byAddress = kotlinx.coroutines.flow.MutableStateFlow<Map<String, PlanLimits>>(emptyMap())
    fun note(address: String, l: PlanLimits?) {
        if (l == null || byAddress.value[address] == l) return
        byAddress.value = byAddress.value + (address to l)
    }
}

/**
 * One pay-as-you-go key's standing (Venice): what is left — DIEM, the daily
 * allowance, and USD — and when the allowance refills; [key] is a
 * fingerprint, so one key used by several machines is one entry.
 */
data class Credit(
    val machines: List<String>, val provider: String, val key: String,
    val balances: Map<String, Double>, val resetsAt: Long, val at: Long, val error: String,
    /** At the pace of the last two hours: when DIEM runs out (0: not before the refill), and what is left at it (-1: not said). */
    val runsOutAt: Long = 0, val leftAtRefill: Double = -1.0,
)

/** One machine's answer: its rows, its subscription's limits if Claude Code ever reported them, and its keys' credits. */
data class UsageAnswer(val rows: List<UsageRow>, val limits: PlanLimits?, val credits: List<Credit> = emptyList())

/** One line of the dashboard: a device, machine or model, and its sums. */
data class UsageLine(val name: String, val turns: Int, val output: Long, val input: Long, val costUsd: Double, val busyMs: Long)

object UsageView {
    enum class Measure(val label: String) { OUTPUT("tokens out"), TURNS("turns"), COST("cost"), BUSY("busy") }

    fun answer(machine: String, json: String) = UsageAnswer(parse(machine, json), parseLimits(machine, json), parseCredits(machine, json))

    /** The machine's keys' credits; none from an agent that has no pay-as-you-go key (or is older than credits). */
    fun parseCredits(machine: String, json: String): List<Credit> {
        val a = JSONObject(json).optJSONArray("credits") ?: return emptyList()
        return (0 until a.length()).map { i ->
            val o = a.getJSONObject(i)
            val b = o.optJSONObject("balances")
            Credit(listOf(machine), o.optString("provider"), o.optString("key"),
                b?.keys()?.asSequence()?.associateWith { b.optDouble(it) }.orEmpty(),
                epochMs(o.optString("resets_at")), epochMs(o.optString("at")), o.optString("error"),
                runsOutAt = epochMs(o.optString("runs_out_at")), leftAtRefill = o.optDouble("left_at_refill", -1.0))
        }
    }

    /** One entry per key, the newest reading, with every machine that uses it. */
    fun keys(all: List<Credit>): List<Credit> =
        all.groupBy { it.provider + "/" + it.key }.map { (_, cs) ->
            cs.maxBy { it.at }.copy(machines = cs.flatMap { it.machines }.distinct().sorted())
        }.sortedBy { it.provider + it.key }

    /** What is left: the daily allowance first, then dollars ("5.62 DIEM left today · USD −0.03"). */
    fun creditLine(c: Credit): String {
        if (c.error.isNotEmpty()) return c.error
        val diem = c.balances["DIEM"]?.let { "%.2f DIEM left today".format(java.util.Locale.ENGLISH, it) }
        val rest = c.balances.filterKeys { it != "DIEM" && (c.balances[it] ?: 0.0) != 0.0 }.toSortedMap()
            .map { (k, v) -> "$k %.2f".format(java.util.Locale.ENGLISH, v) }
        return (listOfNotNull(diem) + rest).joinToString(" · ").ifEmpty { "nothing left" }
    }

    private fun epochMs(t: String): Long = runCatching { java.time.OffsetDateTime.parse(t).toInstant().toEpochMilli() }.getOrDefault(0L)

    /** The machine's limits, or null when its agent has none to report (or is older than them). */
    fun parseLimits(machine: String, json: String): PlanLimits? {
        val o = JSONObject(json).optJSONObject("limits") ?: return null
        val ws = o.optJSONObject("windows")
        val windows = ws?.keys()?.asSequence()?.map { k ->
            val w = ws.getJSONObject(k)
            PlanWindow(k, w.optDouble("utilization", 0.0), epochMs(w.optString("resets_at")),
                projected = w.optDouble("projected", 0.0), runsOutAt = epochMs(w.optString("runs_out_at")))
        }?.sortedBy { windowOrder(it.name) }?.toList().orEmpty()
        if (windows.isEmpty() && o.optString("status").isEmpty()) return null
        return PlanLimits(listOf(machine), epochMs(o.optString("at")), o.optString("status"), o.optString("window"),
            o.optBoolean("overage"), windows)
    }

    private fun windowOrder(name: String) = when (name) { "five_hour" -> 0; "seven_day" -> 1; else -> 2 }

    /**
     * One entry per account: machines that report the same windows resetting
     * at the same moments share a subscription, so they are shown once, with
     * the newest reading among them.
     */
    fun accounts(all: List<PlanLimits>, now: Long = System.currentTimeMillis()): List<PlanLimits> =
        // By the 7-day window's reset where there is one: an old reading of
        // the same account still has it, while its 5-hour reset is long
        // past — grouped by both, a machine idle for hours was an account
        // of its own, with a share of a window gone since (2026-10-07).
        all.groupBy { l ->
            l.windows.firstOrNull { it.name == "seven_day" }?.let { listOf(it.name to it.resetsAt / 60_000) }
                ?: l.windows.map { it.name to it.resetsAt / 60_000 }.sortedBy { it.first }
        }
            .map { (_, ls) ->
                val newest = ls.maxBy { it.at }
                current(newest.copy(machines = ls.flatMap { it.machines }.distinct().sorted()), now)
            }
            .sortedByDescending { it.at }

    /**
     * The reading as it stands now: a window whose reset has passed is
     * renewed — its share was of a window that is over — and a status about
     * it no longer holds.
     */
    /** Where a window is heading at its pace: "" when the agent did not say (or it has renewed). */
    fun windowForecast(w: PlanWindow, now: Long = System.currentTimeMillis()): String = when {
        w.renewed || w.projected <= 0.0 -> ""
        w.runsOutAt > 0 -> "at this pace: runs out " + resets(w.runsOutAt, now) + " — before it resets"
        else -> "at this pace: about ${(w.projected * 100).toInt()}% at the reset — it lasts"
    }

    /** The same for a key's daily allowance. */
    fun creditForecast(c: Credit, now: Long = System.currentTimeMillis()): String = when {
        c.error.isNotEmpty() -> ""
        c.runsOutAt > 0 -> "at this pace: runs out " + resets(c.runsOutAt, now) + " — before the refill"
        c.leftAtRefill >= 0 -> "at this pace: about %.1f DIEM left at the refill".format(java.util.Locale.ENGLISH, c.leftAtRefill)
        else -> ""
    }

    fun current(l: PlanLimits, now: Long = System.currentTimeMillis()): PlanLimits {
        val ws = l.windows.map { if (it.resetsAt in 1..now) it.copy(utilization = 0.0, renewed = true, projected = 0.0, runsOutAt = 0) else it }
        val over = ws.firstOrNull { it.name == l.window }?.renewed == true
        return l.copy(windows = ws, status = if (over) "" else l.status)
    }

    /**
     * How hard the subscription is being used, for the link that opens
     * usage: the 5-hour window's share on the busiest account (the session
     * quota), and a level — 0 fine, 1 from half of it (slow down), 2 from
     * 80% or when a request was refused.
     */
    fun glance(plans: List<PlanLimits>): Pair<Int, Int>? {
        val shares = plans.map { p ->
            val w = p.windows.firstOrNull { it.name == "five_hour" } ?: p.windows.maxByOrNull { it.utilization }
            Triple(p, w?.utilization ?: 0.0, p.status == "rejected")
        }
        val top = shares.maxByOrNull { if (it.third) 2.0 else it.second } ?: return null
        val level = when {
            top.third || top.second >= 0.8 -> 2
            top.second >= 0.5 -> 1
            else -> 0
        }
        return (top.second * 100).toInt() to level
    }

    /** The bars' colour level: amber from half, red from 80%. */
    fun level(utilization: Double): Int = when {
        utilization >= 0.8 -> 2
        utilization >= 0.5 -> 1
        else -> 0
    }

    fun windowLabel(name: String): String = when (name) {
        "five_hour" -> "5 hours"
        "seven_day" -> "7 days"
        "seven_day_opus" -> "7 days, Opus"
        "seven_day_sonnet" -> "7 days, Sonnet"
        else -> name.replace('_', ' ')
    }

    /** When a window starts again, as a person reads it: a time today, else a day and time. */
    fun resets(at: Long, now: Long = System.currentTimeMillis()): String {
        if (at <= 0) return "?"
        val z = java.time.ZoneId.systemDefault()
        val t = java.time.Instant.ofEpochMilli(at).atZone(z)
        val p = if (at - now < 20 * 3600_000L) "HH:mm" else "EEE HH:mm"
        return t.format(java.time.format.DateTimeFormatter.ofPattern(p, java.util.Locale.ENGLISH))
    }

    /** What the newest request was told, when it is worth saying. */
    fun status(l: PlanLimits, now: Long = System.currentTimeMillis()): String {
        val w = l.windows.firstOrNull { it.name == l.window }
        val which = if (l.window.isEmpty()) "" else " (" + windowLabel(l.window) + ")"
        return when (l.status) {
            "rejected" -> "limit reached$which" + (w?.let { " — back at " + resets(it.resetsAt, now) } ?: "")
            // Claude Code warns at thresholds of its own (50% of the 7-day
            // window, 90% of the 5-hour one): the share, not "close".
            "allowed_warning" -> w?.let { "past ${(it.utilization * 100).toInt()}% of " + windowLabel(it.name) } ?: "warned$which"
            else -> ""
        }
    }

    fun age(at: Long, now: Long = System.currentTimeMillis()): String {
        val m = (now - at) / 60_000
        return when {
            at <= 0 -> "?"
            m < 1 -> "just now"
            m < 60 -> "${m} min ago"
            m < 48 * 60 -> "${m / 60} h ago"
            else -> "${m / (24 * 60)} d ago"
        }
    }

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
    var got by remember { mutableStateOf<Map<String, UsageAnswer?>>(emptyMap()) }
    LaunchedEffect(days, targets) {
        got = emptyMap()
        val since = UsageView.since(days)
        coroutineScope {
            for ((name, address) in targets) launch {
                val r = withContext(Dispatchers.IO) {
                    runCatching { UsageView.answer(name, AgentClient(address).usage(since)) }.getOrNull()
                }
                got = got + (name to r)
            }
        }
    }
    val waiting = targets.map { it.first }.filter { it !in got }
    val missing = got.filterValues { it == null }.keys.toList()
    val rows = got.values.filterNotNull().flatMap { it.rows }.takeIf { it.isNotEmpty() || waiting.isEmpty() }
    val plans = UsageView.accounts(got.values.mapNotNull { it?.limits })
    val credits = UsageView.keys(got.values.filterNotNull().flatMap { it.credits })
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
            // Where the subscription stands, whatever the period: how close to
            // its limits, and when they start again.
            if (plans.isNotEmpty()) PlanSection(plans)
            if (credits.isNotEmpty()) CreditSection(credits)
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
private fun CreditSection(credits: List<Credit>) {
    Text("CREDITS", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
        modifier = Modifier.padding(top = 8.dp, bottom = 6.dp))
    for (c in credits) {
        Column(Modifier.padding(vertical = 4.dp)) {
            Text(c.provider.replaceFirstChar { it.uppercase() } + " key " + c.key + " · " + c.machines.joinToString(", ") +
                " · as of " + UsageView.age(c.at), style = MaterialTheme.typography.labelSmall, color = Palette.Ash)
            Row(Modifier.padding(top = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(UsageView.creditLine(c), style = MaterialTheme.typography.bodyMedium,
                    color = if (c.error.isNotEmpty()) Palette.Amber else Palette.Bone, modifier = Modifier.weight(1f))
                if (c.resetsAt > 0 && c.error.isEmpty()) Text("refills " + UsageView.resets(c.resetsAt),
                    style = MaterialTheme.typography.labelMedium, color = Palette.Bone)
            }
            UsageView.creditForecast(c).takeIf { it.isNotEmpty() }?.let {
                Text(it, style = MaterialTheme.typography.labelSmall,
                    color = if (c.runsOutAt > 0) Palette.Rust else Palette.Ash, modifier = Modifier.padding(top = 2.dp))
            }
        }
    }
}

@Composable
private fun PlanSection(plans: List<PlanLimits>) {
    Text("PLAN LIMITS", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
        modifier = Modifier.padding(top = 8.dp, bottom = 6.dp))
    for (p in plans) {
        Column(Modifier.padding(vertical = 4.dp)) {
            Text(p.machines.joinToString(", ") + " · as of " + UsageView.age(p.at),
                style = MaterialTheme.typography.labelSmall, color = Palette.Ash)
            val st = UsageView.status(p)
            if (st.isNotEmpty()) Text(st, style = MaterialTheme.typography.labelSmall,
                color = if (p.status == "rejected") Palette.Rust else Palette.Amber, modifier = Modifier.padding(top = 2.dp))
            for (w in p.windows) {
                val colour = when (UsageView.level(w.utilization)) {
                    2 -> Palette.Rust
                    1 -> Palette.Amber
                    else -> Palette.Phosphor
                }
                Row(Modifier.padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(UsageView.windowLabel(w.name), style = MaterialTheme.typography.bodyMedium, color = Palette.Bone,
                        modifier = Modifier.weight(1f))
                    Text(if (w.renewed) "started again " + UsageView.resets(w.resetsAt) + " · no reading since"
                         else "${(w.utilization * 100).toInt()}% · resets " + UsageView.resets(w.resetsAt),
                        style = MaterialTheme.typography.labelMedium, color = Palette.Bone)
                }
                Box(Modifier.fillMaxWidth().padding(top = 3.dp).height(6.dp).background(Palette.Line, RoundedCornerShape(3.dp))) {
                    Box(Modifier.fillMaxWidth(w.utilization.toFloat().coerceIn(0f, 1f)).height(6.dp)
                        .background(colour, RoundedCornerShape(3.dp)))
                }
                UsageView.windowForecast(w).takeIf { it.isNotEmpty() }?.let {
                    Text(it, style = MaterialTheme.typography.labelSmall,
                        color = if (w.runsOutAt > 0) Palette.Rust else Palette.Ash, modifier = Modifier.padding(top = 2.dp))
                }
            }
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
