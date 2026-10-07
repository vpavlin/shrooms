package xyz.vpavlin.shrooms

import android.content.Context
import kotlinx.coroutines.flow.MutableStateFlow

/**
 * Replies not yet seen on this device, per session: the session's turns — one
 * per reply of the model, counted by the agent — less the turns there were
 * when it was last on screen here. Kept on the phone; each device reads for
 * itself.
 */
object Unread {
    /** Bumped on every change, for the list to redraw. */
    val changed = MutableStateFlow(0L)

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("unread", Context.MODE_PRIVATE)
    private fun key(host: String, session: String) = "$host/$session"

    /**
     * How many of [turns] are unread when [read] were seen. A session never
     * seen here starts read — its whole past is not news — and so does one
     * whose count went down (deleted and made again).
     */
    fun count(read: Long?, turns: Long): Int = when {
        turns < 0 || read == null || turns < read -> 0
        else -> (turns - read).coerceAtMost(Int.MAX_VALUE.toLong()).toInt()
    }

    /** Unread replies of a session in the list; remembers one seen for the first time. */
    fun of(ctx: Context, host: String, s: AgentSession): Int {
        if (s.turns < 0) return 0
        val p = prefs(ctx)
        val k = key(host, s.name)
        val read = if (p.contains(k)) p.getLong(k, 0) else null
        if (read == null || s.turns < read) p.edit().putLong(k, s.turns).apply()
        return count(read, s.turns)
    }

    /** The session has been seen with [turns] ended. */
    fun seen(ctx: Context, host: String, session: String, turns: Long) {
        if (turns < 0) return
        val p = prefs(ctx)
        val k = key(host, session)
        if (p.contains(k) && p.getLong(k, -1) == turns) return
        p.edit().putLong(k, turns).apply()
        changed.value = changed.value + 1
    }

    /** What was read follows a session that was renamed. */
    fun rename(ctx: Context, host: String, from: String, to: String) {
        val p = prefs(ctx)
        val read = p.all[key(host, from)] as? Long ?: return
        p.edit().remove(key(host, from)).putLong(key(host, to), read).apply()
    }

    fun forget(ctx: Context, host: String, session: String) {
        prefs(ctx).edit().remove(key(host, session)).apply()
    }
}
