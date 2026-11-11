package xyz.vpavlin.shrooms

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PersistableBundle
import android.widget.Toast
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONArray
import org.json.JSONObject

// A clipboard between this person's devices, on the mesh (ADR-083). The core
// keeps the clips and moves them; this is the screen, the notification with
// Copy, and the share target. Shrooms only: Shrooms Agents never opens it and
// has no mesh session to carry it.

data class ClipTarget(val name: String, val sentMs: Long, val error: String)

data class Clip(
    val id: String,
    val text: String,
    val secret: Boolean,
    val mesh: String,
    val from: String,
    val to: List<ClipTarget>,
    val createdMs: Long,
    val expiresMs: Long,
    val takenMs: Long,
)

data class ClipDevice(val mesh: String, val name: String, val online: Boolean)

data class ClipsView(
    val received: List<Clip> = emptyList(),
    val sent: List<Clip> = emptyList(),
    val devices: List<ClipDevice> = emptyList(),
    val meshes: List<String> = emptyList(),
)

object Clips {
    /** Bumped when a clip arrives, so an open screen reads at once rather than on its next round. */
    val changed = MutableStateFlow(0L)

    const val EXTRA_OPEN = "open_clips"
    const val ACTION_COPY = "xyz.vpavlin.shrooms.CLIP_COPY"
    const val EXTRA_ID = "clip_id"
    const val EXTRA_NOTIFICATION = "clip_notification"
    private const val CHANNEL = "clips"

    /** How many recent picks to remember; more than one phone's worth of devices. */
    private const val RECENT = 12

    private fun ms(t: String): Long =
        if (t.isEmpty()) 0L
        else runCatching { java.time.OffsetDateTime.parse(t).toInstant().toEpochMilli() }.getOrDefault(0L)

    fun clip(o: JSONObject): Clip {
        val to = o.optJSONArray("to") ?: JSONArray()
        return Clip(
            id = o.optString("id"),
            text = o.optString("text"),
            secret = o.optBoolean("secret"),
            mesh = o.optString("mesh"),
            // A name only announced, not signed by its admin, says so
            // wherever it is shown: another member could be using it.
            from = o.optString("from") + if (o.optBoolean("unsigned")) " (unsigned)" else "",
            to = (0 until to.length()).map { i ->
                val t = to.getJSONObject(i)
                ClipTarget(t.optString("name"), ms(t.optString("sent")), t.optString("error"))
            },
            createdMs = ms(o.optString("created")),
            expiresMs = ms(o.optString("expires")),
            takenMs = ms(o.optString("taken")),
        )
    }

    fun parse(json: String): ClipsView = runCatching {
        val o = JSONObject(json)
        fun clips(key: String): List<Clip> {
            val a = o.optJSONArray(key) ?: return emptyList()
            return (0 until a.length()).map { clip(a.getJSONObject(it)) }.sortedByDescending { it.createdMs }
        }
        val d = o.optJSONArray("devices") ?: JSONArray()
        val m = o.optJSONArray("meshes") ?: JSONArray()
        ClipsView(
            received = clips("received"),
            sent = clips("sent"),
            devices = (0 until d.length()).map { i ->
                val x = d.getJSONObject(i)
                ClipDevice(x.optString("mesh"), x.optString("name"), x.optBoolean("online"))
            },
            meshes = (0 until m.length()).map { m.getString(it) },
        )
    }.getOrDefault(ClipsView())

    // Recent recipients, newest first, as "mesh/name". On this phone only: it
    // is a property of how this hand reaches for devices, not of the mesh.
    private fun prefs(ctx: Context) = ctx.getSharedPreferences("clips", Context.MODE_PRIVATE)

    fun recent(ctx: Context): List<String> =
        prefs(ctx).getString("recent", "").orEmpty().split('\n').filter { it.isNotEmpty() }

    fun picked(ctx: Context, mesh: String, names: List<String>) {
        val now = names.map { "$mesh/$it" }
        val kept = (now + recent(ctx).filterNot { it in now }).take(RECENT)
        prefs(ctx).edit().putString("recent", kept.joinToString("\n")).apply()
    }

    /**
     * The devices of one mesh in the order the chips show them: the ones sent
     * to lately first, so the usual target is where the thumb already is, then
     * the online ones, then by name.
     */
    fun order(devices: List<ClipDevice>, mesh: String, recent: List<String>): List<ClipDevice> =
        devices.filter { it.mesh == mesh }.sortedWith(
            compareBy<ClipDevice> { d -> recent.indexOf("${d.mesh}/${d.name}").let { if (it < 0) Int.MAX_VALUE else it } }
                .thenBy { !it.online }
                .thenBy { it.name },
        )

    /**
     * Puts [text] on this phone's clipboard. A secret is marked sensitive, so
     * Android 13+ keeps it out of its copy preview and keyboards' clipboard
     * suggestions; the extra is read under its literal name before 33 by the
     * keyboards that honour it at all.
     */
    fun copy(ctx: Context, text: String, secret: Boolean) {
        val data = ClipData.newPlainText(if (secret) "secret" else "clip", text)
        if (secret) {
            val key = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) ClipDescription.EXTRA_IS_SENSITIVE
            else "android.content.extra.IS_SENSITIVE"
            data.description.extras = PersistableBundle().apply { putBoolean(key, true) }
        }
        ctx.getSystemService(ClipboardManager::class.java)?.setPrimaryClip(data)
    }

    fun firstLine(text: String): String = text.lineSequence().firstOrNull { it.isNotBlank() }?.trim().orEmpty()

    /** One notification per clip, so two clips are two Copy buttons. Clear of the service's own ids (1-3). */
    private fun notificationId(id: String) = 1000 + (id.hashCode() and 0xffff)

    fun openIntent(ctx: Context): Intent = Intent(ctx, MainActivity::class.java)
        .putExtra(EXTRA_OPEN, true)
        // Into the running screen when there is one (onNewIntent), not a
        // second copy of the app on top of it.
        .addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP or Intent.FLAG_ACTIVITY_CLEAR_TOP)

    /**
     * Says a clip has arrived, with Copy. No Android app may read the
     * clipboard from the background, but any may write it, so Copy works with
     * the screen locked away; taking a clip is this, sending is the share
     * sheet (ADR-083).
     *
     * A secret's text never goes into the notification, nor into the Copy
     * intent: the receiver asks the core for it, so it is not left in the
     * shade's history or in a PendingIntent the system keeps.
     */
    fun notifyArrived(ctx: Context, json: String) {
        changed.update { it + 1 }
        val c = runCatching { clip(JSONObject(json)) }.getOrNull() ?: return
        if (c.from.isEmpty()) return
        val mgr = ctx.getSystemService(NotificationManager::class.java) ?: return
        if (mgr.getNotificationChannel(CHANNEL) == null) {
            mgr.createNotificationChannel(
                NotificationChannel(CHANNEL, "Clips", NotificationManager.IMPORTANCE_DEFAULT).apply {
                    description = "A clip sent from another of your devices"
                },
            )
        }
        val nid = notificationId(c.id)
        val open = PendingIntent.getActivity(ctx, nid, openIntent(ctx),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val copy = PendingIntent.getBroadcast(ctx, nid,
            Intent(ctx, ClipCopyReceiver::class.java).setAction(ACTION_COPY)
                .putExtra(EXTRA_ID, c.id).putExtra(EXTRA_NOTIFICATION, nid),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        val title = "Clip from ${c.from}"
        val body = if (c.secret) "a secret" else firstLine(c.text)
        // What the lock screen shows: who, never what.
        val public = Notification.Builder(ctx, CHANNEL)
            .setContentTitle(title)
            .setSmallIcon(R.drawable.ic_mesh)
            .build()
        val b = Notification.Builder(ctx, CHANNEL)
            .setContentTitle(title)
            .setContentText(body)
            .setSmallIcon(R.drawable.ic_mesh)
            .setContentIntent(open)
            .setAutoCancel(true)
            .addAction(Notification.Action.Builder(null, "Copy", copy).build())
        if (c.secret) b.setVisibility(Notification.VISIBILITY_SECRET)
        else b.setVisibility(Notification.VISIBILITY_PRIVATE).setPublicVersion(public)
        runCatching { mgr.notify(nid, b.build()) }
    }

    fun cancel(ctx: Context, nid: Int) {
        runCatching { ctx.getSystemService(NotificationManager::class.java)?.cancel(nid) }
    }

    /** The open clip's notification goes once it has been copied here, however that was done. */
    fun cancelFor(ctx: Context, id: String) = cancel(ctx, notificationId(id))
}

/**
 * Copy, from a clip's notification: takes the clip from the core (which starts
 * a secret's five minutes), puts it on the clipboard, and clears the
 * notification. Not exported; only the notification's own PendingIntent
 * reaches it.
 */
class ClipCopyReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        if (intent.action != Clips.ACTION_COPY) return
        val id = intent.getStringExtra(Clips.EXTRA_ID) ?: return
        val nid = intent.getIntExtra(Clips.EXTRA_NOTIFICATION, 0)
        val app = ctx.applicationContext
        val pending = goAsync()
        // The core reads and writes its store under a lock; off the main thread.
        Thread {
            val taken = runCatching { Clips.clip(JSONObject(Mobile.clipTake(app.filesDir.absolutePath, id))) }
            Handler(Looper.getMainLooper()).post {
                taken.onSuccess {
                    Clips.copy(app, it.text, it.secret)
                    Clips.cancel(app, nid)
                    Clips.changed.update { n -> n + 1 }
                }.onFailure {
                    // A secret is kept in memory only, so after the app was
                    // restarted it is simply gone; say so rather than copy nothing.
                    Toast.makeText(app, "Could not copy: ${it.message ?: "the clip is gone"}", Toast.LENGTH_LONG).show()
                }
                pending.finish()
            }
        }.start()
    }
}

// --- the screen -------------------------------------------------------------

/**
 * Clips: send one to a device or a mesh, and take the ones that came.
 *
 * [draft] is text handed in by the share sheet, put into the box once and
 * then forgotten via [onDraftUsed].
 */
@Composable
fun ClipsScreen(dir: String, draft: String?, onDraftUsed: () -> Unit, onClose: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var view by remember { mutableStateOf(ClipsView()) }
    var text by rememberSaveable { mutableStateOf("") }
    var secret by rememberSaveable { mutableStateOf(false) }
    var mesh by rememberSaveable { mutableStateOf("") }
    var all by rememberSaveable { mutableStateOf(false) }
    var picked by remember { mutableStateOf(setOf<String>()) }
    var recent by remember { mutableStateOf(Clips.recent(ctx)) }
    var sending by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf("") }
    var reload by remember { mutableIntStateOf(0) }
    val me = remember { runCatching { Mobile.deviceName(dir) }.getOrDefault("") }

    LaunchedEffect(draft) {
        if (draft != null) { text = draft; onDraftUsed() }
    }

    suspend fun load() {
        view = withContext(Dispatchers.IO) { Clips.parse(Mobile.clipsJSON(dir)) }
    }

    // Every three seconds while on screen and never otherwise: a recipient
    // coming online is what turns "waiting" into a tick, and nothing else
    // would say so. An arrival does not wait for the round (Clips.changed).
    val visible = screenVisible()
    val arrived by Clips.changed.collectAsStateWithLifecycle()
    LaunchedEffect(visible, arrived, reload) {
        while (visible) {
            runCatching { load() }
            delay(3_000)
        }
    }

    // The mesh the chips are for. The first one with another device on it until
    // one is picked; a mesh that has gone falls back the same way.
    val shownMesh = mesh.takeIf { it in view.meshes }
        ?: view.meshes.firstOrNull { m -> view.devices.any { it.mesh == m } }
        ?: view.meshes.firstOrNull().orEmpty()
    val devices = Clips.order(view.devices, shownMesh, recent)
    val to = picked.filter { n -> devices.any { it.name == n } }
    val canSend = text.isNotEmpty() && !sending && shownMesh.isNotEmpty() && (all || to.isNotEmpty())

    fun send() {
        sending = true
        error = ""
        val req = JSONObject()
            .put("text", text)
            .put("mesh", shownMesh)
            .put("secret", secret)
        if (all) req.put("all", true) else req.put("to", JSONArray(to))
        scope.launch {
            val r = withContext(Dispatchers.IO) { runCatching { Mobile.clipSend(dir, req.toString()) } }
            sending = false
            r.onSuccess {
                if (!all) { Clips.picked(ctx, shownMesh, to); recent = Clips.recent(ctx) }
                text = ""
                secret = false
                reload++
            }.onFailure { error = it.message ?: "could not send" }
        }
    }

    LazyColumn(Modifier.fillMaxSize(), contentPadding = PaddingValues(horizontal = 24.dp, vertical = 32.dp)) {
        item {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("Clips", style = MaterialTheme.typography.headlineSmall, color = Palette.Bone,
                    modifier = Modifier.weight(1f))
                Link("close", Palette.Ash) { onClose() }
            }
            Spacer(Modifier.height(20.dp))
            KeyField(text) { text = it }
            Row(verticalAlignment = Alignment.CenterVertically) {
                // Reading the clipboard is allowed only to the app in front,
                // which is this screen; so paste is a tap here, never automatic.
                Link("paste") {
                    val cm = ctx.getSystemService(ClipboardManager::class.java)
                    val got = cm?.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(ctx)?.toString()
                    if (!got.isNullOrEmpty()) text = got
                }
                if (text.isNotEmpty()) Link("clear", Palette.Ash) { text = "" }
                Spacer(Modifier.weight(1f))
                Text("secret", style = MaterialTheme.typography.bodySmall,
                    color = if (secret) Palette.Amber else Palette.Ash)
                Spacer(Modifier.width(8.dp))
                Switch(
                    checked = secret, onCheckedChange = { secret = it },
                    colors = SwitchDefaults.colors(
                        checkedThumbColor = Palette.Void, checkedTrackColor = Palette.Amber,
                        uncheckedThumbColor = Palette.Ash, uncheckedTrackColor = Palette.Panel,
                        uncheckedBorderColor = Palette.Line,
                    ),
                )
            }
            if (secret) {
                Text("Kept in memory only, for an hour; gone five minutes after it is first copied.",
                    style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
            }

            Spacer(Modifier.height(16.dp))
            Label("TO")
            Spacer(Modifier.height(8.dp))
            // "All" never spans meshes, and the office mesh has other people's
            // machines on it (ADR-083), so with several meshes the chips are
            // one mesh's at a time and the mesh is picked first.
            if (view.meshes.size > 1) {
                @OptIn(ExperimentalLayoutApi::class)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp),
                    modifier = Modifier.padding(bottom = 10.dp)) {
                    view.meshes.forEach { m ->
                        Chip(m, on = m == shownMesh, tint = Palette.Sky) {
                            if (m != shownMesh) { mesh = m; all = false; picked = emptySet() }
                        }
                    }
                }
            }
            if (view.meshes.isEmpty()) {
                Text("The mesh is not running, so there is nobody to send to. Clips already here are below.",
                    style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
            } else {
                @OptIn(ExperimentalLayoutApi::class)
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    devices.forEach { d ->
                        Chip(d.name, on = !all && d.name in picked, enabled = !all,
                            dot = if (d.online) Palette.Phosphor else Palette.Ash) {
                            picked = if (d.name in picked) picked - d.name else picked + d.name
                        }
                    }
                    Chip("all of $shownMesh", on = all, tint = Palette.Bone) { all = !all }
                }
            }

            if (error.isNotEmpty()) {
                Spacer(Modifier.height(10.dp))
                Text(error, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
            }
            Spacer(Modifier.height(14.dp))
            Action(if (sending) "SENDING…" else if (secret) "SEND SECRET" else "SEND", enabled = canSend) { send() }
        }

        item {
            Spacer(Modifier.height(28.dp))
            Label("RECEIVED")
            Spacer(Modifier.height(8.dp))
            if (view.received.isEmpty()) {
                Text("Nothing yet. From a computer: shrooms copy --to ${me.ifEmpty { "this-phone" }}",
                    style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
            }
        }
        items(view.received, key = { "r" + it.id }) { c ->
            ReceivedRow(c,
                onCopy = {
                    Clips.copy(ctx, c.text, c.secret)
                    Clips.cancelFor(ctx, c.id)
                    scope.launch {
                        withContext(Dispatchers.IO) { runCatching { Mobile.clipTake(dir, c.id) } }
                        reload++
                    }
                },
                onDelete = {
                    scope.launch {
                        withContext(Dispatchers.IO) { runCatching { Mobile.clipDelete(dir, c.id) } }
                        Clips.cancelFor(ctx, c.id)
                        reload++
                    }
                })
        }

        item {
            Spacer(Modifier.height(28.dp))
            Label("SENT")
            Spacer(Modifier.height(8.dp))
            if (view.sent.isEmpty()) {
                Text("Nothing sent from here yet. Share text to Shrooms from any app to send it.",
                    style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
            }
        }
        items(view.sent, key = { "s" + it.id }) { c ->
            SentRow(c, onDelete = {
                scope.launch {
                    withContext(Dispatchers.IO) { runCatching { Mobile.clipDelete(dir, c.id) } }
                    reload++
                }
            })
        }
    }
}

@Composable
private fun Chip(text: String, on: Boolean, enabled: Boolean = true, tint: Color = Palette.Phosphor,
                 dot: Color? = null, onClick: () -> Unit) {
    val colour = if (enabled) tint else Palette.Line
    Row(
        verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.background(if (on) colour.copy(alpha = 0.18f) else Color.Transparent, RoundedCornerShape(10.dp))
            .border(1.dp, if (on) colour else Palette.Line, RoundedCornerShape(10.dp))
            .clickable(enabled = enabled) { onClick() }
            .padding(horizontal = 10.dp, vertical = 8.dp),
    ) {
        if (dot != null) { Dot(if (enabled) dot else Palette.Line, size = 6); Spacer(Modifier.width(6.dp)) }
        Text(text, style = MaterialTheme.typography.labelSmall, color = if (on) colour else if (enabled) Palette.Bone else Palette.Ash)
    }
}

private fun ago(ms: Long): String =
    if (ms <= 0) "" else shortDuration(((System.currentTimeMillis() - ms) / 1000).coerceAtLeast(0)) + " ago"

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun ReceivedRow(c: Clip, onCopy: () -> Unit, onDelete: () -> Unit) {
    // A secret shows as dots until it is tapped, which is also what copies
    // it: somebody looking over a shoulder at the list learns nothing.
    var revealed by remember(c.id) { mutableStateOf(false) }
    var copied by remember { mutableStateOf(false) }
    LaunchedEffect(copied) { if (copied) { delay(1200); copied = false } }
    Column(
        Modifier.fillMaxWidth().padding(vertical = 4.dp)
            .background(Palette.Panel, RoundedCornerShape(10.dp))
            .combinedClickable(onClick = { onCopy(); revealed = true; copied = true }, onLongClick = onDelete)
            .padding(start = 14.dp, top = 10.dp, bottom = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(
                if (copied) "copied" else "${c.from} · ${c.mesh} · ${ago(c.createdMs)}" + if (c.takenMs > 0) " · taken" else "",
                style = MaterialTheme.typography.bodySmall,
                color = if (copied) Palette.Phosphor else Palette.Ash,
                modifier = Modifier.weight(1f),
            )
            if (c.secret) Text("SECRET", style = MaterialTheme.typography.labelSmall, color = Palette.Amber)
            Link("×", Palette.Ash) { onDelete() }
        }
        Text(
            if (c.secret && !revealed) "••••" else c.text,
            style = MaterialTheme.typography.bodyMedium, color = Palette.Bone,
            maxLines = 4, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(end = 14.dp),
        )
    }
}

@OptIn(ExperimentalFoundationApi::class)
@Composable
private fun SentRow(c: Clip, onDelete: () -> Unit) {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 4.dp)
            .background(Palette.Panel, RoundedCornerShape(10.dp))
            .combinedClickable(onClick = {}, onLongClick = onDelete)
            .padding(start = 14.dp, top = 10.dp, bottom = 12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("${c.mesh} · ${ago(c.createdMs)}", style = MaterialTheme.typography.bodySmall,
                color = Palette.Ash, modifier = Modifier.weight(1f))
            if (c.secret) Text("SECRET", style = MaterialTheme.typography.labelSmall, color = Palette.Amber)
            Link("×", Palette.Ash) { onDelete() }
        }
        // A secret's text is dropped here once it is delivered; the sender
        // has no need of it and should not keep it.
        Text(
            if (c.secret) "(secret)" else c.text,
            style = MaterialTheme.typography.bodyMedium, color = if (c.secret) Palette.Ash else Palette.Bone,
            maxLines = 3, overflow = TextOverflow.Ellipsis,
            modifier = Modifier.padding(end = 14.dp),
        )
        Spacer(Modifier.height(6.dp))
        c.to.forEach { t ->
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(end = 14.dp)) {
                Text(if (t.sentMs > 0) "✓" else "…", style = MaterialTheme.typography.bodySmall,
                    color = if (t.sentMs > 0) Palette.Phosphor else Palette.Amber, modifier = Modifier.width(18.dp))
                Text(t.name, style = MaterialTheme.typography.bodySmall, color = Palette.Bone)
                Spacer(Modifier.width(8.dp))
                Text(
                    when {
                        t.sentMs > 0 -> ago(t.sentMs)
                        t.error.isNotEmpty() -> "waiting: ${t.error}"
                        else -> "waiting"
                    },
                    style = MaterialTheme.typography.bodySmall,
                    color = if (t.sentMs == 0L && t.error.isNotEmpty()) Palette.Rust else Palette.Ash,
                    maxLines = 2, overflow = TextOverflow.Ellipsis,
                )
            }
        }
    }
}
