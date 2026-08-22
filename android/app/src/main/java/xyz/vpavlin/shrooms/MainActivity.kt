package xyz.vpavlin.shrooms

import android.Manifest
import android.content.Intent
import android.net.Uri
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import mobile.Mobile

class MainActivity : ComponentActivity() {

    private val vpnConsent = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) { r ->
        if (r.resultCode == RESULT_OK) startService(connectIntent()) else MeshState.fail("VPN permission refused")
    }

    /** Set by JoinScreen so a scan result can be routed back to it. */
    private var onScanned: ((String) -> Unit)? = null

    private val scanner = registerForActivityResult(ScanContract()) { result ->
        result.contents?.let { onScanned?.invoke(it) }
    }

    fun scanInvite(onResult: (String) -> Unit) {
        onScanned = onResult
        scanner.launch(
            ScanOptions()
                .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                .setPrompt("Scan the code from `shrooms key show --qr`")
                .setBeepEnabled(false)
                .setOrientationLocked(false)
        )
    }

    private val notificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* the tunnel works either way */ }

    override fun onCreate(savedInstanceState: Bundle?) {
        // Edge to edge, then inset the content ourselves. Android 15 makes this
        // the default whether or not an app asks, so a layout that ignores the
        // insets has its header under the status bar — which is where the
        // version line went.
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)

        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            notificationPermission.launch(Manifest.permission.POST_NOTIFICATIONS)
        }

        setContent {
            LogosTheme {
                val dir = filesDir.absolutePath
                var configured by remember { mutableStateOf(Mobile.configured(dir)) }
                var addingMesh by remember { mutableStateOf(false) }
                var inSettings by remember { mutableStateOf(false) }
                var inviting by remember { mutableStateOf(false) }
                // Which picture the graph draws. It is set in settings and read
                // by the mesh screen, so it belongs to neither of them — and it
                // is saveable because a rotation that quietly reverts a setting
                // looks like the setting did not take.
                // Kept across launches, not just across rotations. It was
                // rememberSaveable, which survives a rotation and dies with
                // the process — so a setting chosen deliberately was gone by
                // the next time the app was opened, which reads as the setting
                // not having worked rather than as its scope.
                //
                // SharedPreferences rather than the daemon's config: this is a
                // property of this screen, not of the mesh, and a display
                // choice does not belong in a file every node reads.
                val prefs = remember { getSharedPreferences("view", MODE_PRIVATE) }
                var wholeMesh by remember {
                    mutableStateOf(prefs.getBoolean("whole_mesh", false))
                }

                // Connect on launch, once. A mesh VPN that has to be switched
                // on by hand every time is a mesh that is off when you need it
                // — and the consent dialog only appears the first time, so
                // there is nothing to interrupt afterwards.
                var autoConnected by rememberSaveable { mutableStateOf(false) }
                LaunchedEffect(configured) {
                    if (configured && !autoConnected) {
                        autoConnected = true
                        requestConnect()
                    }
                }
                val snap by MeshState.snapshot.collectAsStateWithLifecycle()

                Surface(
                    color = Palette.Void,
                    modifier = Modifier.fillMaxSize().safeDrawingPadding(),
                ) {
                    if (!configured) {
                        JoinScreen(dir, onScan = ::scanInvite) { configured = true }
                    } else if (addingMesh) {
                        AddMeshScreen(
                            dir = dir,
                            onScan = ::scanInvite,
                            onDone = {
                                addingMesh = false
                                // A new mesh gets its addresses and routes when
                                // the tunnel is built, so it does nothing until
                                // the next connect. Reconnecting here rather
                                // than asking: the alternative is a join that
                                // succeeded and a mesh that appears broken.
                                if (snap.connected) {
                                    startService(Intent(this@MainActivity, MeshVpnService::class.java)
                                        .setAction(MeshVpnService.ACTION_RECONNECT))
                                } else {
                                    requestConnect()
                                }
                            },
                            onCancel = { addingMesh = false },
                        )
                    } else if (inviting) {
                        // Same reason as settings: a state-swapped screen, so
                        // back has to mean "leave this screen" rather than
                        // "close the app". More so here, where leaving by
                        // accident drops an invite somebody is mid-way through
                        // scanning.
                        BackHandler { inviting = false }
                        InviteScreen(
                            dir = dir,
                            // The first mesh, which is what a phone with one
                            // mesh means. Inviting to a specific one of several
                            // needs a picker; until there is one, this admits
                            // to the mesh the phone thinks of as its own.
                            meshLabel = "",
                            onClose = { inviting = false },
                        )
                    } else if (inSettings) {
                        // System back leaves settings rather than the app: this
                        // is a screen swapped in by state, not an Activity, so
                        // without this the back gesture closes shrooms from
                        // what looks like a sub-screen.
                        BackHandler { inSettings = false }
                        SettingsScreen(
                            dir = dir,
                            connected = snap.connected,
                            wholeMesh = wholeMesh,
                            onWholeMesh = {
                                wholeMesh = it
                                prefs.edit().putBoolean("whole_mesh", it).apply()
                            },
                            onClose = { inSettings = false },
                        )
                    } else {
                        MeshScreen(
                            snap = snap,
                            dir = dir,
                            wholeMesh = wholeMesh,
                            onConnect = ::requestConnect,
                            onDisconnect = {
                                startService(Intent(this, MeshVpnService::class.java)
                                    .setAction(MeshVpnService.ACTION_DISCONNECT))
                            },
                            onAddMesh = { addingMesh = true },
                            onSettings = { inSettings = true },
                            onInvite = { inviting = true },
                            onLeftMesh = {
                                // The tunnel is built from the config at
                                // connect time, so a mesh added, removed or
                                // switched needs it rebuilt. One intent, so the
                                // stop and the start cannot arrive out of order.
                                if (snap.connected) {
                                    startService(Intent(this@MainActivity, MeshVpnService::class.java)
                                        .setAction(MeshVpnService.ACTION_RECONNECT))
                                } else {
                                    requestConnect()
                                }
                            },
                        )
                    }
                }
            }
        }
    }

    private fun connectIntent() = Intent(this, MeshVpnService::class.java)
        .setAction(MeshVpnService.ACTION_CONNECT)

    /** Android requires explicit consent before any app may hold a tunnel. */
    private fun requestConnect() {
        val intent = VpnService.prepare(this)
        if (intent != null) vpnConsent.launch(intent) else startService(connectIntent())
    }
}

// --- screens ---------------------------------------------------------------

@Composable
private fun JoinScreen(dir: String, onScan: ((String) -> Unit) -> Unit, onDone: () -> Unit) {
    var key by remember { mutableStateOf("") }
    var name by remember { mutableStateOf(Build.MODEL.replace(' ', '-').lowercase()) }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var waiting by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    // An invite token and a network key are told apart by Go, not by asking:
    // one is 26 characters and the other 52, so a single field can take either
    // and nobody has to know which they were handed.
    val isInvite = key.isNotEmpty() && runCatching { Mobile.inviteToken(key) }.getOrNull()
        .orEmpty().isNotEmpty()

    Column(
        Modifier.fillMaxSize().padding(24.dp).verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.Center,
    ) {
        Text("Shrooms", style = MaterialTheme.typography.displaySmall, color = Palette.Bone)
        Spacer(Modifier.height(6.dp))
        Text(
            "an overlay mesh between your own devices",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )
        Spacer(Modifier.height(4.dp))
        Text(buildLabel(), style = MaterialTheme.typography.bodySmall, color = Palette.Ash)

        Spacer(Modifier.height(40.dp))
        Label(if (isInvite) "INVITE" else "INVITE OR NETWORK KEY")
        Spacer(Modifier.height(8.dp))
        KeyField(key) { key = it.trim() }

        Spacer(Modifier.height(12.dp))
        Action("SCAN A CODE", enabled = !busy) {
            error = ""
            onScan { scanned ->
                // Parsed in Go, so the invitation format has one implementation
                // and the app cannot drift from what the CLI writes. An invite
                // token is tried first: it is the one that expires, so a stale
                // scan should say so rather than falling through to a key.
                val token = runCatching { Mobile.inviteToken(scanned) }.getOrNull().orEmpty()
                if (token.isNotEmpty()) {
                    key = token
                } else {
                    runCatching { Mobile.inviteKey(scanned) }
                        .onSuccess { key = it }
                        .onFailure { error = it.message ?: "that is not a mesh invitation" }
                }
            }
        }

        Spacer(Modifier.height(20.dp))
        Label("THIS DEVICE")
        Spacer(Modifier.height(8.dp))
        KeyField(name, singleLine = true) { name = it.trim() }

        if (error.isNotEmpty()) {
            Spacer(Modifier.height(16.dp))
            Text(error, color = Palette.Rust, style = MaterialTheme.typography.bodySmall)
        }

        Spacer(Modifier.height(28.dp))
        Action(
            when {
                waiting -> "WAITING FOR THE OTHER DEVICE…"
                busy -> "JOINING…"
                else -> "JOIN"
            },
            enabled = key.isNotEmpty() && !busy,
        ) {
            busy = true
            error = ""
            if (isInvite) {
                // Redeeming an invite talks to the other machine and can take
                // the better part of a minute, so it cannot run here: the main
                // thread is what draws the "waiting" state it produces.
                waiting = true
                scope.launch {
                    val result = withContext(Dispatchers.IO) {
                        runCatching { Mobile.joinWithInvite(key, name, dir, 120L) }
                    }
                    waiting = false
                    result
                        .onSuccess { onDone() }
                        .onFailure { error = it.message ?: "could not join"; busy = false }
                }
            } else {
                runCatching { Mobile.join(key, name, dir) }
                    .onSuccess { onDone() }
                    .onFailure { error = it.message ?: "could not join"; busy = false }
            }
        }

        Spacer(Modifier.height(14.dp))
        Text(
            if (isInvite)
                "An invite is good for one device and fifteen minutes. Keep " +
                    "`shrooms invite` running on the other machine until this finishes."
            else
                "The key is the only secret. Anyone who has it is a member of the mesh — " +
                    "treat it like a password. An invite is the safer way in.",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )

        Spacer(Modifier.height(32.dp))
        Text(
            "no mesh yet? create one",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Phosphor,
            modifier = Modifier.clickable(enabled = !busy) {
                busy = true
                runCatching { Mobile.init(name, dir) }
                    .onSuccess { onDone() }
                    .onFailure { error = it.message ?: "could not create"; busy = false }
            },
        )
    }
}

/**
 * Join a second mesh, keeping the ones this device already has (ADR-015).
 *
 * The name is asked for and required, because it is local to this device and
 * there is no way to learn it later — mesh names deliberately never travel on
 * the wire, so nobody else can tell you what to call theirs.
 */
@Composable
private fun AddMeshScreen(
    dir: String,
    onScan: ((String) -> Unit) -> Unit,
    onDone: () -> Unit,
    onCancel: () -> Unit,
) {
    var token by remember { mutableStateOf("") }
    var label by remember { mutableStateOf("") }
    // The name this device already answers to, not the phone's model. Offering
    // the model here renamed a device that was perfectly well named, on every
    // mesh it was already on, with no way back.
    var name by remember {
        mutableStateOf(
            Mobile.deviceName(dir).ifEmpty { Build.MODEL.replace(' ', '-').lowercase() }
        )
    }
    var error by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    val scope = rememberCoroutineScope()

    Column(
        Modifier.fillMaxSize().padding(24.dp).verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.Center,
    ) {
        Text("Another mesh", style = MaterialTheme.typography.headlineSmall, color = Palette.Bone)
        Spacer(Modifier.height(6.dp))
        Text(
            "joining a second mesh does not connect the two — this device is an " +
                "endpoint on each, not a bridge between them",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )

        Spacer(Modifier.height(28.dp))
        Label("INVITE")
        Spacer(Modifier.height(8.dp))
        KeyField(token) { token = it.trim() }

        Spacer(Modifier.height(12.dp))
        Action("SCAN A CODE", enabled = !busy) {
            error = ""
            onScan { scanned ->
                val t = runCatching { Mobile.inviteToken(scanned) }.getOrNull().orEmpty()
                if (t.isNotEmpty()) token = t else error = "that is not an invite"
            }
        }

        Spacer(Modifier.height(20.dp))
        Label("NAME FOR THIS MESH")
        Spacer(Modifier.height(6.dp))
        Text(
            "your own label for it — “home”, “bobs” — used in names like " +
                "nas.home.mesh. Only this phone sees it.",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )
        Spacer(Modifier.height(8.dp))
        KeyField(label, singleLine = true) { label = it.trim() }

        Spacer(Modifier.height(20.dp))
        Label("NAME FOR THIS PHONE")
        Spacer(Modifier.height(6.dp))
        Text(
            "how other devices see this one — the same on every mesh, so " +
                "changing it here renames it everywhere",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )
        Spacer(Modifier.height(8.dp))
        KeyField(name, singleLine = true) { name = it.trim() }

        if (error.isNotEmpty()) {
            Spacer(Modifier.height(16.dp))
            Text(error, color = Palette.Rust, style = MaterialTheme.typography.bodySmall)
        }

        Spacer(Modifier.height(28.dp))
        Action(
            if (busy) "WAITING — KEEP `shrooms invite` RUNNING" else "ADD",
            enabled = token.isNotEmpty() && label.isNotEmpty() && !busy,
        ) {
            busy = true
            error = ""
            scope.launch {
                val result = withContext(Dispatchers.IO) {
                    runCatching {
                        // Applied deliberately rather than as a side effect of
                        // joining: the field is prefilled with the current
                        // name, so this only fires if it was actually edited.
                        Mobile.setDeviceName(dir, name)
                        Mobile.joinAnotherWithInvite(token, label, name, dir, 120L)
                    }
                }
                result
                    .onSuccess { onDone() }
                    .onFailure { error = it.message ?: "could not join"; busy = false }
            }
        }

        Spacer(Modifier.height(14.dp))
        Text(
            "the tunnel reconnects when this finishes, so the new mesh comes up",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )

        Spacer(Modifier.height(24.dp))
        Text(
            "cancel",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Phosphor,
            modifier = Modifier.clickable(enabled = !busy) { onCancel() },
        )
    }
}

@Composable
private fun MeshScreen(
    snap: Snapshot,
    dir: String,
    wholeMesh: Boolean,
    onConnect: () -> Unit,
    onDisconnect: () -> Unit,
    onAddMesh: () -> Unit = {},
    onSettings: () -> Unit = {},
    onInvite: () -> Unit = {},
    onLeftMesh: () -> Unit = {},
) {
    // Leaving edits the config; the running session still holds the mesh it
    // was told about at connect time. Without saying so, the button looks
    // like it did nothing at all.
    var leaveError by remember { mutableStateOf("") }
    // Which mesh has been tapped once. Leaving means re-enrolling to come
    // back, which is too much to hang on a single tap next to a toggle.
    var confirmLeave by remember { mutableStateOf("") }

    // An armed confirmation disarms itself.
    //
    // Tapping "leave" by mistake otherwise leaves "sure?" sitting there
    // indefinitely, one slip away from a mesh you have to re-enrol into — and
    // the way out was to notice the small "cancel" beside it. A destructive
    // confirmation should expire on its own: the safe state is the resting
    // state, and time should return you to it.
    //
    // Thirty seconds is long enough to read the sentence underneath and decide,
    // and short enough that a phone put down and picked up again is disarmed.
    LaunchedEffect(confirmLeave) {
        if (confirmLeave.isNotEmpty()) {
            delay(30_000)
            confirmLeave = ""
        }
    }
    val clipboard = LocalClipboardManager.current

    Column(Modifier.fillMaxSize()) {
        Column(Modifier.padding(start = 24.dp, end = 24.dp, top = 32.dp, bottom = 20.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Dot(
                    when {
                        snap.error.isNotEmpty() -> Palette.Rust
                        snap.connected -> Palette.Phosphor
                        else -> Palette.Ash
                    },
                    size = 10,
                )
                Spacer(Modifier.width(10.dp))
                Text(
                    if (snap.connected) "CONNECTED" else "OFFLINE",
                    style = MaterialTheme.typography.labelSmall,
                    color = if (snap.connected) Palette.Phosphor else Palette.Ash,
                )
            }
            Spacer(Modifier.height(12.dp))
            val self = snap.overlay.ifEmpty { Mobile.overlayAddress(dir) }
            var selfCopied by remember { mutableStateOf(false) }
            LaunchedEffect(selfCopied) {
                if (selfCopied) { kotlinx.coroutines.delay(1200); selfCopied = false }
            }
            // Which meshes this device is on, when there is more than one.
            // Each has its own address here, and the roster below is grouped
            // the same way.
            val allMeshes = remember(snap.meshes, snap.connected) { meshesFromConfig(dir) }
            if (allMeshes.size > 1) {
                allMeshes.forEach { cm ->
                    val m = snap.meshes.firstOrNull { it.label == cm.label }
                        ?: MeshInfo(cm.label, "", "", 0)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(
                            if (cm.disabled) "${cm.label}  — off"
                            else "${m.label}  ${m.overlay}  ·  ${m.peers} peer${if (m.peers == 1) "" else "s"}",
                            style = MaterialTheme.typography.bodySmall,
                            color = if (cm.disabled) Palette.Line else Palette.Ash,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f),
                        )
                        // On or off without leaving — being a member of a mesh
                        // and using it are different things. Text rather than a
                        // Switch: these rows are one line of small type, and a
                        // Material switch is twice their height.
                        if (cm.label != "default") {
                            Text(
                                if (cm.disabled) "off" else "on",
                                style = MaterialTheme.typography.labelSmall,
                                color = if (cm.disabled) Palette.Line else Palette.Phosphor,
                                modifier = Modifier
                                    .padding(horizontal = 10.dp)
                                    .clickable {
                                        leaveError = ""
                                        runCatching { Mobile.setMeshEnabled(dir, cm.label, cm.disabled) }
                                            .onSuccess { onLeftMesh() }
                                            .onFailure {
                                                leaveError = it.message ?: "could not change it"
                                            }
                                    },
                            )
                        }
                        // The way back from a join that went wrong. Only the
                        // added meshes: leaving the original one is "forget
                        // everything", which is what clearing app data is for.
                        if (cm.label != "default") {
                            val armed = confirmLeave == cm.label
                            // Armed, the row offers both answers. A confirm
                            // with no way back is a trap, and tapping
                            // elsewhere is not an obvious way out of one.
                            if (armed) {
                                Text(
                                    "cancel",
                                    style = MaterialTheme.typography.labelSmall,
                                    color = Palette.Line,
                                    modifier = Modifier
                                        .padding(end = 10.dp)
                                        .clickable { confirmLeave = ""; leaveError = "" },
                                )
                            }
                            Text(
                                if (armed) "sure?" else "leave",
                                style = MaterialTheme.typography.labelSmall,
                                color = if (armed) Palette.Amber else Palette.Rust,
                                modifier = Modifier.clickable {
                                    leaveError = ""
                                    if (!armed) {
                                        confirmLeave = cm.label
                                        return@clickable
                                    }
                                    confirmLeave = ""
                                    runCatching { Mobile.leaveMesh(dir, cm.label) }
                                        // Reconnect, or the mesh stays up until
                                        // something else restarts the tunnel and
                                        // the button appears to have done nothing.
                                        .onSuccess { onLeftMesh() }
                                        .onFailure {
                                            leaveError = it.message ?: "could not leave"
                                        }
                                },
                            )
                        }
                    }
                }
                if (confirmLeave.isNotEmpty()) {
                    Text(
                        "leaving $confirmLeave means re-enrolling with a new invite to come back",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Amber,
                    )
                }
                if (leaveError.isNotEmpty()) {
                    Text(
                        leaveError,
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Rust,
                    )
                }
                Spacer(Modifier.height(8.dp))
            }
            Text(
                if (selfCopied) "copied" else self,
                style = MaterialTheme.typography.bodyMedium,
                color = if (selfCopied) Palette.Phosphor else Palette.Bone,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.clickable {
                    clipboard.setText(AnnotatedString(self))
                    selfCopied = true
                },
            )
            if (snap.dnsName.isNotEmpty()) {
                var nameCopied by remember { mutableStateOf(false) }
                LaunchedEffect(nameCopied) {
                    if (nameCopied) { kotlinx.coroutines.delay(1200); nameCopied = false }
                }
                Text(
                    if (nameCopied) "copied" else snap.dnsName,
                    style = MaterialTheme.typography.bodySmall,
                    color = if (nameCopied) Palette.Phosphor else Palette.Ash,
                    modifier = Modifier.clickable {
                        clipboard.setText(AnnotatedString(snap.dnsName))
                        nameCopied = true
                    },
                )
            } else if (snap.name.isNotEmpty()) {
                Text(snap.name, style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
            }
        }

        // Whether .mesh names resolve, stated plainly and in both directions.
        //
        // Only warning on failure was not enough: "I see nothing about DNS"
        // is indistinguishable from "there is nothing to say", so the working
        // case has to be visible too. Peers appear and addresses work either
        // way, so nothing else on this screen implies it.
        if (snap.connected) {
            if (snap.names.isEmpty()) {
                Banner("names not available — .mesh will not resolve, addresses still work", Palette.Amber)
            } else {
                // One line when it is working, the counters on request — the
                // same shape as the log below. The state is what you want
                // every time; the numbers are what you want on the one day
                // something is wrong with them.
                var showDns by remember { mutableStateOf(false) }
                val quiet = snap.dns.intercepted == 0L
                Text(
                    when {
                        showDns -> "dns  ${snap.dns.summary()}"
                        quiet -> "dns  no queries yet"
                        else -> "dns  on  ·  ${snap.names}"
                    },
                    style = MaterialTheme.typography.labelSmall,
                    color = if (quiet) Palette.Amber else Palette.Ash,
                    modifier = Modifier
                        .padding(start = 24.dp, end = 24.dp, bottom = 6.dp)
                        .clickable { showDns = !showDns },
                )
            }
        }

        if (snap.error.isNotEmpty()) Banner(snap.error, Palette.Rust)

        // A log tail in the app, because the alternative is asking someone to
        // run adb logcat — and a failure nobody can read is a failure nobody
        // can report.
        val logs by MeshState.logs.collectAsStateWithLifecycle()
        var showLogs by remember { mutableStateOf(false) }
        if (logs.isNotEmpty()) {
            Text(
                if (showLogs) "hide log" else "show log (${logs.size})",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Ash,
                modifier = Modifier.padding(horizontal = 24.dp, vertical = 4.dp)
                    .clickable { showLogs = !showLogs },
            )
            if (showLogs) {
                Column(
                    Modifier.fillMaxWidth().heightIn(max = 220.dp)
                        .padding(horizontal = 16.dp)
                        .background(Palette.Panel, RoundedCornerShape(8.dp))
                        .verticalScroll(rememberScrollState())
                        .padding(10.dp),
                ) {
                    logs.takeLast(60).forEach { line ->
                        Text(
                            line,
                            style = MaterialTheme.typography.bodySmall,
                            color = if (line.startsWith("ERROR")) Palette.Rust else Palette.Ash,
                        )
                    }
                }
            }
        }

        // Rendezvous trouble is reported separately from peers being offline.
        // They look identical and have entirely different causes; conflating
        // them sends you to debug the wrong plane.
        if (snap.connected && !snap.rendezvous.ok && snap.rendezvous.problem.isNotEmpty()) {
            Banner(
                "discovery: ${snap.rendezvous.problem}\nexisting tunnels are unaffected",
                Palette.Amber,
            )
        }

        // A device with no address to give is undialable, and that is invisible
        // from everything else on this screen: peers appear, discovery looks
        // healthy, and every handshake fails.
        //
        // Guarded on more than "no addresses", because on Android that alone is
        // the *normal* state — the OS will not let an untrusted app list its
        // own interfaces, so a phone has nothing to announce until some peer
        // reaches it and reports back. Saying so during an ordinary start would
        // be an alarm on every healthy connect, which is how people learn to
        // ignore banners.
        //
        // So: no address, peers to talk to, and not one of them has ever
        // completed a handshake. That combination is stuck rather than starting.
        val stuck = snap.connected &&
            snap.announced != null && snap.announced.isEmpty() &&
            snap.peers.isNotEmpty() && snap.peers.none { it.live || it.handshakeAgeS > 0 }
        if (stuck) {
            Banner(
                "no peer can dial this device: it has no address to give\n" +
                    "it becomes reachable once something reaches it first — a relay, " +
                    "or a peer with a public address",
                Palette.Amber,
            )
        }

        var asGraph by remember { mutableStateOf(true) }
        var selected by remember { mutableStateOf<Peer?>(null) }

        if (snap.peers.isNotEmpty()) {
            Row(
                Modifier.fillMaxWidth().padding(horizontal = 24.dp, vertical = 2.dp),
                horizontalArrangement = Arrangement.End,
            ) {
                Text(
                    if (asGraph) "list" else "graph",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                    modifier = Modifier.clickable { asGraph = !asGraph; selected = null },
                )
            }
        }

        Box(Modifier.weight(1f)) {
            when {
                // Nothing to draw yet, so draw the thing that is actually
                // happening: spores drifting and finding each other. It also
                // makes the difference between "connected, nobody found" and
                // "not connected" legible without reading the label.
                snap.peers.isEmpty() -> SporeField(
                    modifier = Modifier.align(Alignment.Center),
                    label = if (snap.connected) "looking for peers…" else "not connected",
                )

                asGraph -> {
                    MeshGraph(snap, onSelect = { selected = it }, inferred = wholeMesh)
                    // Only the whole-mesh picture is labelled, and only to
                    // disown the extra links: they are inferred from what peers
                    // report, so an unmarked drawing of them would pass off a
                    // guess as a measurement. The default picture is all
                    // measured and needs nothing said about it.
                    if (wholeMesh) {
                        Text(
                            "whole mesh · faint links are assumed",
                            style = MaterialTheme.typography.labelSmall,
                            color = Palette.Amber,
                            modifier = Modifier
                                .align(Alignment.TopCenter)
                                .padding(top = 6.dp),
                        )
                    }
                    selected?.let { peer ->
                        // The detail for a tapped node, over the graph rather
                        // than replacing it: the shape is the context.
                        Box(
                            Modifier.align(Alignment.BottomCenter)
                                .padding(16.dp)
                                .background(Palette.Panel, RoundedCornerShape(10.dp))
                                .border(1.dp, Palette.Line, RoundedCornerShape(10.dp))
                                .padding(14.dp),
                        ) {
                            Column { PeerDetails(peer) }
                        }
                    }
                }

                // Grouped by mesh when there is more than one. Two meshes may
                // hold devices with the same name, and a flat list gives no way
                // to tell which network a peer is actually on.
                else -> LazyColumn(contentPadding = PaddingValues(horizontal = 16.dp)) {
                    val grouped = snap.peers.groupBy { it.mesh }
                    if (grouped.size <= 1) {
                        items(snap.peers) { PeerRow(it) }
                    } else {
                        grouped.toSortedMap().forEach { (mesh, peers) ->
                            item {
                                Text(
                                    mesh.ifEmpty { "default" }.uppercase(),
                                    style = MaterialTheme.typography.labelSmall,
                                    color = Palette.Violet,
                                    modifier = Modifier.padding(top = 14.dp, bottom = 6.dp),
                                )
                            }
                            items(peers) { PeerRow(it) }
                        }
                    }
                }
            }
        }

        // Joining is an action and stays here; everything that was a preference
        // moved behind the link next to it, because each one used to spend
        // vertical space on this screen arguing for itself.
        // What the mesh offers, as a list rather than as something you find by
        // tapping a peer and reading its details.
        //
        // A service name exists so you can go there, and a name you have to go
        // looking for is one you keep asking somebody for instead. Every peer
        // that announces (ADR-023) contributes its names here, with the device
        // shown, because two devices may publish the same name and the device
        // is what tells them apart.
        val svcCtx = LocalContext.current
        // Carries reachability, because a list outlives it on purpose: a
        // service is a claim about what a device offers, and a device that is
        // asleep still offers it. Drawn the same as a reachable one, though, it
        // invites somebody to tap an address that cannot answer.
        data class Offer(
            val name: String,
            val mesh: String,
            val url: String,
            val live: Boolean,
            /** A port merely listening on the mesh address, not a published service. */
            val bound: Boolean,
        )
        // The mesh rather than the device, because the URL already names the
        // device — immich.k11.mesh — and which mesh a service is on is the
        // thing you cannot read off it. Shown only when there is more than
        // one, where it is the answer to "why can I not reach that".
        val offered = remember(snap.peers) {
            snap.peers.flatMap { p ->
                val host = p.dnsName.ifEmpty { p.name }
                p.services.map { svc ->
                    Offer(svc, p.mesh, "http://$svc.$host", p.live, false)
                } + p.bound.map { b ->
                    // "ssh:22" — the name is advisory and the port is the fact,
                    // so the port is what goes in the address. No scheme: this
                    // is a socket on the mesh address, not a forwarded service,
                    // and http:// would open the wrong thing convincingly.
                    val port = b.substringAfterLast(':', "")
                    Offer(
                        b.substringBefore(':'), p.mesh,
                        host + if (port.isEmpty()) "" else ":$port",
                        p.live, true,
                    )
                }
            }.sortedWith(compareBy({ !it.live }, { it.mesh }, { it.name }))
        }
        val showMesh = snap.meshes.size > 1
        if (offered.isNotEmpty()) {
            Spacer(Modifier.height(18.dp))
            Box(Modifier.padding(horizontal = 24.dp)) { Label("SERVICES") }
            Spacer(Modifier.height(6.dp))
            offered.forEach { (name, mesh, url, live, isBound) ->
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier
                        .fillMaxWidth()
                        .clickable {
                            // A bound port is a host and a port with no scheme
                            // (ADR-026), so there is nothing to open — guessing
                            // http:// for an ssh port would open the wrong
                            // thing convincingly. It copies instead, which is
                            // what you want it for on a phone anyway.
                            if (isBound) {
                                clipboard.setText(AnnotatedString(url))
                                return@clickable
                            }
                            // Opening it is the whole point. A browser is the
                            // right guess for a mesh service and the wrong one
                            // for some, so a failure to resolve a handler is
                            // ignored rather than reported: the address is
                            // still on screen to copy.
                            runCatching {
                                svcCtx.startActivity(
                                    Intent(Intent.ACTION_VIEW, Uri.parse(url))
                                        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                                )
                            }
                        }
                        .padding(horizontal = 24.dp, vertical = 5.dp),
                ) {
                    Text(
                        name,
                        style = MaterialTheme.typography.bodySmall,
                        color = if (live) Palette.Phosphor else Palette.Ash,
                        modifier = Modifier.width(96.dp),
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    if (showMesh) {
                        Text(
                            mesh,
                            style = MaterialTheme.typography.labelSmall,
                            color = Palette.Ash,
                            modifier = Modifier.width(64.dp),
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    }
                    Text(
                        url.removePrefix("http://")
                            + (if (isBound) "  ·  bound" else "")
                            + (if (live) "" else "  ·  unreachable"),
                        style = MaterialTheme.typography.labelSmall,
                        color = if (live) Palette.Bone else Palette.Line,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            // Said once, under the list: this is what peers claim to publish,
            // and only the publishing device knows whether the port answers.
            Text(
                "announced by their devices; not checked from here",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash,
                modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 2.dp),
            )
        }

        Spacer(Modifier.height(18.dp))
        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier.padding(horizontal = 24.dp),
        ) {
            Text(
                "join another mesh",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Phosphor,
                modifier = Modifier.clickable { onAddMesh() },
            )
            Spacer(Modifier.width(16.dp))
            Text(
                "invite a device",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Phosphor,
                modifier = Modifier.clickable { onInvite() },
            )
            Spacer(Modifier.width(16.dp))
            Text(
                "settings",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Phosphor,
                modifier = Modifier.clickable { onSettings() },
            )
        }

        Text(
            buildLabel(),
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
            modifier = Modifier.padding(start = 24.dp, end = 24.dp, top = 4.dp),
        )
        // Two things keep this out of the way of the system gesture.
        //
        // A swipe up from the bottom edge starts inside the last few
        // millimetres of the screen, and safeDrawingPadding only clears the
        // navigation bar — which in gesture mode is a thin handle, not the
        // area the gesture actually claims. So the button sits well above it.
        //
        // And disconnecting takes two taps. Spacing makes a stray hit less
        // likely; it cannot make it impossible, and the cost of being wrong is
        // asymmetric — connecting by accident is a moment's confusion, while
        // disconnecting by accident drops every tunnel on the phone and,
        // if you are away from home, the way back into the machines you were
        // reaching. Connect stays one tap, because nothing about it is
        // destructive.
        var confirming by remember { mutableStateOf(false) }
        LaunchedEffect(confirming) {
            if (confirming) {
                kotlinx.coroutines.delay(3_000)
                confirming = false
            }
        }
        Box(Modifier.padding(start = 24.dp, end = 24.dp, top = 4.dp, bottom = 48.dp)) {
            Action(
                when {
                    !snap.connected -> "CONNECT"
                    confirming -> "TAP AGAIN TO DISCONNECT"
                    else -> "DISCONNECT"
                },
                enabled = true,
                danger = snap.connected,
            ) {
                when {
                    !snap.connected -> onConnect()
                    confirming -> { confirming = false; onDisconnect() }
                    else -> confirming = true
                }
            }
        }
    }
}

/** One mesh as the config knows it, including ones that are switched off. */
private data class ConfigMesh(val label: String, val disabled: Boolean)

private fun meshesFromConfig(dir: String): List<ConfigMesh> =
    runCatching {
        val arr = org.json.JSONArray(Mobile.meshesJSON(dir))
        (0 until arr.length()).map { i ->
            val o = arr.getJSONObject(i)
            ConfigMesh(o.optString("label"), o.optBoolean("disabled"))
        }
    }.getOrDefault(emptyList())

@Composable
private fun PeerRow(p: Peer) {
    var open by remember { mutableStateOf(false) }
    val colour = when (p.reach) {
        Peer.Reach.CONNECTED -> Palette.Phosphor
        Peer.Reach.REACHING -> Palette.Amber
        Peer.Reach.OFFLINE -> Palette.Ash
    }

    Column(
        Modifier.fillMaxWidth().padding(vertical = 4.dp)
            .background(Palette.Panel, RoundedCornerShape(10.dp))
            .clickable { open = !open }
            .padding(14.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Dot(colour)
            Spacer(Modifier.width(10.dp))
            Text(p.name, style = MaterialTheme.typography.titleMedium, color = Palette.Bone, modifier = Modifier.weight(1f))
            if (p.relay) {
                Text("RELAY", style = MaterialTheme.typography.labelSmall, color = Palette.Violet)
                Spacer(Modifier.width(8.dp))
            }
            Text(
                if (p.reach == Peer.Reach.CONNECTED && p.rttMs > 0) "${p.rttMs}ms" else p.how,
                style = MaterialTheme.typography.bodySmall,
                color = if (p.relayed) Palette.Violet else Palette.Ash,
            )
        }

        if (open) {
            Spacer(Modifier.height(12.dp))
            PeerDetails(p)
        }
    }
}

/** The facts about one peer, shared by the list and the graph. */
@Composable
private fun PeerDetails(p: Peer) {
    Column {
        // Copyable: these are the two ways to reach the peer, and retyping
        // either off a phone screen is not a plan.
        if (p.dnsName.isNotEmpty()) CopyableDetail("name", p.dnsName)
        CopyableDetail("address", p.overlay)
        // For everything that still cannot speak IPv6, which on a phone is a
        // long list. Copyable for the same reason the others are: this exists
        // to be pasted into another app's server field.
        if (p.overlayV4.isNotEmpty()) CopyableDetail("ipv4", p.overlayV4)
        Detail("path", if (p.relayed) "through a relay" else p.how)
        if (p.handshakeAgeS > 0) Detail("handshake", "${shortDuration(p.handshakeAgeS)} ago")
        if (p.tunnelAfterS > 0) Detail("connected in", "%.1fs".format(p.tunnelAfterS))
        Detail("traffic", "${humanBytes(p.rxBytes)} in · ${humanBytes(p.txBytes)} out")
        // What this peer says it offers (ADR-023). Copyable, because the whole
        // point of a service name is that you paste it into a browser — and
        // worded as a claim, because it is one: this peer repeats the list
        // every few minutes, and only it knows whether the port answers.
        for (svc in p.services) {
            CopyableDetail("offers", "http://$svc.${p.dnsName.ifEmpty { p.name }}")
        }
        // And the ports merely listening on its mesh address (ADR-026). A host
        // and a port rather than a URL, because there is no forwarder and no
        // scheme to guess — offering http:// for an ssh port would be a
        // confident lie.
        for (b in p.bound) {
            val port = b.substringAfterLast(':', "")
            CopyableDetail(
                "listening",
                p.dnsName.ifEmpty { p.name } + if (port.isEmpty()) "" else ":$port",
            )
        }
        Spacer(Modifier.height(8.dp))
        CopyableDetail("ping", "ping6 -c3 " + p.dnsName.ifEmpty { p.overlay })
    }
}

/**
 * Which build this is.
 *
 * Shown because "did you install the fixed one?" came up on every single
 * iteration, and neither of us could answer it from the screen.
 */
internal fun buildLabel(): String =
    "v${BuildConfig.VERSION_NAME} (${BuildConfig.VERSION_CODE})"

// --- small pieces ----------------------------------------------------------
//
// Internal rather than private to this file: the settings screen draws the same
// section headings and the same text field, and a second copy of them is how
// two screens in one app end up looking like two apps.

@Composable internal fun Label(text: String) =
    Text(text, style = MaterialTheme.typography.labelSmall, color = Palette.Ash)

/**
 * A detail row that copies on tap and says so.
 *
 * A copy with no feedback is indistinguishable from a tap that missed, which is
 * how you end up pasting the previous thing you copied.
 */
@Composable
private fun CopyableDetail(key: String, value: String) {
    val clipboard = LocalClipboardManager.current
    var copied by remember { mutableStateOf(false) }
    LaunchedEffect(copied) { if (copied) { kotlinx.coroutines.delay(1200); copied = false } }

    Row(
        Modifier.fillMaxWidth().padding(vertical = 2.dp).clickable {
            clipboard.setText(AnnotatedString(value))
            copied = true
        },
    ) {
        Text(
            if (copied) "copied" else key,
            style = MaterialTheme.typography.bodySmall,
            color = if (copied) Palette.Phosphor else Palette.Ash,
            modifier = Modifier.width(110.dp),
        )
        Text(
            value,
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Bone,
            modifier = Modifier.weight(1f),
        )
    }
}

@Composable
private fun Detail(key: String, value: String) {
    Row(Modifier.fillMaxWidth().padding(vertical = 2.dp)) {
        Text(key, style = MaterialTheme.typography.bodySmall, color = Palette.Ash, modifier = Modifier.width(110.dp))
        Text(value, style = MaterialTheme.typography.bodySmall, color = Palette.Bone)
    }
}

@Composable
private fun Dot(colour: Color, size: Int = 8) =
    Box(Modifier.size(size.dp).background(colour, CircleShape))

@Composable
private fun Banner(text: String, colour: Color) {
    Text(
        text,
        style = MaterialTheme.typography.bodySmall,
        color = colour,
        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp)
            .border(1.dp, colour.copy(alpha = 0.4f), RoundedCornerShape(8.dp))
            .padding(12.dp),
    )
}

@Composable
internal fun KeyField(value: String, singleLine: Boolean = false, onChange: (String) -> Unit) {
    BasicTextField(
        value = value,
        onValueChange = onChange,
        singleLine = singleLine,
        textStyle = MaterialTheme.typography.bodyMedium.copy(color = Palette.Bone),
        cursorBrush = SolidColor(Palette.Phosphor),
        modifier = Modifier.fillMaxWidth()
            .background(Palette.Panel, RoundedCornerShape(8.dp))
            .border(1.dp, Palette.Line, RoundedCornerShape(8.dp))
            .padding(14.dp),
    )
}

@Composable
internal fun Action(text: String, enabled: Boolean, danger: Boolean = false, onClick: () -> Unit) {
    val colour = if (danger) Palette.Rust else Palette.Phosphor
    Box(
        Modifier.fillMaxWidth()
            .background(if (enabled) colour.copy(alpha = 0.12f) else Color.Transparent, RoundedCornerShape(10.dp))
            .border(1.dp, if (enabled) colour else Palette.Line, RoundedCornerShape(10.dp))
            .clickable(enabled = enabled) { onClick() }
            .padding(vertical = 16.dp),
        contentAlignment = Alignment.Center,
    ) {
        Text(text, style = MaterialTheme.typography.labelSmall, color = if (enabled) colour else Palette.Ash)
    }
}
