package dev.logos.vpn

import android.Manifest
import android.content.Intent
import android.content.pm.PackageManager
import android.net.VpnService
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalClipboardManager
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
                .setPrompt("Scan the code from `logos-vpn key show --qr`")
                .setBeepEnabled(false)
                .setOrientationLocked(false)
        )
    }

    private val notificationPermission =
        registerForActivityResult(ActivityResultContracts.RequestPermission()) { /* the tunnel works either way */ }

    override fun onCreate(savedInstanceState: Bundle?) {
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
                val snap by MeshState.snapshot.collectAsStateWithLifecycle()

                Surface(color = Palette.Void, modifier = Modifier.fillMaxSize()) {
                    if (!configured) {
                        JoinScreen(dir, onScan = ::scanInvite) { configured = true }
                    } else {
                        MeshScreen(
                            snap = snap,
                            dir = dir,
                            onConnect = ::requestConnect,
                            onDisconnect = {
                                startService(Intent(this, MeshVpnService::class.java)
                                    .setAction(MeshVpnService.ACTION_DISCONNECT))
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

    Column(
        Modifier.fillMaxSize().padding(24.dp).verticalScroll(rememberScrollState()),
        verticalArrangement = Arrangement.Center,
    ) {
        Text("logos-vpn", style = MaterialTheme.typography.displaySmall, color = Palette.Bone)
        Spacer(Modifier.height(6.dp))
        Text(
            "an overlay mesh between your own devices",
            style = MaterialTheme.typography.bodySmall, color = Palette.Ash,
        )

        Spacer(Modifier.height(40.dp))
        Label("NETWORK KEY")
        Spacer(Modifier.height(8.dp))
        KeyField(key) { key = it.trim() }

        Spacer(Modifier.height(12.dp))
        Action("SCAN A CODE", enabled = !busy) {
            error = ""
            onScan { scanned ->
                // Parsed in Go, so the invitation format has one implementation
                // and the app cannot drift from what the CLI writes.
                runCatching { Mobile.inviteKey(scanned) }
                    .onSuccess { key = it }
                    .onFailure { error = it.message ?: "that is not a mesh invitation" }
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
        Action(if (busy) "JOINING…" else "JOIN", enabled = key.isNotEmpty() && !busy) {
            busy = true
            error = ""
            runCatching { Mobile.join(key, name, dir) }
                .onSuccess { onDone() }
                .onFailure { error = it.message ?: "could not join"; busy = false }
        }

        Spacer(Modifier.height(14.dp))
        Text(
            "The key is the only secret. Anyone who has it is a member of the mesh — " +
                "treat it like a password.",
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

@Composable
private fun MeshScreen(snap: Snapshot, dir: String, onConnect: () -> Unit, onDisconnect: () -> Unit) {
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
            if (snap.name.isNotEmpty()) {
                Text(snap.name, style = MaterialTheme.typography.bodySmall, color = Palette.Ash)
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

        Box(Modifier.weight(1f)) {
            if (snap.peers.isEmpty()) {
                Text(
                    if (snap.connected) "looking for peers…" else "not connected",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                    modifier = Modifier.align(Alignment.Center),
                )
            } else {
                LazyColumn(contentPadding = PaddingValues(horizontal = 16.dp)) {
                    items(snap.peers) { PeerRow(it) }
                }
            }
        }

        Box(Modifier.padding(24.dp)) {
            Action(
                if (snap.connected) "DISCONNECT" else "CONNECT",
                enabled = true,
                danger = snap.connected,
            ) { if (snap.connected) onDisconnect() else onConnect() }
        }
    }
}

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
            // Copyable, because the address is the only way to reach this peer
            // until there is a resolver, and retyping 32 hex characters from a
            // phone screen is not a plan.
            CopyableDetail("address", p.overlay)
            Detail("path", if (p.relayed) "through a relay" else p.how)
            if (p.handshakeAgeS > 0) Detail("handshake", "${shortDuration(p.handshakeAgeS)} ago")
            if (p.tunnelAfterS > 0) Detail("connected in", "%.1fs".format(p.tunnelAfterS))
            Detail("traffic", "${humanBytes(p.rxBytes)} in · ${humanBytes(p.txBytes)} out")
            Spacer(Modifier.height(8.dp))
            CopyableDetail("ping", "ping6 -c3 ${p.overlay}")
        }
    }
}

// --- small pieces ----------------------------------------------------------

@Composable private fun Label(text: String) =
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
private fun KeyField(value: String, singleLine: Boolean = false, onChange: (String) -> Unit) {
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
private fun Action(text: String, enabled: Boolean, danger: Boolean = false, onClick: () -> Unit) {
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
