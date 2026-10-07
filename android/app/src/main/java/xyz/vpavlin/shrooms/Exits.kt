package xyz.vpavlin.shrooms

import android.app.ActivityManager
import android.app.Application
import android.app.ApplicationExitInfo
import android.content.Context
import android.os.Build
import java.io.File
import mobile.Mobile
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone

/**
 * How the app's process ended, for "share diagnostics".
 *
 * The core's own record says "KILLED" for every end it did not see coming —
 * a crash in Kotlin, a crash in the delivery library, Android reclaiming
 * memory, and the watchdog ending the process on purpose all look the same.
 * Android keeps the real reason (Android 11+, ApplicationExitInfo), and a
 * Kotlin crash leaves its stack trace here; the report shows both.
 */
object Exits {
    private fun crashFile(ctx: Context) = File(ctx.filesDir, "last-crash.txt")

    /** Writes an uncaught Kotlin exception's stack trace before the process dies. */
    fun install(ctx: Context) {
        val app = ctx.applicationContext
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, e ->
            runCatching {
                crashFile(app).writeText("${stamp(System.currentTimeMillis())} in thread ${thread.name}\n" +
                    e.stackTraceToString().take(8000))
            }
            previous?.uncaughtException(thread, e)
        }
    }

    private fun stamp(ms: Long) = SimpleDateFormat("yyyy-MM-dd HH:mm:ss", Locale.ROOT)
        .apply { timeZone = TimeZone.getTimeZone("UTC") }.format(Date(ms)) + "Z"

    fun reasonName(r: Int): String = when (r) {
        ApplicationExitInfo.REASON_ANR -> "not responding (ANR)"
        ApplicationExitInfo.REASON_CRASH -> "crash (Kotlin/Java)"
        ApplicationExitInfo.REASON_CRASH_NATIVE -> "crash (native: Go or the delivery library)"
        ApplicationExitInfo.REASON_DEPENDENCY_DIED -> "a process it depends on died"
        ApplicationExitInfo.REASON_EXCESSIVE_RESOURCE_USAGE -> "too much CPU or memory"
        ApplicationExitInfo.REASON_EXIT_SELF -> "ended itself (the watchdog's restart, or exit)"
        ApplicationExitInfo.REASON_INITIALIZATION_FAILURE -> "failed to start"
        ApplicationExitInfo.REASON_LOW_MEMORY -> "Android reclaimed memory"
        ApplicationExitInfo.REASON_OTHER -> "other (Android's own reasons)"
        ApplicationExitInfo.REASON_PERMISSION_CHANGE -> "a permission changed"
        ApplicationExitInfo.REASON_SIGNALED -> "killed by a signal"
        ApplicationExitInfo.REASON_USER_REQUESTED -> "stopped by you (force stop, or an update)"
        ApplicationExitInfo.REASON_USER_STOPPED -> "stopped by you"
        else -> "unknown ($r)"
    }

    /**
     * The whole report, with Android's own account of how the app ended
     * placed after the core's "how it stopped" and panic — not at the end:
     * the report is long, a share through a chat cuts it, and three reports
     * in a row arrived without this section (2026-10-07).
     */
    fun report(dir: String, ctx: Context): String = insert(Mobile.diagnostics(dir), describe(ctx))

    /** [exits] put before the core report's memory section, or at the end if it has none. */
    fun insert(core: String, exits: String): String {
        val at = core.indexOf("\n== memory ==")
        return if (at < 0) core + exits else core.substring(0, at) + "\n" + exits.trimEnd('\n') + core.substring(at)
    }

    /** The report's section: Android's last exits of this app, and the last Kotlin crash. */
    fun describe(ctx: Context): String {
        val b = StringBuilder("\n== how Android says it ended, most recent first ==\n")
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            val am = ctx.getSystemService(ActivityManager::class.java)
            val exits = runCatching { am.getHistoricalProcessExitReasons(ctx.packageName, 0, 12) }.getOrDefault(emptyList())
            if (exits.isEmpty()) b.append("(none recorded)\n")
            for (e in exits) {
                b.append(stamp(e.timestamp)).append("  ").append(reasonName(e.reason))
                if (e.reason == ApplicationExitInfo.REASON_SIGNALED || e.reason == ApplicationExitInfo.REASON_CRASH_NATIVE)
                    b.append(", signal ").append(e.status)
                else if (e.reason == ApplicationExitInfo.REASON_EXIT_SELF) b.append(", status ").append(e.status)
                b.append(", ").append(e.processName.substringAfter(':', "main"))
                b.append(", memory ").append(e.pss / 1024).append(" MiB")
                e.description?.takeIf { it.isNotBlank() }?.let { b.append("\n    ").append(it.take(300)) }
                b.append('\n')
            }
        } else b.append("(Android ${Build.VERSION.RELEASE} does not keep them; 11 and later do)\n")
        b.append("\n== last Kotlin crash ==\n")
        b.append(runCatching { crashFile(ctx).readText() }.getOrNull()?.ifBlank { null } ?: "(none)\n")
        return b.toString()
    }
}

/** Installs the crash record first thing in every process of the app. */
class ShroomsApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Exits.install(this)
    }
}
