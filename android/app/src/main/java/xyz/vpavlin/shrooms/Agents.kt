package xyz.vpavlin.shrooms

import androidx.activity.compose.BackHandler
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.ui.draw.clip
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.derivedStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.graphics.Color
import org.json.JSONObject
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitAll
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import kotlin.math.PI

/** How many of a session's latest events open with it; the rest on request. */
const val SESSION_TAIL = 300

/** A machine running shrooms-agent, reached at [address] on [mesh]. */
data class AgentHost(
    val name: String,
    val mesh: String,
    val address: String,
    val sessions: List<AgentSession>,
    /** When it last answered, epoch millis; 0 for "just now" (found this round). */
    val lastSeen: Long = 0,
)

/**
 * The list kept between rounds of finding, so a machine that misses a round —
 * a moment of a flaky network — stays where it is with its sessions as last
 * seen, rather than vanishing and coming back and moving everything under the
 * reader. Shown as unreachable once it has missed [STALE_MS]; forgotten after
 * [FORGET_MS].
 */
object HostCache {
    const val STALE_MS = 25_000L
    const val FORGET_MS = 7L * 24 * 3600 * 1000

    fun reachable(h: AgentHost, now: Long): Boolean = now - h.lastSeen < STALE_MS

    /** This round's [found] hosts, and the [previous] ones that did not answer, by name. */
    fun merge(previous: List<AgentHost>, found: List<AgentHost>, now: Long): List<AgentHost> {
        val fresh = found.map { it.copy(lastSeen = now) }
        val names = fresh.map { it.name }.toSet()
        val kept = previous.filter { it.name !in names && now - it.lastSeen < FORGET_MS }
        return (fresh + kept).sortedBy { it.name }
    }

    fun encode(hosts: List<AgentHost>): String = org.json.JSONArray(hosts.map { h ->
        JSONObject().put("name", h.name).put("mesh", h.mesh).put("address", h.address).put("seen", h.lastSeen)
            .put("sessions", org.json.JSONArray(h.sessions.map { s ->
                JSONObject().put("name", s.name).put("dir", s.dir).put("state", s.state).put("pending", s.pending)
                    .put("running", s.running).put("last_seq", s.lastSeq).put("last_time", s.lastTime)
                    .put("auto_approve", s.autoApprove).put("context_used", s.contextUsed)
                    .put("context_window", s.contextWindow).put("preview", s.preview).put("model", s.model)
                    .put("harness", s.harness).put("approves", s.approves).put("starred", s.starred)
            }))
    }).toString()

    fun decode(json: String): List<AgentHost> = runCatching {
        val a = org.json.JSONArray(json)
        (0 until a.length()).map { i ->
            val h = a.getJSONObject(i)
            val ss = h.optJSONArray("sessions") ?: org.json.JSONArray()
            AgentHost(h.optString("name"), h.optString("mesh"), h.optString("address"),
                (0 until ss.length()).map { j ->
                    val s = ss.getJSONObject(j)
                    AgentSession(
                        name = s.optString("name"), dir = s.optString("dir"), state = s.optString("state"),
                        pending = s.optInt("pending"), running = s.optBoolean("running"), lastSeq = s.optLong("last_seq"),
                        lastTime = s.optLong("last_time"), autoApprove = s.optBoolean("auto_approve"),
                        contextUsed = s.optLong("context_used"), contextWindow = s.optLong("context_window"),
                        preview = s.optString("preview"), model = s.optString("model"),
                        harness = s.optString("harness").ifEmpty { "claude" }, approves = s.optBoolean("approves", true),
                        starred = s.optBoolean("starred"),
                    )
                },
                lastSeen = h.optLong("seen"))
        }.filter { it.name.isNotEmpty() && AgentClient.isMeshAddress(it.address) }
    }.getOrDefault(emptyList())
}

/**
 * The list as shown: starred sessions first, from every machine, by name;
 * then each machine with the rest of its sessions.
 */
fun starredFirst(hosts: List<AgentHost>): Pair<List<Pair<AgentHost, AgentSession>>, List<AgentHost>> {
    val starred = hosts.flatMap { h -> h.sessions.filter { it.starred }.map { h to it } }
        .sortedWith(compareBy({ it.second.name }, { it.first.name }))
    return starred to hosts.map { h -> h.copy(sessions = h.sessions.filterNot { it.starred }) }
}

/** The hosts with one session's star set as asked: shown at once, before the agent answers. */
fun withStar(hosts: List<AgentHost>, host: String, session: String, on: Boolean): List<AgentHost> =
    hosts.map { h -> if (h.name != host) h else h.copy(sessions = h.sessions.map { if (it.name == session) it.copy(starred = on) else it }) }

/** A session to open straight away — from a notification. */
data class OpenSession(val address: String, val host: String, val mesh: String, val session: String)

/**
 * Finds every agent among the peers: one probe per online device on each of
 * its addresses, first answer wins. A probe rather than reading what peers
 * announce, because announcing bound ports is off by default (ADR-026) and an
 * agent should be found with nothing configured.
 */
/**
 * Who to ask directly, besides the shrooms app's peers: the ones it handed
 * over, and every machine an agent was found on before. Those last were often
 * found only through another agent's /v1/peers — through the laptop, say — and
 * with the laptop gone, nothing reached them and every one greyed (2026-10-04).
 */
fun agentCandidates(handed: List<AgentHosts.Host>, seen: List<AgentHost>): List<AgentHosts.Host> =
    (handed + seen.map { AgentHosts.Host(it.name, it.mesh, it.address) }).distinctBy { it.name }

suspend fun discoverAgents(peers: List<Peer>, byName: List<String> = emptyList(),
                           known: List<AgentHosts.Host> = emptyList()): List<AgentHost> =
    withContext(Dispatchers.IO) {
        val fromPeers = peers.filter { it.online }.groupBy { it.name }.map { (name, ps) ->
            async {
                ps.firstNotNullOfOrNull { p ->
                    listOf(p.overlay, p.overlayV4).filter { it.isNotEmpty() }.firstNotNullOfOrNull { a ->
                        runCatching { AgentHost(name, p.mesh, a, AgentClient(a).sessions(3000)) }.getOrNull()
                    }
                }
            }
        }
        // The peers the shrooms app handed over, when this is Shrooms Agents.
        val handed = known.filter { k -> peers.none { it.name == k.name } }.map { k ->
            async { runCatching { AgentHost(k.name, k.mesh, k.address, AgentClient(k.address).sessions(3000)) }.getOrNull() }
        }
        val named = byName.filter { n -> peers.none { it.name == n.substringBefore('.') } }.map { n ->
            async {
                meshAddressesOf(n).firstNotNullOfOrNull { a ->
                    runCatching {
                        AgentHost(n.substringBefore('.'), n.substringAfter('.', "").substringBefore('.'), a,
                            AgentClient(a).sessions(3000))
                    }.getOrNull()
                }
            }
        }
        val found = (fromPeers + handed + named).awaitAll().filterNotNull().distinctBy { it.name }
        // Then the mesh as each agent's machine sees it: a phone that knows one
        // agent finds the rest without being told (/v1/peers). One round, not
        // a crawl — every machine sees the same mesh.
        val known = found.map { it.name }.toSet() + peers.map { it.name }
        val via = found.flatMap { h -> runCatching { AgentClient(h.address).peers() }.getOrDefault(emptyList()) }
            .filter { it.first !in known }.distinctBy { it.first }
            .map { (name, mesh, addr) ->
                async { runCatching { AgentHost(name, mesh, addr, AgentClient(addr).sessions(3000)) }.getOrNull() }
            }
        (found + via.awaitAll().filterNotNull()).sortedBy { it.name }
    }

/**
 * A machine named rather than found among the peers — `laptop.office.mesh` —
 * resolved by whatever answers .mesh names on this phone (the shrooms app's
 * resolver, when its tunnel is up), keeping only answers that are mesh
 * addresses: a name is not trusted to point inside the tunnel just because it
 * ends in .mesh.
 */
fun meshAddressesOf(name: String): List<String> =
    runCatching { java.net.InetAddress.getAllByName(name).map { it.hostAddress ?: "" } }
        .getOrDefault(emptyList())
        .map { it.substringBefore('%') }
        .filter { AgentClient.isMeshAddress(it) }

// --- the look ---------------------------------------------------------------

/**
 * The same spores the mesh screen draws, faint, drifting behind everything:
 * the agents are part of the same app, not a utility bolted onto it.
 */
@Composable
private fun SporeBackdrop(alpha: Float = 0.16f) {
    val motes = remember { sporeField(26) }
    val t by rememberInfiniteTransition(label = "agents-spores").animateFloat(
        initialValue = 0f, targetValue = 2f * PI.toFloat(),
        animationSpec = infiniteRepeatable(tween(60_000, easing = LinearEasing), RepeatMode.Restart),
        label = "drift",
    )
    Canvas(Modifier.fillMaxSize()) { drawSpores(motes, t, alpha) }
}

/** A spore that breathes: something is happening. */
@Composable
private fun Pulse(colour: Color, size: Int = 8) {
    val a by rememberInfiniteTransition(label = "pulse").animateFloat(
        0.25f, 1f, infiniteRepeatable(tween(900), RepeatMode.Reverse), label = "a",
    )
    Box(Modifier.size(size.dp).background(colour.copy(alpha = a), CircleShape))
}

private fun meshColour(mesh: String, meshes: List<String>): Color =
    meshTints[meshes.indexOf(mesh).coerceAtLeast(0) % meshTints.size]

private val clock = SimpleDateFormat("HH:mm", Locale.getDefault())
private val dated = SimpleDateFormat("d MMM HH:mm", Locale.getDefault())

/** "14:02" today, "2 Oct 14:02" otherwise. */
fun whenSaid(ms: Long): String {
    if (ms <= 0) return ""
    val day = 24 * 3600 * 1000L
    val tz = java.util.TimeZone.getDefault()
    val today = (System.currentTimeMillis() + tz.getOffset(System.currentTimeMillis())) / day
    val that = (ms + tz.getOffset(ms)) / day
    return (if (that == today) clock else dated).format(Date(ms))
}

/** "45% of 1M", or "" when the agent has not said. */
fun contextLabel(used: Long, window: Long): String {
    if (used <= 0 || window <= 0) return ""
    val pct = (used * 100 / window).coerceIn(0, 100)
    val w = if (window >= 1_000_000) "${window / 1_000_000}M" else "${window / 1000}k"
    return "$pct% of $w"
}

/** "claude-opus-5[1m]" → "opus-5 1m". */
/** The harness, named where it is not the usual one: "pi". */
fun harnessLabel(h: String): String = if (h == "claude" || h.isEmpty()) "" else h

fun shortModel(m: String): String =
    m.removePrefix("claude-").replace("[", " ").replace("]", "").replace(Regex("""-\d{8}$"""), "")

private fun contextColour(used: Long, window: Long): Color {
    val f = if (window > 0) used.toFloat() / window else 0f
    return when {
        f >= 0.9f -> Palette.Rust
        f >= 0.7f -> Palette.Amber
        else -> Palette.Phosphor
    }
}

@Composable
private fun Link(text: String, colour: Color = Palette.Phosphor, onClick: () -> Unit) =
    Text(text, style = MaterialTheme.typography.labelSmall, color = colour, maxLines = 1, softWrap = false,
        modifier = Modifier.clickable { onClick() }.padding(horizontal = 8.dp, vertical = 10.dp))

// --- the list ---------------------------------------------------------------

@Composable
fun AgentsScreen(peers: List<Peer>, onClose: () -> Unit, initial: OpenSession? = null) {
    val ctx = LocalContext.current
    val cachePrefs = remember { ctx.getSharedPreferences("agents", android.content.Context.MODE_PRIVATE) }
    // The list as last seen, at once: the agents answer within seconds, and
    // until then this is what there was (HostCache).
    var hosts by remember { mutableStateOf(HostCache.decode(cachePrefs.getString("cache", "") ?: "").ifEmpty { null }) }
    var open by remember(initial) { mutableStateOf(initial) }
    var creatingOn by remember { mutableStateOf<AgentHost?>(null) }
    var refresh by remember { mutableStateOf(0) }
    // Machines added by name, kept across launches.
    val prefs = remember { ctx.getSharedPreferences("agents", android.content.Context.MODE_PRIVATE) }
    var named by remember { mutableStateOf(prefs.getStringSet("named", emptySet())!!.sorted()) }
    var addingMachine by remember { mutableStateOf(false) }
    var voiceOpen by remember { mutableStateOf(false) }
    var usageOpen by remember { mutableStateOf(false) }
    if (voiceOpen) VoiceDialog { voiceOpen = false }
    // A session awaiting confirmation that it should be deleted.
    var deleting by remember { mutableStateOf<Pair<AgentHost, AgentSession>?>(null) }
    val scope = rememberCoroutineScope()

    // Refreshed while the list is on screen, so a session that starts waiting
    // for an answer shows it without a pull. What is found is remembered for
    // the notification watcher (AgentWatch).
    LaunchedEffect(refresh, open, named) {
        if (open != null) return@LaunchedEffect
        while (isActive) {
            val found = discoverAgents(peers, named, agentCandidates(AgentHosts.peers(ctx), hosts.orEmpty()))
            val merged = HostCache.merge(hosts.orEmpty(), found, System.currentTimeMillis())
            hosts = merged
            cachePrefs.edit().putString("cache", HostCache.encode(merged)).apply()
            // The watcher keeps asking a machine that missed a round, too.
            if (merged.isNotEmpty()) AgentHosts.save(ctx, merged.map { AgentHosts.Host(it.name, it.mesh, it.address) })
            delay(10_000)
        }
    }

    Box(Modifier.fillMaxSize().background(Palette.Void)) {
        SporeBackdrop()
        if (usageOpen) {
            BackHandler { usageOpen = false }
            UsageScreen(hosts.orEmpty()) { usageOpen = false }
            return@Box
        }
        val o = open
        if (o != null) {
            BackHandler { open = null }
            SessionScreen(o, onBack = { open = null; refresh++ })
            return@Box
        }
        BackHandler { if (creatingOn != null) creatingOn = null else onClose() }
        deleting?.let { (h, sess) ->
            DeleteSessionDialog(h.name, h.address, sess, onDismiss = { deleting = null },
                onDeleted = { deleting = null; refresh++ })
        }

        Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing).padding(horizontal = 20.dp)) {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Pulse(Palette.Phosphor, 10)
                Spacer(Modifier.width(10.dp))
                Text("AGENTS", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
            }
            // Actions on a line of their own, so nothing has to squeeze in
            // beside the title.
            Row(Modifier.padding(top = 2.dp)) {
                Link("refresh") { refresh++ }
                Link(if (addingMachine) "cancel" else "+ machine") { addingMachine = !addingMachine }
                Spacer(Modifier.weight(1f))
                Link("usage", Palette.Ash) { usageOpen = true }
                Link("voice", Palette.Ash) { voiceOpen = true }
                Link("close", Palette.Ash) { onClose() }
            }

            if (addingMachine) {
                var adding by remember { mutableStateOf("") }
                Field("machine, e.g. laptop.office.mesh", adding) { adding = it.trim() }
                Row {
                    Link("add", if (adding.isNotEmpty()) Palette.Phosphor else Palette.Ash) {
                        if (adding.isNotEmpty()) {
                            named = (named + adding).distinct().sorted()
                            prefs.edit().putStringSet("named", named.toSet()).apply()
                            addingMachine = false
                            refresh++
                        }
                    }
                    if (named.isNotEmpty()) Link("forget ${named.size} named", Palette.Ash) {
                        named = emptyList()
                        prefs.edit().remove("named").apply()
                    }
                }
            }

            val c = creatingOn
            if (c != null) {
                NewSession(c, onDone = { creatingOn = null; refresh++ },
                onOpen = { n -> creatingOn = null; open = OpenSession(c.address, c.name, c.mesh, n) })
                return@Column
            }

            val hs = hosts
            when {
                hs == null -> Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 20.dp)) {
                    Pulse(Palette.Amber); Spacer(Modifier.width(10.dp)); Label("looking for agents on the mesh…")
                }
                hs.isEmpty() -> Text(
                    "No agents found. An agent is shrooms-agent running on one of your machines, " +
                        "on its mesh address, port $AGENT_PORT. Open this from the shrooms app's " +
                        "\"agents\" link to look on every peer it can reach, or add a machine by name.",
                    style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
                    modifier = Modifier.padding(top = 20.dp),
                )
            }

            val meshes = hs.orEmpty().map { it.mesh }.distinct().sorted()
            val (starred, rest) = starredFirst(hs.orEmpty())
            // Read on each round's recomposition: what has gone quiet greys.
            val now = System.currentTimeMillis()
            // Replies not yet seen here, redrawn as sessions are read.
            val unreadTick by Unread.changed.collectAsState()
            fun unread(h: AgentHost, sess: AgentSession) = unreadTick.let { Unread.of(ctx, h.name, sess) }
            fun star(h: AgentHost, sess: AgentSession) {
                hosts = withStar(hosts.orEmpty(), h.name, sess.name, !sess.starred)
                scope.launch {
                    withContext(Dispatchers.IO) { runCatching { AgentClient(h.address).setStarred(sess.name, !sess.starred) } }
                        .onFailure { hosts = withStar(hosts.orEmpty(), h.name, sess.name, sess.starred) }
                }
            }
            LazyColumn(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 8.dp)) {
                if (starred.isNotEmpty()) {
                    item(key = "starred") {
                        Text("🍄  STARRED", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor,
                            modifier = Modifier.padding(top = 14.dp))
                    }
                    itemsIndexed(starred, key = { _, p -> "s-" + p.first.name + "/" + p.second.name }) { _, (h, sess) ->
                        SessionRow(sess, where = h.name, reachable = HostCache.reachable(h, now), unread = unread(h, sess), onStar = { star(h, sess) }, onLongPress = { deleting = h to sess }) {
                            open = OpenSession(h.address, h.name, h.mesh, sess.name)
                        }
                    }
                }
                for (h in rest) {
                    val up = HostCache.reachable(h, now)
                    item(key = "h-" + h.name) {
                        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(top = 14.dp).alpha(if (up) 1f else 0.45f)) {
                            Box(Modifier.size(10.dp).border(2.dp, meshColour(h.mesh, meshes), CircleShape))
                            Spacer(Modifier.width(10.dp))
                            Text(h.name, style = MaterialTheme.typography.titleMedium, color = Palette.Bone)
                            Spacer(Modifier.width(8.dp))
                            Text(h.mesh, style = MaterialTheme.typography.labelSmall, color = meshColour(h.mesh, meshes))
                            if (!up) {
                                Spacer(Modifier.width(8.dp))
                                Text("unreachable · seen ${whenSaid(h.lastSeen)}", style = MaterialTheme.typography.labelSmall,
                                    color = Palette.Ash, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                            Spacer(Modifier.weight(1f))
                            if (up) Link("+ session") { creatingOn = h }
                        }
                    }
                    if (h.sessions.isEmpty()) {
                        val all = hs.orEmpty().firstOrNull { it.name == h.name }?.sessions.orEmpty()
                        item(key = "e-" + h.name) { Label(if (all.isEmpty()) "no sessions yet" else "all starred") }
                    }
                    itemsIndexed(h.sessions, key = { _, s -> h.name + "/" + s.name }) { _, s ->
                        SessionRow(s, reachable = up, unread = unread(h, s), onStar = { star(h, s) }, onLongPress = { deleting = h to s }) {
                            open = OpenSession(h.address, h.name, h.mesh, s.name)
                        }
                    }
                }
                item(key = "bottom") { Spacer(Modifier.height(24.dp)) }
            }
        }
    }
}

@Composable
@OptIn(androidx.compose.foundation.ExperimentalFoundationApi::class)
private fun SessionRow(s: AgentSession, where: String = "", reachable: Boolean = true, unread: Int = 0, onStar: () -> Unit, onLongPress: () -> Unit, onOpen: () -> Unit) {
    val (badge, colour) = when {
        !reachable -> "unreachable" to Palette.Ash
        else -> when (s.state) {
        "waiting" -> "NEEDS YOU" to Palette.Amber
        "working" -> "WORKING" to Palette.Phosphor
        else -> (if (s.running) "idle" else "asleep") to Palette.Ash
        }
    }
    // The star has a column of its own, the card's full height and the same
    // place on every row: tucked in after the badge it moved with the badge's
    // width and sat where a thumb lands to open the session.
    Row(
        Modifier.fillMaxWidth().height(IntrinsicSize.Min)
            .clip(RoundedCornerShape(12.dp))
            .background(Palette.Panel.copy(alpha = 0.85f), RoundedCornerShape(12.dp))
            .border(1.dp, if (reachable && s.state == "waiting") Palette.Amber else Palette.Line, RoundedCornerShape(12.dp))
            .alpha(if (reachable) 1f else 0.5f),
    ) {
    Column(
        Modifier.weight(1f)
            .combinedClickable(onClick = onOpen, onLongClick = onLongPress)
            .padding(14.dp),
        verticalArrangement = Arrangement.spacedBy(6.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(s.name, style = MaterialTheme.typography.titleMedium, color = Palette.Bone,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            if (unread > 0) {
                Spacer(Modifier.width(8.dp))
                Text(if (unread > 99) "99+" else unread.toString(), style = MaterialTheme.typography.labelSmall,
                    color = Palette.Void,
                    modifier = Modifier.background(Palette.Bone, RoundedCornerShape(50)).padding(horizontal = 7.dp, vertical = 1.dp))
            }
            if (where.isNotEmpty()) {
                Spacer(Modifier.width(8.dp))
                Text(where, style = MaterialTheme.typography.labelSmall, color = Palette.Ash, maxLines = 1)
            }
            Spacer(Modifier.weight(1f))
            if (reachable && s.state != "idle") { Pulse(colour); Spacer(Modifier.width(6.dp)) }
            Text(badge, style = MaterialTheme.typography.labelSmall, color = colour)
        }
        if (s.preview.isNotEmpty()) {
            Text(s.preview, style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
                maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        val meta = listOf(whenSaid(s.lastTime), contextLabel(s.contextUsed, s.contextWindow),
            harnessLabel(s.harness), shortModel(s.model), if (s.autoApprove && s.approves) "auto-approve" else "")
            .filter { it.isNotEmpty() }
        Text((meta + s.dir.replace(Regex("^/home/[^/]+"), "~")).joinToString("  ·  "),
            style = MaterialTheme.typography.labelSmall, color = Palette.Ash,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
    Box(Modifier.fillMaxHeight().width(1.dp).background(Palette.Line))
    // Starred: listed first. Faint until it is.
    Box(
        Modifier.fillMaxHeight().width(52.dp).clickable(onClick = onStar),
        contentAlignment = Alignment.Center,
    ) {
        Text("🍄", modifier = Modifier.alpha(if (s.starred) 1f else 0.25f))
    }
    }
}

@Composable
private fun NewSession(h: AgentHost, onDone: () -> Unit, onOpen: (String) -> Unit) {
    var convs by remember { mutableStateOf<List<Conversation>?>(null) }
    var convError by remember { mutableStateOf("") }
    var reload by remember { mutableStateOf(0) }
    LaunchedEffect(h.address, reload) {
        withContext(Dispatchers.IO) { runCatching { AgentClient(h.address).conversations() } }
            .onSuccess { convs = it }
            .onFailure { convError = it.message ?: "could not list them" }
    }
    var harnesses by remember { mutableStateOf(claudeOnly) }
    var harness by remember { mutableStateOf("claude") }
    LaunchedEffect(h.address) { harnesses = withContext(Dispatchers.IO) { AgentClient(h.address).harnesses() } }
    val chosen = harnesses.firstOrNull { it.name == harness } ?: claudeOnly[0]
    var name by remember { mutableStateOf("") }
    var dir by remember { mutableStateOf("~/") }
    var auto by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()
    Column(verticalArrangement = Arrangement.spacedBy(10.dp),
        modifier = Modifier.padding(top = 16.dp).verticalScroll(rememberScrollState())) {
        Text("NEW SESSION ON ${h.name.uppercase()}", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
        Label("A name and a directory on that machine, like a cl session. ~ is that machine's home.")
        Field("name", name) { name = it }
        Field("directory", dir) { dir = it }
        // Which coding agent, when the machine has more than Claude Code.
        if (harnesses.size > 1) Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            for (hn in harnesses) {
                val on = hn.name == harness
                Text(hn.title, style = MaterialTheme.typography.labelSmall,
                    color = if (on) Palette.Void else Palette.Bone,
                    modifier = Modifier.background(if (on) Palette.Phosphor else Color.Transparent, RoundedCornerShape(10.dp))
                        .border(1.dp, if (on) Palette.Phosphor else Palette.Line, RoundedCornerShape(10.dp))
                        .clickable { harness = hn.name }.padding(horizontal = 12.dp, vertical = 8.dp))
            }
        }
        if (chosen.approves) Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.clickable { auto = !auto }) {
            Box(Modifier.size(12.dp).border(1.dp, if (auto) Palette.Phosphor else Palette.Ash, RoundedCornerShape(3.dp))
                .background(if (auto) Palette.Phosphor else Color.Transparent, RoundedCornerShape(3.dp)))
            Spacer(Modifier.width(10.dp))
            Text("auto-approve — never ask, like --dangerously-skip-permissions",
                style = MaterialTheme.typography.bodySmall, color = if (auto) Palette.Bone else Palette.Ash)
        }
        if (error.isNotEmpty()) Text(error, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
        Action("CREATE", enabled = !busy && name.isNotBlank() && dir.isNotBlank()) {
            busy = true
            scope.launch {
                val r = withContext(Dispatchers.IO) {
                    runCatching { AgentClient(h.address).create(name.trim(), dir.trim(), auto && chosen.approves, harness) }
                }
                busy = false
                r.onSuccess { onDone() }.onFailure { error = it.message ?: "could not create it" }
            }
        }
        Link("cancel", Palette.Ash) { onDone() }

        if (harness != "claude") { Spacer(Modifier.height(24.dp)); return@Column }
        // Or carry on one that started somewhere else — in a terminal, under
        // cl. Resuming keeps writing to the same conversation: this continues
        // it rather than copying it. Claude Code's only, so far.
        Spacer(Modifier.height(8.dp))
        Text("OR CONTINUE A CONVERSATION", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
        Label("Newest first. A terminal still open in the same directory may hold it: stop it first, or the two write over each other's turns.")
        when {
            convError.isNotEmpty() -> Text(convError, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
            convs == null -> Row(verticalAlignment = Alignment.CenterVertically) { Pulse(Palette.Amber); Spacer(Modifier.width(8.dp)); Label("looking…") }
            convs.isEmpty() -> Label("none here yet — only conversations that ran in a directory on this machine are listed")
        }
        for (c in convs.orEmpty()) {
            val taken = c.adoptedBy.isNotEmpty()
            Column(
                Modifier.fillMaxWidth()
                    .background(Palette.Panel, RoundedCornerShape(10.dp))
                    .border(1.dp, Palette.Line, RoundedCornerShape(10.dp))
                    .clickable(enabled = !taken && !busy) {
                        busy = true
                        val n = sessionNameFor(c.dir, h.sessions.map { it.name })
                        scope.launch {
                            val r = withContext(Dispatchers.IO) { runCatching { AgentClient(h.address).takeOver(n, c.id, auto) } }
                            busy = false
                            r.onSuccess { onOpen(n) }.onFailure { error = it.message ?: "could not take it over" }
                        }
                    }
                    .padding(12.dp),
                verticalArrangement = Arrangement.spacedBy(3.dp),
            ) {
                Row {
                    Text(c.dir.replace(Regex("^/home/[^/]+"), "~"), style = MaterialTheme.typography.bodyMedium,
                        color = if (taken) Palette.Ash else Palette.Bone, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f))
                    Label(whenSaid(c.modified))
                }
                if (c.lastUser.isNotEmpty()) Text("you: " + c.lastUser, style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (c.lastAssistant.isNotEmpty()) Text("claude: " + c.lastAssistant, style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (taken) Text("continued here as \"${c.adoptedBy}\"", style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor)
                for ((pid, where) in c.terminals) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Pulse(Palette.Amber); Spacer(Modifier.width(8.dp))
                        Text("open in a terminal: $where", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
                            modifier = Modifier.weight(1f))
                        Link("stop it", Palette.Rust) {
                            scope.launch {
                                withContext(Dispatchers.IO) { runCatching { AgentClient(h.address).stopTerminal(pid) } }
                                    .onFailure { error = it.message ?: "could not stop it" }
                                reload++
                            }
                        }
                    }
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

/**
 * A session name from the directory a conversation ran in, made unique among
 * the machine's sessions: "logos-vpn", then "logos-vpn-2".
 */
fun sessionNameFor(dir: String, taken: List<String>): String {
    val base = dir.trimEnd('/').substringAfterLast('/')
        .replace(Regex("[^a-zA-Z0-9._-]"), "-").trimStart('-', '.', '_').take(40).ifEmpty { "conversation" }
    var name = base
    var n = 2
    while (name in taken) name = "$base-${n++}"
    return name
}

@Composable
private fun Field(label: String, value: String, onChange: (String) -> Unit) {
    OutlinedTextField(
        value = value, onValueChange = onChange, singleLine = true,
        label = { Text(label) },
        textStyle = MaterialTheme.typography.bodyMedium.copy(color = Palette.Bone),
        colors = OutlinedTextFieldDefaults.colors(
            focusedBorderColor = Palette.Phosphor, unfocusedBorderColor = Palette.Line,
            focusedLabelColor = Palette.Phosphor, unfocusedLabelColor = Palette.Ash,
            cursorColor = Palette.Phosphor,
        ),
        modifier = Modifier.fillMaxWidth(),
    )
}

// --- a conversation ---------------------------------------------------------

@Composable
private fun SessionScreen(o: OpenSession, onBack: () -> Unit) {
    var askDelete by remember { mutableStateOf(false) }
    // How many of the last events are loaded: 0 is everything, once asked for;
    // more, to reach a search result further back.
    var tail by remember(o) { mutableStateOf(SESSION_TAIL) }
    // Search: the box, what it found, and the event a result jumps to.
    var searching by remember(o) { mutableStateOf(false) }
    var query by remember(o) { mutableStateOf("") }
    var found by remember(o) { mutableStateOf<List<Found>?>(null) }
    var searchBusy by remember { mutableStateOf(false) }
    var reading by remember { mutableStateOf<Found?>(null) }
    var jumpTo by remember(o) { mutableStateOf(0L) }
    var lit by remember(o) { mutableStateOf(0L) }
    val client = remember(o.address) { AgentClient(o.address) }
    val events = remember(o) { mutableStateListOf<AgentEvent>() }
    var earlier by remember(o) { mutableStateOf<List<Earlier>>(emptyList()) }
    var info by remember(o) { mutableStateOf<AgentSession?>(null) }
    var streaming by remember(o) { mutableStateOf("") }
    var connError by remember { mutableStateOf("") }
    // When what is shown is the copy kept on the phone (History), the time it
    // was kept; 0 once the machine's own events have arrived.
    var keptAt by remember(o) { mutableStateOf(0L) }
    var input by remember { mutableStateOf("") }
    var actionError by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    val list = rememberLazyListState()
    val ctx = LocalContext.current
    // Files to go with the next message: kept on the phone at once, and sent
    // with the message through the outbox — so a machine that is not there
    // holds up nothing, and loses nothing.
    var attached by remember(o) { mutableStateOf<List<Attachment>>(emptyList()) }
    var uploading by remember { mutableStateOf("") }
    val pickFile = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.GetContent(),
    ) { uri ->
        if (uri == null) return@rememberLauncherForActivityResult
        val name = displayName(ctx, uri)
        uploading = name
        scope.launch {
            withContext(Dispatchers.IO) {
                runCatching { ctx.contentResolver.openInputStream(uri)!!.use { Outbox.keep(ctx, name, it) } }
            }.onSuccess { attached = attached + it }.onFailure { actionError = it.message ?: "could not read $name" }
            uploading = ""
        }
    }
    // Voice notes: recorded here, transcribed on the agent's machine, and the
    // text put in the box to be read and corrected before it is sent.
    val recorder = remember { VoiceRecorder(ctx) }
    var recording by remember { mutableStateOf(false) }
    DisposableEffect(Unit) { onDispose { recorder.cancel() } }
    fun startRecording() {
        runCatching { recorder.start() }
            .onSuccess { recording = true }
            .onFailure { actionError = "could not record: ${it.message}" }
    }
    val askMic = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission(),
    ) { granted -> if (granted) startRecording() else actionError = "the microphone was not allowed" }
    // The outbox: what is written here goes there and is sent from there —
    // now if the machine answers, later if not (Outbox).
    val outbox by Outbox.changed.collectAsState()
    val queued = remember(outbox, o) {
        Outbox.list(ctx).filter { it.address == o.address && it.session == o.session }.sortedBy { it.created }
    }
    fun mine(q: Outgoing) = q.address == o.address && q.session == o.session
    fun enqueue(q: Outgoing) {
        Outbox.add(ctx, q)
        scope.launch { Outbox.flush(ctx, ::mine) }
    }
    // A voice note goes as a voice note: the agent transcribes it and sends
    // what was said, so there is nothing to wait for and read back here.
    fun stopAndQueue() {
        recording = false
        val f = recorder.stop() ?: return
        val id = Outbox.newId()
        val kept = java.io.File(Outbox.voiceDir(ctx), "$id.m4a")
        if (!f.renameTo(kept)) { f.copyTo(kept, overwrite = true); f.delete() }
        enqueue(Outgoing(id, o.address, o.host, o.session, "voice", file = kept.path, created = System.currentTimeMillis()))
    }
    LaunchedEffect(o) {
        while (isActive) {
            Outbox.flush(ctx, ::mine)
            delay(5_000)
        }
    }

    // Not notified about while it is on screen — and only while the app is in
    // front: a phone in a pocket on this screen should still buzz.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(o, lifecycle) {
        val key = "${o.address}/${o.session}"
        // Leaving it — back to the list, or the app to the background — reads
        // what was on screen: a reply that came since the last look at the
        // session's figures would otherwise count as unread.
        fun readNow() {
            kotlinx.coroutines.CoroutineScope(Dispatchers.IO).launch {
                runCatching { client.sessions() }.getOrNull()?.firstOrNull { it.name == o.session }
                    ?.let { Unread.seen(ctx, o.host, o.session, it.turns) }
            }
        }
        val obs = LifecycleEventObserver { _, e ->
            when (e) {
                Lifecycle.Event.ON_RESUME -> AgentWatch.visible = key
                Lifecycle.Event.ON_PAUSE -> if (AgentWatch.visible == key) { AgentWatch.visible = null; readNow() }
                else -> {}
            }
        }
        lifecycle.addObserver(obs)
        if (lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED)) AgentWatch.visible = key
        onDispose {
            lifecycle.removeObserver(obs)
            if (AgentWatch.visible == key) { AgentWatch.visible = null; readNow() }
        }
    }

    // What came before this agent had the conversation, once.
    LaunchedEffect(o) {
        withContext(Dispatchers.IO) { runCatching { client.history(o.session, 30) } }.onSuccess { earlier = it }
    }
    // Kept on the phone, as it is now, for when the machine cannot be reached.
    LaunchedEffect(o) {
        var saved = -1L to -1
        while (isActive) {
            delay(5_000)
            val now = (events.lastOrNull()?.seq ?: 0L) to earlier.size
            if (keptAt == 0L && events.isNotEmpty() && now != saved) {
                val ev = events.toList()
                val ea = earlier
                withContext(Dispatchers.IO) { History.save(ctx, o.host, o.session, ev, ea) }
                saved = now
            }
        }
    }
    // The session's own figures — context, model, auto-approve — kept fresh.
    LaunchedEffect(o) {
        while (isActive) {
            withContext(Dispatchers.IO) { runCatching { client.sessions() } }.getOrNull()
                ?.firstOrNull { it.name == o.session }?.let {
                    info = it
                    // Read: on screen, with the app in front.
                    if (AgentWatch.visible == "${o.address}/${o.session}") Unread.seen(ctx, o.host, o.session, it.turns)
                }
            delay(10_000)
        }
    }
    // Follow the session for as long as it is on screen. A dropped connection
    // is normal on mobile data: reconnect from the last event seen, which the
    // server keeps, so nothing is lost or shown twice.
    //
    // It opens at the last SESSION_TAIL events, and what arrives is applied in
    // batches: a long session replayed one event and one re-render at a time
    // scrolled through its own history for ten seconds before settling.
    // Made again since its copy was kept: start over without the copy.
    var copyStale by remember(o) { mutableStateOf(0) }
    LaunchedEffect(o, tail, copyStale) {
        events.clear()
        streaming = ""
        // The end of the conversation as it was last seen here, at once; then
        // only what came after it is asked for — usually nothing or a few
        // events. Replaying the last SESSION_TAIL instead, tool output and
        // all, took tens of seconds over the mesh, and the list was rebuilt
        // under the reader as it trickled in.
        var keptLast = 0L
        var keptFirst = 0L
        if (tail == SESSION_TAIL && copyStale == 0) withContext(Dispatchers.IO) { History.load(ctx, o.host, o.session) }?.let { h ->
            if (h.events.isNotEmpty()) {
                events.addAll(h.events)
                if (earlier.isEmpty()) earlier = h.earlier
                keptAt = h.saved
                keptLast = h.events.last().seq
                keptFirst = h.events.first().seq
            }
        }
        val after = java.util.concurrent.atomic.AtomicLong(keptLast)
        val answered = java.util.concurrent.atomic.AtomicBoolean(false)
        val pending = java.util.concurrent.ConcurrentLinkedQueue<AgentEvent>()
        // Without a copy, the opening replay is gathered and shown in one go
        // once it reaches the session's newest event.
        val replay = ArrayList<AgentEvent>()
        var replaying = keptLast == 0L
        var quiet = 0
        launch {
            while (isActive) {
                // The machine answered: what is shown is its own from here.
                if (answered.get() && keptAt != 0L) keptAt = 0
                val i = info
                // Its numbers went back below the copy's oldest: deleted and
                // made again, numbering restarted. The copy is of something
                // else. (Not against the copy's newest: the listed last event
                // trails a session that is talking.)
                if (keptFirst > 0 && i != null && i.lastSeq in 1 until keptFirst) {
                    withContext(Dispatchers.IO) { History.forget(ctx, o.host, o.session) }
                    copyStale++
                    return@launch
                }
                if (replaying) {
                    if (pending.isEmpty() && replay.isNotEmpty()) quiet++
                    if (AgentChat.replayCaughtUp(replay.lastOrNull()?.seq, i?.lastSeq ?: 0, quiet)) {
                        events.addAll(replay)
                        replay.clear()
                        replaying = false
                    }
                }
                if (pending.isNotEmpty()) {
                    val batch = ArrayList<AgentEvent>()
                    while (true) batch += pending.poll() ?: break
                    var text = streaming
                    val kept = ArrayList<AgentEvent>()
                    for (e in batch) {
                        if (e.kind == "partial") { text += e.data.optString("text"); continue }
                        // The whole message replaces what was streamed of it.
                        val t = e.data.optString("type")
                        if (e.kind == "claude" && (t == "assistant" || t == "result")) text = ""
                        kept += e
                    }
                    streaming = text
                    if (replaying) { replay.addAll(kept); quiet = 0 }
                    else if (kept.isNotEmpty()) {
                        events.addAll(kept)
                        Speech.heard(ctx, o.host, o.session, kept)
                    }
                    connError = ""
                }
                delay(120)
            }
        }
        while (isActive) {
            val r = withContext(Dispatchers.IO) {
                runCatching {
                    client.follow(o.session, after.get(), stop = { !isActive },
                        tail = tail, onOpen = { answered.set(true) }) { e ->
                        if (e.kind != "partial") after.set(e.seq)
                        pending += e
                    }
                }
            }
            r.onFailure { connError = it.message ?: "connection lost" }
            delay(2000)
        }
    }

    val items = remember(events.size, earlier) { AgentChat.items(events.toList(), earlier) }
    val keys = remember(items) { keysOf(items) }
    val last = items.lastOrNull()
    // A kept copy says nothing about now.
    val working = keptAt == 0L && (info?.state == "working" ||
        (items.isNotEmpty() && last !is ChatItem.Done && last !is ChatItem.Stopped &&
            last !is ChatItem.Earlier && last !is ChatItem.Note && last !is ChatItem.Voice &&
            !(last is ChatItem.Prompt && !last.open)))
    val waiting = items.any { it is ChatItem.Prompt && it.open }
    // Read aloud: what is being read now, and whether new replies are.
    val readingNow by Speech.reading.collectAsState()
    val autoTick by Speech.autoChanged.collectAsState()
    val autoPlay = remember(autoTick, o) { Speech.autoPlay(ctx, o.host, o.session) }
    // A reply starting to be read is brought into view: it changes height as
    // it turns into the reading view, and auto-play may start one off screen.
    LaunchedEffect(readingNow?.id) {
        val id = readingNow?.id ?: return@LaunchedEffect
        val prefix = "${o.host}/${o.session}/"
        if (!id.startsWith(prefix)) return@LaunchedEffect
        val seq = id.removePrefix(prefix).toLongOrNull() ?: return@LaunchedEffect
        AgentChat.listIndexOf(items, seq)?.let { list.animateScrollToItem(it) }
    }
    // Ends the turn running now, as Esc does in Claude Code's terminal; the
    // session stays and takes the next message.
    fun stopTurn() { scope.launch(Dispatchers.IO) { runCatching { client.interrupt(o.session) } } }

    // Laid out from the bottom, as chats are: the newest message is item 0, so
    // a reader at the bottom stays there as messages arrive and one who has
    // scrolled up stays where they are — the list keeps its place by key, with
    // no scrolling done in code. Scrolling in code is what kept throwing the
    // view to the start of the conversation.
    val scrolledUp by remember { derivedStateOf { list.firstVisibleItemIndex > 1 } }

    // A search result's event, once it is loaded: the one scroll done in code,
    // because somebody asked for it. Lit for a few seconds so it is seen.
    LaunchedEffect(jumpTo, items) {
        if (jumpTo == 0L) return@LaunchedEffect
        val at = AgentChat.listIndexOf(items, jumpTo) ?: return@LaunchedEffect
        list.scrollToItem(at)
        lit = jumpTo
        jumpTo = 0
        delay(4000)
        lit = 0
    }
    fun open(f: Found) {
        if (f.seq == 0L) { reading = f; return }
        searching = false
        val first = events.firstOrNull()?.seq ?: Long.MAX_VALUE
        if (f.seq < first) {
            val lastSeq = maxOf(info?.lastSeq ?: 0, events.lastOrNull()?.seq ?: 0)
            tail = AgentChat.tailReaching(tail, lastSeq, f.seq)
        }
        jumpTo = f.seq
    }
    fun runSearch() {
        val q = query.trim()
        if (q.isEmpty()) return
        searchBusy = true
        scope.launch {
            withContext(Dispatchers.IO) { runCatching { client.search(o.session, q) } }
                .onSuccess { found = it }
                .onFailure { actionError = it.message ?: "could not search" }
            searchBusy = false
        }
    }

    val i = info
    if (askDelete) {
        DeleteSessionDialog(o.host, o.address,
            i ?: AgentSession(o.session, "", "idle", 0, false, 0),
            onDismiss = { askDelete = false }, onDeleted = { askDelete = false; History.forget(ctx, o.host, o.session); Unread.forget(ctx, o.host, o.session); onBack() })
    }
    reading?.let { f ->
        androidx.compose.material3.AlertDialog(
            onDismissRequest = { reading = null },
            containerColor = Palette.Panel,
            title = {
                Stamp(listOf(if (f.role == "user") "YOU" else "CLAUDE", whenSaid(f.time), "before this agent")
                    .joinToString("  ·  "), copy = f.text)
            },
            text = {
                Column(Modifier.verticalScroll(rememberScrollState())) { MarkdownText(f.text) }
            },
            confirmButton = {
                Text("CLOSE", style = MaterialTheme.typography.labelSmall, color = Palette.Bone,
                    modifier = Modifier.clickable { reading = null }.padding(12.dp))
            },
        )
    }
    Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing).imePadding()) {
        // Header: the name on its own line, the facts under it, then actions.
        Column(Modifier.padding(start = 12.dp, end = 12.dp, top = 8.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("‹", style = MaterialTheme.typography.titleMedium.copy(fontSize = 22.sp), color = Palette.Phosphor,
                    modifier = Modifier.clickable { onBack() }.padding(horizontal = 8.dp, vertical = 4.dp))
                Text(o.session, style = MaterialTheme.typography.titleMedium, color = Palette.Bone,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f))
                when {
                    waiting -> { Pulse(Palette.Amber); Spacer(Modifier.width(6.dp)); Label("NEEDS YOU") }
                    working -> { Pulse(Palette.Phosphor); Spacer(Modifier.width(6.dp)); Label("WORKING") }
                }
            }
            val facts = listOf(o.host, o.mesh, harnessLabel(i?.harness ?: "claude"), shortModel(i?.model ?: ""),
                contextLabel(i?.contextUsed ?: 0, i?.contextWindow ?: 0)).filter { it.isNotEmpty() }
            Text(facts.joinToString("  ·  "), style = MaterialTheme.typography.labelSmall, color = Palette.Ash,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.padding(start = 30.dp))
            if (i != null && i.contextWindow > 0) {
                val f = (i.contextUsed.toFloat() / i.contextWindow).coerceIn(0f, 1f)
                Box(Modifier.padding(start = 30.dp, top = 6.dp, end = 8.dp).fillMaxWidth().height(2.dp).background(Palette.Line)) {
                    Box(Modifier.fillMaxWidth(f).height(2.dp).background(contextColour(i.contextUsed, i.contextWindow)))
                }
            }
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(start = 22.dp)) {
                val auto = i?.autoApprove == true
                // A harness that never asks has nothing to approve.
                if (i?.approves != false) Link(if (auto) "AUTO-APPROVE" else "asks first", if (auto) Palette.Phosphor else Palette.Ash) {
                    scope.launch {
                        withContext(Dispatchers.IO) { runCatching { client.setAutoApprove(o.session, !auto) } }
                            .onSuccess { info = i?.copy(autoApprove = !auto) }
                            .onFailure { actionError = it.message ?: "could not change it" }
                    }
                }
                Spacer(Modifier.weight(1f))
                Link(if (autoPlay) "AUTO-PLAY" else "auto-play", if (autoPlay) Palette.Phosphor else Palette.Ash) {
                    val last = maxOf(info?.lastSeq ?: 0, events.lastOrNull()?.seq ?: 0)
                    Speech.setAutoPlay(ctx, o.host, o.session, !autoPlay, last)
                }
                if (working) Link("■ stop", Palette.Rust) { stopTurn() }
                Link(if (searching) "close search" else "search", Palette.Sky) { searching = !searching }
                Link("delete", Palette.Ash) { askDelete = true }
            }
            if (keptAt != 0L) {
                Text("offline — as it was ${whenSaid(keptAt)}", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
                    modifier = Modifier.padding(start = 30.dp))
            }
            if (connError.isNotEmpty()) {
                Text("reconnecting — $connError", style = MaterialTheme.typography.labelSmall, color = Palette.Amber,
                    modifier = Modifier.padding(start = 30.dp))
            }
        }

        if (searching) Column(Modifier.weight(1f).padding(horizontal = 14.dp)) {
            OutlinedTextField(
                value = query, onValueChange = { query = it },
                placeholder = { Text("search the whole conversation", color = Palette.Ash) },
                textStyle = MaterialTheme.typography.bodyMedium.copy(color = Palette.Bone),
                singleLine = true,
                keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(
                    imeAction = androidx.compose.ui.text.input.ImeAction.Search),
                keyboardActions = androidx.compose.foundation.text.KeyboardActions(onSearch = { runSearch() }),
                trailingIcon = {
                    if (searchBusy) Pulse(Palette.Sky, 10)
                    else Text("go", color = Palette.Sky, style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.clickable { runSearch() }.padding(12.dp))
                },
                colors = OutlinedTextFieldDefaults.colors(
                    focusedBorderColor = Palette.Sky, unfocusedBorderColor = Palette.Line, cursorColor = Palette.Sky),
                shape = RoundedCornerShape(14.dp),
                modifier = Modifier.fillMaxWidth().padding(vertical = 6.dp),
            )
            val f = found
            when {
                f == null -> Label("Words anywhere in what was typed or answered — also before this agent had it. Case and accents do not matter.")
                f.isEmpty() -> Label("Nothing found.")
                else -> {
                    Label(if (f.size >= 100) "the newest 100" else "${f.size} found")
                    LazyColumn(verticalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.padding(top = 6.dp)) {
                        items(f.size) { n ->
                            val r = f[n]
                            Column(Modifier.fillMaxWidth()
                                .border(1.dp, Palette.Line, RoundedCornerShape(10.dp))
                                .clickable { open(r) }.padding(10.dp)) {
                                Text(listOf(if (r.role == "user") "YOU" else "CLAUDE", whenSaid(r.time),
                                    if (r.seq == 0L) "before this agent" else "").filter { it.isNotEmpty() }.joinToString("  ·  "),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = if (r.role == "user") Palette.Phosphor else Palette.Ash)
                                Text(r.snippet, style = MaterialTheme.typography.bodySmall, color = Palette.Bone,
                                    maxLines = 4, overflow = TextOverflow.Ellipsis)
                            }
                        }
                    }
                }
            }
        } else Box(Modifier.weight(1f)) {
            LazyColumn(
                state = list,
                reverseLayout = true,
                verticalArrangement = Arrangement.spacedBy(10.dp),
                modifier = Modifier.fillMaxSize().padding(horizontal = 14.dp),
            ) {
                // At the very bottom: what is written and not sent yet.
                items(queued.size, key = { "q-" + queued[queued.size - 1 - it].id }) { r ->
                    val q = queued[queued.size - 1 - r]
                    QueuedRow(q, o.host) { Outbox.remove(ctx, q.id) }
                }
                // Then what is happening now.
                item(key = "live") {
                    Column {
                        if (streaming.isNotEmpty()) Bubble(Palette.Panel) {
                            MarkdownText(streaming)
                            Text("▍", color = Palette.Phosphor, style = MaterialTheme.typography.bodyMedium)
                        }
                        // Where the eye is while it works: stopping the reply is
                        // offered here too — a bare "stop" in the header did not
                        // say what it stopped.
                        if (working && !waiting) Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(4.dp)) {
                            Pulse(Palette.Phosphor); Spacer(Modifier.width(8.dp))
                            Label(if (streaming.isEmpty()) "thinking…" else "writing…")
                            Spacer(Modifier.width(14.dp))
                            Link("■ stop", Palette.Rust) { stopTurn() }
                        } else if (streaming.isEmpty()) Spacer(Modifier.height(4.dp))
                    }
                }
                // Then newest to oldest. Stable keys: history arriving, or a
                // prompt being answered, must not move what is being read.
                items(items.size, key = { keys[items.size - 1 - it] }) { r ->
                    val idx = items.size - 1 - r
                    val item = items[idx]
                    val prev = items.getOrNull(idx - 1)
                    if (item is ChatItem.Earlier && prev !is ChatItem.Earlier) Label("— earlier, from the transcript —")
                    if (item !is ChatItem.Earlier && prev is ChatItem.Earlier) Label("— on this phone —")
                    Box(if (lit != 0L && item.seq == lit && item !is ChatItem.Earlier)
                        Modifier.border(2.dp, Palette.Sky, RoundedCornerShape(12.dp)) else Modifier) {
                        ChatRow(item, onAnswer = { prompt, allow, answers ->
                            scope.launch(Dispatchers.IO) {
                                runCatching { client.answer(o.session, prompt, allow, answers = answers) }
                                    .onFailure { actionError = it.message ?: "could not answer" }
                            }
                        }, onRetryVoice = { id ->
                            scope.launch(Dispatchers.IO) {
                                runCatching { client.retryVoice(o.session, id) }
                                    .onFailure { actionError = it.message ?: "could not transcribe it again" }
                            }
                        }, reading = readingNow?.takeIf { item is ChatItem.Said && it.id == "${o.host}/${o.session}/${item.seq}" },
                            onRead = { said -> Speech.say(ctx, "${o.host}/${o.session}/${said.seq}", said.text) })
                    }
                }
                // At the top (the list is laid out from the bottom): what was
                // left out, and how to have it.
                val firstSeq = events.firstOrNull()?.seq ?: 0
                if (tail != 0 && firstSeq > 1) {
                    item(key = "earlier-events") {
                        Text("— ${firstSeq - 1} earlier events not loaded · load them —",
                            style = MaterialTheme.typography.labelSmall, color = Palette.Phosphor,
                            modifier = Modifier.fillMaxWidth().clickable { tail = 0 }.padding(vertical = 12.dp))
                    }
                }
            }
            // Nothing to show yet: say what is happening rather than a blank
            // screen — a session with no copy kept here waits for its machine.
            if (items.isEmpty() && streaming.isEmpty() && queued.isEmpty()) {
                Row(Modifier.align(Alignment.Center), verticalAlignment = Alignment.CenterVertically) {
                    val empty = info?.lastSeq == 0L
                    if (!empty) { Pulse(Palette.Ash, 6); Spacer(Modifier.width(8.dp)) }
                    Label(when {
                        empty -> "no messages yet"
                        connError.isNotEmpty() -> "reaching ${o.host}… — $connError"
                        else -> "loading the conversation…"
                    })
                }
            }
            if (scrolledUp) {
                Box(Modifier.align(Alignment.BottomEnd).padding(16.dp).size(40.dp)
                    .background(Palette.Panel, CircleShape).border(1.dp, Palette.Phosphor, CircleShape)
                    .clickable { scope.launch { list.animateScrollToItem(0) } },
                    contentAlignment = Alignment.Center) {
                    Text("↓", color = Palette.Phosphor, style = MaterialTheme.typography.titleMedium)
                }
            }
        }

        // Reading this session aloud: the controls stay here, in reach however
        // far the reply being read is scrolled; "show" brings it back.
        readingNow?.takeIf { it.id.startsWith("${o.host}/${o.session}/") }?.let { r ->
            ReadingBar(r) {
                val seq = r.id.substringAfterLast('/').toLongOrNull()
                if (seq != null) AgentChat.listIndexOf(items, seq)?.let { at -> scope.launch { list.animateScrollToItem(at) } }
            }
        }
        if (actionError.isNotEmpty()) {
            Text(actionError, style = MaterialTheme.typography.bodySmall, color = Palette.Rust,
                modifier = Modifier.padding(horizontal = 20.dp).clickable { actionError = "" })
        }
        if (attached.isNotEmpty() || uploading.isNotEmpty()) {
            Row(Modifier.padding(start = 14.dp, end = 14.dp, top = 6.dp).horizontalScroll(rememberScrollState()),
                horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                for (a in attached) {
                    Text("📎 ${a.name}  ×",
                        style = MaterialTheme.typography.labelSmall, color = Palette.Sky,
                        modifier = Modifier.border(1.dp, Palette.Sky.copy(alpha = 0.5f), RoundedCornerShape(10.dp))
                            .clickable { attached = attached - a; java.io.File(a.file).delete() }.padding(horizontal = 10.dp, vertical = 6.dp))
                }
                if (uploading.isNotEmpty()) Row(verticalAlignment = Alignment.CenterVertically) {
                    Pulse(Palette.Sky); Spacer(Modifier.width(6.dp)); Label("adding $uploading…")
                }
            }
        }
        Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(10.dp)) {
            ComposerButton("📎") { pickFile.launch("*/*") }
            when {
                recording -> Box(Modifier.size(40.dp).clickable { stopAndQueue() }, contentAlignment = Alignment.Center) {
                    Pulse(Palette.Rust, 16)
                    Text("■", color = Palette.Bone, style = MaterialTheme.typography.labelSmall)
                }
                else -> ComposerButton("🎤") {
                    val has = androidx.core.content.ContextCompat.checkSelfPermission(ctx, android.Manifest.permission.RECORD_AUDIO) ==
                        android.content.pm.PackageManager.PERMISSION_GRANTED
                    if (has) startRecording() else askMic.launch(android.Manifest.permission.RECORD_AUDIO)
                }
            }
            Box(Modifier.weight(1f)) {
                OutlinedTextField(
                    value = input, onValueChange = { input = it },
                    placeholder = { Text("message", color = Palette.Ash) },
                    textStyle = MaterialTheme.typography.bodyMedium.copy(color = Palette.Bone),
                    colors = OutlinedTextFieldDefaults.colors(
                        focusedBorderColor = Palette.Phosphor, unfocusedBorderColor = Palette.Line,
                        cursorColor = Palette.Phosphor,
                    ),
                    shape = RoundedCornerShape(14.dp),
                    maxLines = 6,
                    modifier = Modifier.fillMaxWidth(),
                )
            }
            Spacer(Modifier.width(8.dp))
            val canSend = (input.isNotBlank() || attached.isNotEmpty()) && uploading.isEmpty()
            Box(
                Modifier.size(44.dp)
                    .background(if (canSend) Palette.Phosphor else Palette.Line, CircleShape)
                    .clickable(enabled = canSend) {
                        val text = input.trim()
                        val files = attached
                        input = ""
                        attached = emptyList()
                        actionError = ""
                        enqueue(Outgoing(Outbox.newId(), o.address, o.host, o.session, "text", text = text,
                            created = System.currentTimeMillis(), attachments = files))
                    },
                contentAlignment = Alignment.Center,
            ) { Text("↑", color = Palette.Void, style = MaterialTheme.typography.titleMedium) }
        }
    }
}

/**
 * A reply while it is read aloud: as the sentences being read, the current
 * one lit, so a reply full of paths and line numbers can be followed by eye;
 * and the controls — pause or resume, back and on a sentence, stop.
 */
@Composable
private fun ReadingView(r: Speech.Reading) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        val text = androidx.compose.ui.text.buildAnnotatedString {
            r.sentences.forEachIndexed { i, sentence ->
                // A sentence that starts a line (Speech.sentences) keeps it.
                if (i > 0 && !sentence.startsWith("\n")) append(" ")
                if (i == r.index) {
                    pushStyle(androidx.compose.ui.text.SpanStyle(color = Palette.Void, background = Palette.Sky))
                    append(sentence)
                    pop()
                } else {
                    pushStyle(androidx.compose.ui.text.SpanStyle(color = if (i < r.index) Palette.Ash else Palette.Bone))
                    append(sentence)
                    pop()
                }
            }
        }
        Text(text, style = MaterialTheme.typography.bodyMedium)
    }
}

/**
 * The read-aloud voice: which engine reads now, and how to get a natural one —
 * SherpaTTS with a Piper voice, chosen as the system's engine — with a button
 * for each step and one to try it (docs/agents-voices.md). The engine is the
 * system's, so every app that reads aloud gains the voice too.
 */
@Composable
private fun VoiceDialog(onClose: () -> Unit) {
    val ctx = LocalContext.current
    var engines by remember { mutableStateOf<List<android.speech.tts.TextToSpeech.EngineInfo>?>(null) }
    var preferred by remember { mutableStateOf<String?>(null) }
    var check by remember { mutableStateOf(0) }
    // Checked again on coming back from the settings or F-Droid.
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(lifecycle) {
        val obs = LifecycleEventObserver { _, e -> if (e == Lifecycle.Event.ON_RESUME) check++ }
        lifecycle.addObserver(obs)
        onDispose { lifecycle.removeObserver(obs) }
    }
    LaunchedEffect(check) {
        Speech.rebind()
        Speech.engines(ctx) { list, default -> engines = list; preferred = default }
    }
    val sherpa = engines?.any { it.name == Speech.SHERPA } == true
    val usingSherpa = preferred == Speech.SHERPA
    val label = engines?.firstOrNull { it.name == preferred }?.label ?: preferred ?: "…"
    fun open(i: android.content.Intent) = runCatching { ctx.startActivity(i.addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)) }.isSuccess
    androidx.compose.material3.AlertDialog(
        onDismissRequest = onClose,
        containerColor = Palette.Panel,
        title = { Text("READ-ALOUD VOICE", style = MaterialTheme.typography.labelMedium, color = Palette.Sky) },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text("Replies are read by $label" + if (usingSherpa) " — a natural voice." else ".",
                    style = MaterialTheme.typography.bodyMedium, color = Palette.Bone)
                if (!usingSherpa) {
                    Text("For a natural voice, offline: SherpaTTS with a Piper voice, as Android's speech engine. " +
                        "Every app that reads aloud gets it too.", style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                    VoiceStep("1", if (sherpa) "SherpaTTS is installed ✓" else "Install SherpaTTS from F-Droid", done = sherpa,
                        action = if (sherpa) null else "F-Droid") {
                        // F-Droid's own page: the F-Droid app takes the link
                        // when installed, a browser otherwise. Not market://,
                        // which the Play Store claims — SherpaTTS is F-Droid's.
                        val page = android.net.Uri.parse("https://f-droid.org/packages/${Speech.SHERPA}/")
                        open(android.content.Intent(android.content.Intent.ACTION_VIEW, page).setPackage("org.fdroid.fdroid")) ||
                            open(android.content.Intent(android.content.Intent.ACTION_VIEW, page))
                    }
                    VoiceStep("2", "In it, pick an English voice — en_US lessac or ryan (medium or high)", done = false,
                        action = if (sherpa) "open" else null) {
                        ctx.packageManager.getLaunchIntentForPackage(Speech.SHERPA)?.let { open(it) }
                    }
                    VoiceStep("3", "Make it the preferred engine: Text-to-speech → Preferred engine → SherpaTTS",
                        done = usingSherpa, action = "settings") {
                        open(android.content.Intent("com.android.settings.TTS_SETTINGS")) ||
                            open(android.content.Intent(android.provider.Settings.ACTION_SETTINGS))
                    }
                }
            }
        },
        confirmButton = {
            Row {
                Text("▶ try", style = MaterialTheme.typography.labelMedium, color = Palette.Sky,
                    modifier = Modifier.clickable { Speech.say(ctx, "voice-test", "This is how replies will sound. Fixed in session.go, line 654.") }
                        .padding(12.dp))
                Text("CLOSE", style = MaterialTheme.typography.labelMedium, color = Palette.Bone,
                    modifier = Modifier.clickable { Speech.stop(); onClose() }.padding(12.dp))
            }
        },
    )
}

@Composable
private fun VoiceStep(n: String, text: String, done: Boolean, action: String?, onAction: () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text(n, style = MaterialTheme.typography.labelMedium, color = if (done) Palette.Phosphor else Palette.Sky,
            modifier = Modifier.padding(end = 10.dp))
        Text(text, style = MaterialTheme.typography.bodySmall, color = if (done) Palette.Ash else Palette.Bone,
            modifier = Modifier.weight(1f))
        if (action != null) Text(action, style = MaterialTheme.typography.labelMedium, color = Palette.Phosphor,
            modifier = Modifier.clickable(onClick = onAction).padding(start = 8.dp, top = 6.dp, bottom = 6.dp))
    }
}

/** The controls for what is being read, above the message box. */
@Composable
private fun ReadingBar(r: Speech.Reading, onShow: () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 4.dp)
            .background(Palette.Panel, RoundedCornerShape(10.dp)).border(1.dp, Palette.Sky.copy(alpha = 0.4f), RoundedCornerShape(10.dp))
            .padding(horizontal = 8.dp)) {
        Pulse(if (r.paused) Palette.Ash else Palette.Sky, 6)
        Spacer(Modifier.width(6.dp))
        Text("${r.index + 1} / ${r.sentences.size}", style = MaterialTheme.typography.labelSmall, color = Palette.Ash, maxLines = 1)
        ReadControl("show", Palette.Ash, onShow)
        Spacer(Modifier.weight(1f))
        ReadControl("⏮") { Speech.skip(-1) }
        ReadControl(if (r.paused) "▶ resume" else "⏸ pause", Palette.Sky) { if (r.paused) Speech.resume() else Speech.pause() }
        ReadControl("⏭") { Speech.skip(1) }
        ReadControl("■", Palette.Rust) { Speech.stop() }
    }
}

@Composable
private fun ReadControl(label: String, colour: Color = Palette.Bone, onClick: () -> Unit) =
    Text(label, style = MaterialTheme.typography.labelMedium, color = colour, maxLines = 1, softWrap = false,
        modifier = Modifier.clickable(onClick = onClick).padding(horizontal = 8.dp, vertical = 6.dp))

/**
 * Keys that do not change when an item does — a prompt being answered, say —
 * so the list never re-lays itself out under the reader. One event can make
 * several items (text and a tool use in one message), so an item is its
 * event's number and its position among that event's items.
 */
fun keysOf(items: List<ChatItem>): List<String> {
    val seen = mutableMapOf<Long, Int>()
    var earlier = 0
    return items.map {
        if (it is ChatItem.Earlier) "h${earlier++}"
        else { val n = seen.merge(it.seq, 1, Int::plus)!!; "e${it.seq}-$n" }
    }
}

/**
 * A message or voice note in the outbox: written, not yet sent — the machine
 * is unreachable, or this is being sent now. Cancel takes it back.
 */
@Composable
private fun QueuedRow(q: Outgoing, host: String, onCancel: () -> Unit) {
    Bubble(Palette.Phosphor.copy(alpha = 0.04f), Palette.Phosphor.copy(alpha = 0.25f)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Pulse(Palette.Ash, 6)
            Spacer(Modifier.width(6.dp))
            Text(queuedLabel(q, host), style = MaterialTheme.typography.labelSmall, color = Palette.Ash,
                modifier = Modifier.weight(1f), maxLines = 2, overflow = TextOverflow.Ellipsis)
            Text("cancel", style = MaterialTheme.typography.labelSmall, color = Palette.Rust,
                modifier = Modifier.clickable(onClick = onCancel).padding(start = 8.dp))
        }
        if (q.kind == "voice" || q.text.isNotEmpty())
            Text(if (q.kind == "voice") "🎤 voice note" else q.text, style = MaterialTheme.typography.bodyMedium,
                color = Palette.Bone.copy(alpha = 0.7f))
        for (a in q.attachments) Text((if (a.sent.isEmpty()) "📎 " else "📎 ✓ ") + a.name,
            style = MaterialTheme.typography.labelSmall, color = Palette.Sky, modifier = Modifier.padding(top = 4.dp))
    }
}

/** What a queued row says about where it is. */
fun queuedLabel(q: Outgoing, host: String): String =
    if (q.lastError.isEmpty()) "QUEUED · sending to $host…" else "QUEUED · waiting for $host — ${q.lastError}"

@Composable
private fun Bubble(bg: Color, border: Color = Palette.Line, content: @Composable () -> Unit) {
    // Selectable: long-press any part of a message to copy just that.
    androidx.compose.foundation.text.selection.SelectionContainer {
        // Multiplied, not replaced: a tint's own faintness is the point of it.
        // Replacing it made "you" bubbles solid Phosphor under light text.
        Column(Modifier.fillMaxWidth().background(bg.copy(alpha = bg.alpha * 0.9f), RoundedCornerShape(12.dp))
            .border(1.dp, border, RoundedCornerShape(12.dp)).padding(12.dp)) { content() }
    }
}

@Composable
private fun Stamp(text: String, colour: Color = Palette.Ash, copy: String? = null, extra: (@Composable () -> Unit)? = null) {
    Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(bottom = 4.dp)) {
        Text(text, style = MaterialTheme.typography.labelSmall, color = colour, modifier = Modifier.weight(1f))
        extra?.invoke()
        if (copy != null) CopyLink(copy)
    }
}

/**
 * Copies a whole message in one tap — the markdown as written, so it pastes
 * into a terminal or another chat intact — and says so, since a copy with no
 * feedback looks like a tap that missed.
 */
@Composable
private fun CopyLink(text: String) {
    val clipboard = androidx.compose.ui.platform.LocalClipboardManager.current
    var copied by remember { mutableStateOf(false) }
    LaunchedEffect(copied) { if (copied) { delay(1200); copied = false } }
    androidx.compose.foundation.text.selection.DisableSelection {
        Text(if (copied) "copied" else "copy", style = MaterialTheme.typography.labelSmall,
            color = if (copied) Palette.Phosphor else Palette.Ash,
            modifier = Modifier.clickable {
                clipboard.setText(AnnotatedString(text))
                copied = true
            }.padding(start = 12.dp, top = 2.dp, bottom = 2.dp))
    }
}

@Composable
private fun ChatRow(item: ChatItem, onAnswer: (String, Boolean, Map<String, String>?) -> Unit,
                    onRetryVoice: (String) -> Unit = {}, reading: Speech.Reading? = null, onRead: ((ChatItem.Said) -> Unit)? = null) {
    when (item) {
        is ChatItem.You -> Bubble(Palette.Phosphor.copy(alpha = 0.08f), Palette.Phosphor.copy(alpha = 0.35f)) {
            Stamp(listOf(if (item.voice) "YOU 🎤" else "YOU", item.by, whenSaid(item.time)).filter { it.isNotEmpty() }.joinToString("  ·  "),
                Palette.Phosphor, copy = item.text)
            Text(spans(Markdown.links(item.text)), style = MaterialTheme.typography.bodyMedium)
        }
        is ChatItem.Said -> Bubble(Palette.Panel) {
            Stamp(whenSaid(item.time), copy = item.text) {
                // Read aloud (Speech); while it is, the controls are below.
                if (onRead != null && reading == null) Text("▶ listen", style = MaterialTheme.typography.labelSmall,
                    color = Palette.Sky,
                    modifier = Modifier.clickable { onRead(item) }.padding(start = 6.dp, end = 10.dp, top = 2.dp, bottom = 2.dp))
            }
            if (reading == null) MarkdownText(item.text) else ReadingView(reading)
        }
        is ChatItem.Earlier -> if (item.user) {
            Bubble(Palette.Phosphor.copy(alpha = 0.05f), Palette.Line) {
                Stamp(listOf("YOU", whenSaid(item.time)).joinToString("  ·  "), Palette.Phosphor.copy(alpha = 0.6f),
                    copy = item.text)
                Text(item.text, style = MaterialTheme.typography.bodyMedium, color = Palette.Bone.copy(alpha = 0.8f))
            }
        } else {
            Bubble(Palette.Panel.copy(alpha = 0.6f)) {
                Stamp(whenSaid(item.time), copy = item.text)
                MarkdownText(item.text)
            }
        }
        // The first line of what it ran; the rest on a tap — a long script
        // filled the screen (2026-10-03).
        is ChatItem.Tool -> {
            var expanded by remember { mutableStateOf(false) }
            val more = item.summary.contains('\n') || item.summary.length > 120
            Row(verticalAlignment = Alignment.Top, modifier = Modifier.clickable(enabled = more) { expanded = !expanded }) {
                Text("▸ ", style = MaterialTheme.typography.bodySmall, color = Palette.Violet)
                Text(buildAnnotatedString {
                    withStyle(SpanStyle(color = Palette.Violet, fontWeight = FontWeight.Medium)) { append(item.name) }
                    append("  ")
                    withStyle(SpanStyle(color = Palette.Sky)) {
                        append(if (expanded) item.summary else item.summary.lineSequence().first().take(120) + if (more) "  …" else "")
                    }
                }, style = MaterialTheme.typography.bodySmall,
                    maxLines = if (expanded) Int.MAX_VALUE else 1, overflow = TextOverflow.Ellipsis)
            }
        }
        is ChatItem.Output -> {
            var expanded by remember { mutableStateOf(false) }
            val firstLine = item.text.lineSequence().firstOrNull().orEmpty().take(120)
            val more = item.text.contains('\n') || item.text.length > 120
            androidx.compose.foundation.text.selection.SelectionContainer { Text(
                if (expanded) item.text else firstLine + if (more) "  …" else "",
                style = MaterialTheme.typography.bodySmall,
                color = if (item.error) Palette.Rust else Palette.Ash,
                modifier = Modifier.clickable(enabled = more) { expanded = !expanded }.padding(start = 16.dp),
            ) }
        }
        is ChatItem.Prompt -> if (item.questions.isNotEmpty()) QuestionCard(item, onAnswer) else PromptCard(item, onAnswer)
        is ChatItem.Done -> Label("— ${item.note}${whenSaid(item.time).let { if (it.isEmpty()) "" else "  ·  $it" }}")
        is ChatItem.Stopped -> Label("— asleep; the next message wakes it")
        is ChatItem.Note -> Label("— ${item.text}")
        is ChatItem.Voice -> if (!item.failed) {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(4.dp)) {
                Pulse(Palette.Sky); Spacer(Modifier.width(8.dp))
                Label("🎤 voice note — transcribing on the agent's machine…")
            }
        } else Column(Modifier.padding(4.dp)) {
            Text("🎤 the voice note could not be transcribed: ${item.error}", style = MaterialTheme.typography.bodySmall,
                color = Palette.Rust)
            Text("transcribe again", style = MaterialTheme.typography.labelSmall, color = Palette.Sky,
                modifier = Modifier.clickable { onRetryVoice(item.id) }.padding(vertical = 6.dp))
        }
    }
}

/**
 * The reason this screen exists: something on another machine wants to run,
 * and waits for you. What it would do is shown in full before the buttons —
 * the summary is what you are saying yes to.
 */
@Composable
private fun PromptCard(p: ChatItem.Prompt, onAnswer: (String, Boolean, Map<String, String>?) -> Unit) {
    Column(
        Modifier.fillMaxWidth()
            .background(Palette.Panel, RoundedCornerShape(12.dp))
            .border(1.dp, if (p.open) Palette.Amber else Palette.Line, RoundedCornerShape(12.dp))
            .padding(14.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (p.open) { Pulse(Palette.Amber); Spacer(Modifier.width(8.dp)) }
            Text(if (p.open) "${p.tool.uppercase()} WANTS TO RUN" else p.tool,
                style = MaterialTheme.typography.labelSmall, color = if (p.open) Palette.Amber else Palette.Ash)
        }
        CodeBox(p.summary)
        if (p.description.isNotEmpty() && p.description != p.summary) {
            Text(p.description, style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
        }
        if (p.open) {
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Box(Modifier.weight(1f)) { Action("ALLOW", enabled = true) { onAnswer(p.id, true, null) } }
                Box(Modifier.weight(1f)) { Action("DENY", enabled = true, danger = true) { onAnswer(p.id, false, null) } }
            }
        } else {
            Label(p.answer)
        }
    }
}

/**
 * The model asking something (AskUserQuestion): its questions with the
 * options it offers, picked by tapping — several where it allows — or
 * answered in your own words, then sent together.
 */
@Composable
private fun QuestionCard(p: ChatItem.Prompt, onAnswer: (String, Boolean, Map<String, String>?) -> Unit) {
    var picked by remember(p.id) { mutableStateOf(mapOf<String, Set<String>>()) }
    var typed by remember(p.id) { mutableStateOf(mapOf<String, String>()) }
    val answers = AgentChat.answersFor(p.questions, picked, typed)
    Column(
        Modifier.fillMaxWidth()
            .background(Palette.Panel, RoundedCornerShape(12.dp))
            .border(1.dp, if (p.open) Palette.Sky else Palette.Line, RoundedCornerShape(12.dp))
            .padding(14.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            if (p.open) { Pulse(Palette.Sky); Spacer(Modifier.width(8.dp)) }
            Text(if (p.open) "CLAUDE ASKS" else "CLAUDE ASKED", style = MaterialTheme.typography.labelSmall,
                color = if (p.open) Palette.Sky else Palette.Ash)
        }
        for (q in p.questions) {
            Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                if (q.header.isNotEmpty()) Label(q.header.uppercase() + if (q.multi) "  ·  pick any" else "")
                Text(q.question, style = MaterialTheme.typography.bodyMedium, color = Palette.Bone)
                if (p.open) {
                    for (o in q.options) {
                        val on = o.label in picked[q.question].orEmpty()
                        Column(
                            Modifier.fillMaxWidth()
                                .border(1.dp, if (on) Palette.Sky else Palette.Line, RoundedCornerShape(10.dp))
                                .background(if (on) Palette.Sky.copy(alpha = 0.12f) else Color.Transparent, RoundedCornerShape(10.dp))
                                .clickable {
                                    val cur = picked[q.question].orEmpty()
                                    val next = when {
                                        q.multi && on -> cur - o.label
                                        q.multi -> cur + o.label
                                        else -> setOf(o.label)
                                    }
                                    picked = picked + (q.question to next)
                                    typed = typed - q.question
                                }
                                .padding(horizontal = 12.dp, vertical = 8.dp),
                        ) {
                            Text(o.label, style = MaterialTheme.typography.bodyMedium, color = if (on) Palette.Sky else Palette.Bone)
                            if (o.description.isNotEmpty()) {
                                Text(o.description, style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                            }
                        }
                    }
                    OutlinedTextField(
                        value = typed[q.question].orEmpty(),
                        onValueChange = { v -> typed = typed + (q.question to v); if (v.isNotBlank()) picked = picked - q.question },
                        placeholder = { Text("or in your own words", color = Palette.Ash) },
                        textStyle = MaterialTheme.typography.bodySmall.copy(color = Palette.Bone),
                        colors = OutlinedTextFieldDefaults.colors(
                            focusedBorderColor = Palette.Sky, unfocusedBorderColor = Palette.Line, cursorColor = Palette.Sky),
                        shape = RoundedCornerShape(10.dp),
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
            }
        }
        if (p.open) {
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                Box(Modifier.weight(1f)) {
                    // Closes when the answer is in the session's events.
                    Action("ANSWER", enabled = answers != null) { onAnswer(p.id, true, answers) }
                }
                Box(Modifier.weight(1f)) {
                    Action("DECLINE", enabled = true, danger = true) { onAnswer(p.id, false, null) }
                }
            }
        } else {
            Label(p.answer)
        }
    }
}

// --- markdown ---------------------------------------------------------------

@Composable
private fun CodeBox(text: String) = androidx.compose.foundation.text.selection.SelectionContainer {
    Box(Modifier.fillMaxWidth().background(Palette.Void, RoundedCornerShape(8.dp))
        .border(1.dp, Palette.Line, RoundedCornerShape(8.dp))
        .horizontalScroll(rememberScrollState()).padding(10.dp)) {
        Text(text, style = MaterialTheme.typography.bodySmall, color = Palette.Chartreuse, softWrap = false)
    }
}

private fun spans(s: List<Markdown.Span>, base: Color = Palette.Bone): AnnotatedString = buildAnnotatedString {
    for (sp in s) {
        val style = SpanStyle(
            color = when {
                sp.code -> Palette.Chartreuse
                sp.link != null -> Palette.Sky
                sp.bold -> Color.White
                else -> base
            },
            fontWeight = if (sp.bold) FontWeight.Bold else null,
            fontStyle = if (sp.italic) FontStyle.Italic else null,
            background = if (sp.code) Palette.Void else Color.Unspecified,
            textDecoration = if (sp.link != null) TextDecoration.Underline else null,
        )
        if (sp.link != null && isWebLink(sp.link)) {
            withLink(androidx.compose.ui.text.LinkAnnotation.Url(sp.link,
                androidx.compose.ui.text.TextLinkStyles(style))) { append(sp.text) }
        } else {
            withStyle(style) { append(sp.text) }
        }
    }
}

/** Only web links open on tap; anything else stays text. */
private fun isWebLink(u: String) = u.startsWith("http://") || u.startsWith("https://")

/** Claude's markdown, in the app's own look. */
@Composable
fun MarkdownText(src: String) {
    val blocks = remember(src) { Markdown.parse(src) }
    Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
        for (b in blocks) when (b) {
            is Markdown.Block.Heading -> Text(spans(b.text, Palette.Phosphor),
                style = MaterialTheme.typography.titleMedium.copy(fontSize = if (b.level <= 2) 15.sp else 13.sp),
                modifier = Modifier.padding(top = 4.dp))
            is Markdown.Block.Para -> Text(spans(b.text), style = MaterialTheme.typography.bodyMedium)
            is Markdown.Block.Item -> Row(Modifier.padding(start = (b.indent * 14).dp)) {
                Text(b.marker + " ", style = MaterialTheme.typography.bodyMedium, color = Palette.Phosphor)
                Text(spans(b.text), style = MaterialTheme.typography.bodyMedium)
            }
            is Markdown.Block.Quote -> Row {
                Box(Modifier.width(2.dp).height(18.dp).background(Palette.Violet))
                Spacer(Modifier.width(8.dp))
                Text(spans(b.text, Palette.Ash), style = MaterialTheme.typography.bodyMedium)
            }
            is Markdown.Block.Code -> CodeBox(b.text)
            is Markdown.Block.Table -> Column(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState())
                .border(1.dp, Palette.Line, RoundedCornerShape(8.dp)).padding(8.dp)) {
                b.rows.forEachIndexed { r, row ->
                    Row {
                        row.forEach { cell ->
                            Text(spans(cell, if (r == 0) Palette.Phosphor else Palette.Bone),
                                style = MaterialTheme.typography.bodySmall,
                                modifier = Modifier.width(140.dp).padding(end = 10.dp, bottom = 4.dp))
                        }
                    }
                }
            }
            Markdown.Block.Rule -> Box(Modifier.fillMaxWidth().height(1.dp).background(Palette.Line))
        }
    }
}

@Composable
private fun ComposerButton(glyph: String, onClick: () -> Unit) {
    Box(Modifier.size(40.dp).clickable { onClick() }, contentAlignment = Alignment.Center) {
        Text(glyph, style = MaterialTheme.typography.titleMedium)
    }
}

/** What a picked file is called, as the phone shows it. */
private fun displayName(ctx: android.content.Context, uri: android.net.Uri): String =
    runCatching {
        ctx.contentResolver.query(uri, arrayOf(android.provider.OpenableColumns.DISPLAY_NAME), null, null, null)
            ?.use { c -> if (c.moveToFirst()) c.getString(0) else null }
    }.getOrNull() ?: uri.lastPathSegment ?: "file"

/**
 * A message with the files sent alongside it named at the end, by the path on
 * the agent's machine — which is all Claude Code needs to read them.
 */
fun withAttachments(text: String, paths: List<String>): String =
    if (paths.isEmpty()) text
    else (if (text.isEmpty()) "" else "$text\n\n") +
        "Attached from my phone (on this machine):\n" + paths.joinToString("\n") { "- $it" }

/**
 * Asks before deleting a session, and says what is lost and what is not: the
 * session goes, the conversation stays (it can be continued again from
 * "+ session"). A busy session is stopped mid-turn, so that is said too.
 */
@Composable
private fun DeleteSessionDialog(host: String, address: String, s: AgentSession,
                                onDismiss: () -> Unit, onDeleted: () -> Unit) {
    var busy by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    val scope = rememberCoroutineScope()
    androidx.compose.material3.AlertDialog(
        onDismissRequest = { if (!busy) onDismiss() },
        containerColor = Palette.Panel,
        title = { Text("Delete session \"${s.name}\" on $host?", style = MaterialTheme.typography.titleMedium, color = Palette.Bone) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(deleteSessionText(s.state), style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
                if (error.isNotEmpty()) Text(error, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
            }
        },
        confirmButton = {
            Text("DELETE", style = MaterialTheme.typography.labelSmall,
                color = if (busy) Palette.Ash else Palette.Rust,
                modifier = Modifier.clickable(enabled = !busy) {
                    busy = true
                    scope.launch {
                        withContext(Dispatchers.IO) { runCatching { AgentClient(address).remove(s.name) } }
                            .onSuccess { onDeleted() }
                            .onFailure { error = it.message ?: "could not delete it"; busy = false }
                    }
                }.padding(12.dp))
        },
        dismissButton = {
            Text("CANCEL", style = MaterialTheme.typography.labelSmall, color = Palette.Bone,
                modifier = Modifier.clickable(enabled = !busy) { onDismiss() }.padding(12.dp))
        },
    )
}

/** What deleting a session in this state does, in words. */
fun deleteSessionText(state: String): String = buildString {
    append("This stops the session and removes it from the list. ")
    append("The Claude Code conversation itself is kept on that machine, and can be continued again from \"+ session\".")
    when (state) {
        "working" -> append("\n\nIt is working right now: that turn will be cut off.")
        "waiting" -> append("\n\nIt is waiting for an answer to a permission prompt, which will be dropped.")
    }
}
