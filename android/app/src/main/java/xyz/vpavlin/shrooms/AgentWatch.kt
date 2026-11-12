package xyz.vpavlin.shrooms

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * Tells you when an agent needs you or has answered, with the app closed
 * (docs/agents.md, stage 3).
 *
 * Polls each known agent's session list rather than holding a stream per
 * session: one small request per machine every [EVERY_MS], which is cheap on a
 * phone and survives the network changing underneath it. Which machines are
 * known is remembered by the Agents screen ([AgentHosts]).
 */
object AgentWatch {
    const val EVERY_MS = 15_000L
    /** With the screen off: a notification a minute late costs less than the radio woken four times a minute. */
    const val EVERY_MS_SCREEN_OFF = 60_000L

    /**
     * The machines to ask in one round: one address per machine, the one that
     * answered last first, and a machine that missed rounds skipped in all but
     * every 2^(misses-1)-th, up to 16. A machine on two meshes was asked
     * twice and notified about twice; one that went away was asked every round
     * for the week it is remembered. Pure, for the tests.
     */
    fun roundTargets(hosts: List<AgentHosts.Host>, lastGood: Map<String, String>, misses: Map<String, Int>, round: Long): List<List<AgentHosts.Host>> =
        hosts.groupBy { it.name }.map { (name, hs) ->
            hs.sortedBy { if (it.address == lastGood[name]) 0 else 1 }
        }.filter { hs ->
            val m = misses[hs.first().name] ?: 0
            m < 2 || round % (1L shl (m - 1).coerceAtMost(4)) == 0L
        }
    const val CHANNEL = "agents"
    const val EXTRA_ADDRESS = "agent_address"
    const val EXTRA_HOST = "agent_host"
    const val EXTRA_MESH = "agent_mesh"
    const val EXTRA_SESSION = "agent_session"

    /** The session on screen right now, as "address/session"; it is not notified about. */
    @Volatile var visible: String? = null

    /** What a session looked like at the last poll. */
    data class Seen(val state: String, val lastSeq: Long, val turns: Long = -1, val stalled: Int = 0, val held: Int = 0)

    /**
     * What to say about a change, if anything. Pure, so the rules are tested
     * off-device (AgentWatchTest).
     *
     * - started waiting for an answer → it needs you;
     * - a turn ended → it replied, with what it said. Once, however many
     *   ended between two polls.
     *
     * Turns as the agent counts them, not events: a session waiting on
     * background work sends heartbeats, thinking and progress while it sits
     * between turns, and every one of them read as a new reply — a
     * notification every fifteen seconds, with the same text (2026-10-04).
     * An agent too old to count turns gets the old rule without that part:
     * working, then idle with something new.
     *
     * A session seen for the first time is only remembered: on start, every
     * existing session is "new", and a burst of stale notifications is noise.
     */
    fun change(before: Seen?, now: AgentSession): String? = when {
        before == null -> null
        now.state == "waiting" && before.state != "waiting" -> "needs you — something is waiting for approval"
        // A caged agent gave it a task the owner has not allowed yet (ADR-070).
        now.taskRequests.size > before.held ->
            "${now.taskRequests.first().from} wants to give it a task — allow or decline it"
        // A task another agent gave it made no progress after its reminders.
        now.tasksStalled > before.stalled -> "a task stalled — no progress after the reminders"
        now.turns >= 0 && before.turns >= 0 ->
            if (now.turns > before.turns) now.preview.ifEmpty { "finished" } else null
        now.state == "idle" && now.lastSeq > before.lastSeq && before.state != "idle" ->
            now.preview.ifEmpty { "finished" }
        else -> null
    }
}

/** "name|mesh|address;..." as hosts, mesh addresses only. Pure, for the tests. */
fun parsePeers(s: String): List<AgentHosts.Host> =
    s.split(';').mapNotNull {
        val p = it.split('|')
        if (p.size == 3 && AgentClient.isMeshAddress(p[2])) AgentHosts.Host(p[0], p[1], p[2]) else null
    }

/** The other way: what the shrooms app hands Shrooms Agents. Online peers only. */
fun peersForAgents(peers: List<Peer>): String =
    peers.filter { it.online && it.overlay.isNotEmpty() }.joinToString(";") { "${it.name}|${it.mesh}|${it.overlay}" }

/** The machines the Agents screen has found, so the watcher can poll them. */
object AgentHosts {
    data class Host(val name: String, val mesh: String, val address: String)

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("agents", Context.MODE_PRIVATE)

    fun load(ctx: Context): List<Host> =
        prefs(ctx).getStringSet("hosts", emptySet())!!.mapNotNull {
            val p = it.split('|')
            if (p.size == 3 && AgentClient.isMeshAddress(p[2])) Host(p[0], p[1], p[2]) else null
        }

    /**
     * The peers the shrooms app could reach when it last opened this one, as
     * "name|mesh|address;..." — where Shrooms Agents looks for agents, since
     * it is no mesh client itself.
     */
    fun savePeers(ctx: Context, peers: String) {
        prefs(ctx).edit().putString("peers", peers).apply()
    }

    fun peers(ctx: Context): List<Host> = parsePeers(prefs(ctx).getString("peers", "") ?: "")

    fun save(ctx: Context, hosts: List<Host>) {
        prefs(ctx).edit().putStringSet("hosts", hosts.map { "${it.name}|${it.mesh}|${it.address}" }.toSet()).apply()
    }
}

/** One watcher loop, hosted by whichever service is running (see [start]). */
class AgentWatcher(private val ctx: Context) {
    private val seen = mutableMapOf<String, AgentWatch.Seen>()
    private val lastGood = mutableMapOf<String, String>() // machine -> address that answered
    private val misses = mutableMapOf<String, Int>()
    private var round = 0L

    suspend fun run() {
        ensureChannel(ctx)
        while (kotlinx.coroutines.currentCoroutineContext().isActive) {
            // What was written while a machine was unreachable goes as soon
            // as it is back, app open or not.
            runCatching { Outbox.flush(ctx) }
            round++
            for (addrs in AgentWatch.roundTargets(AgentHosts.load(ctx), lastGood, misses, round)) {
                var h = addrs.first()
                var sessions: List<AgentSession>? = null
                for (a in addrs) {
                    sessions = runCatching { AgentClient(a.address).sessions(5000) }.getOrNull()
                    if (sessions != null) { h = a; break }
                }
                if (sessions == null) { misses[h.name] = (misses[h.name] ?: 0) + 1; continue }
                misses.remove(h.name)
                lastGood[h.name] = h.address
                for (s in sessions) {
                    val key = "${h.address}/${s.name}"
                    val before = seen[key]
                    val said = AgentWatch.change(before, s)
                    seen[key] = AgentWatch.Seen(s.state, s.lastSeq, s.turns, s.tasksStalled, s.taskRequests.size)
                    if (said != null && AgentWatch.visible != key) notify(h, s, said)
                    if (AgentWatch.visible == key) Unread.seen(ctx, h.name, s.name, s.turns)
                    // Kept up to date for offline; the one on screen keeps itself.
                    // Only when something happened since the last round:
                    // refresh loads and parses the whole saved history (up to
                    // 1 MB) before it can tell that nothing is new, and did so
                    // for every session every 15 s with the screen off.
                    if (AgentWatch.visible != key && (before == null || before.lastSeq != s.lastSeq)) {
                        runCatching { History.refresh(ctx, AgentClient(h.address), h.name, s.name, s.lastSeq) }
                            .onSuccess { Speech.heard(ctx, h.name, s.name, it) }
                    }
                }
            }
            val screenOn = (ctx.getSystemService(Context.POWER_SERVICE) as android.os.PowerManager).isInteractive
            delay(if (screenOn) AgentWatch.EVERY_MS else AgentWatch.EVERY_MS_SCREEN_OFF)
        }
    }

    private fun notify(h: AgentHosts.Host, s: AgentSession, text: String) {
        val open = Intent(ctx, MainActivity::class.java)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_CLEAR_TOP)
            .putExtra(AgentWatch.EXTRA_ADDRESS, h.address)
            .putExtra(AgentWatch.EXTRA_HOST, h.name)
            .putExtra(AgentWatch.EXTRA_MESH, h.mesh)
            .putExtra(AgentWatch.EXTRA_SESSION, s.name)
        val id = "${h.address}/${s.name}".hashCode()
        val pi = PendingIntent.getActivity(ctx, id, open,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(ctx, AgentWatch.CHANNEL)
            .setSmallIcon(android.R.drawable.stat_notify_chat)
            .setContentTitle("${s.name} · ${h.name}")
            .setContentText(text)
            .setStyle(Notification.BigTextStyle().bigText(text))
            .setContentIntent(pi)
            .setAutoCancel(true)
            .build()
        ctx.getSystemService(NotificationManager::class.java).notify(id, n)
    }

    companion object {
        fun ensureChannel(ctx: Context) {
            val mgr = ctx.getSystemService(NotificationManager::class.java)
            if (mgr.getNotificationChannel(AgentWatch.CHANNEL) == null) {
                mgr.createNotificationChannel(NotificationChannel(
                    AgentWatch.CHANNEL, "Agents", NotificationManager.IMPORTANCE_HIGH,
                ).apply { description = "An agent needs you, or has replied" })
            }
        }
    }
}

/**
 * Hosts the watcher in Shrooms Agents. Foreground, because Android stops
 * anything else in the background within minutes; a quiet notification is the
 * price of hearing that an agent needs you.
 */
class AgentWatchService : Service() {
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val mgr = getSystemService(NotificationManager::class.java)
        if (mgr.getNotificationChannel(QUIET) == null) {
            mgr.createNotificationChannel(NotificationChannel(QUIET, "Watching agents", NotificationManager.IMPORTANCE_MIN))
        }
        val n = Notification.Builder(this, QUIET)
            .setSmallIcon(android.R.drawable.stat_notify_sync_noanim)
            .setContentTitle("Watching your agents")
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(1, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(1, n)
        }
        if (!started) {
            started = true
            scope.launch { AgentWatcher(this@AgentWatchService).run() }
        }
        return START_STICKY
    }

    private var started = false

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }

    companion object {
        private const val QUIET = "agents-watch"
    }
}
