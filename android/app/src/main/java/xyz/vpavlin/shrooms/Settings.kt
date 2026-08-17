package xyz.vpavlin.shrooms

import android.content.Intent
import android.provider.Settings
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import mobile.Mobile

/**
 * Everything that is a preference rather than an action.
 *
 * These controls used to sit along the bottom edge of the mesh screen, where
 * each one had to justify the space it took from the thing the screen is for —
 * which is why the node mode was a one-line summary that had to be tapped open
 * and the graph toggle was a caption floating over the picture. Off the main
 * screen they can simply be rows, and the room that buys is what finally made
 * renaming a device possible.
 *
 * Joining another mesh deliberately stayed behind: it is something you do once,
 * not something you set.
 */
@Composable
fun SettingsScreen(
    dir: String,
    connected: Boolean,
    wholeMesh: Boolean,
    onWholeMesh: (Boolean) -> Unit,
    onClose: () -> Unit,
) {
    val ctx = LocalContext.current

    Column(
        Modifier.fillMaxSize().padding(vertical = 32.dp).verticalScroll(rememberScrollState()),
    ) {
        Text(
            "Settings",
            style = MaterialTheme.typography.headlineSmall,
            color = Palette.Bone,
            modifier = Modifier.padding(horizontal = 24.dp),
        )

        Spacer(Modifier.height(28.dp))
        DeviceNameSetting(dir)

        Spacer(Modifier.height(28.dp))
        ModeSetting(dir, connected = connected)

        // No SERVICES section here, deliberately.
        //
        // It controlled whether peers are told the names of services this
        // device publishes — and a phone publishes none. The toggle asked
        // about a disclosure that does not exist, and every setting nobody
        // needs is one more thing to read past to reach the ones they do. The
        // capability is untouched: announce_services is still in the config,
        // still honoured by the daemon, and still on the desktop where devices
        // actually publish things.
        Spacer(Modifier.height(28.dp))
        Column(Modifier.padding(horizontal = 24.dp)) {
            Label("GRAPH")
            Spacer(Modifier.height(8.dp))
            ToggleRow(
                title = "Draw the whole mesh",
                detail = if (wholeMesh) "your links and the ones between other peers"
                else "only this device's own links",
                checked = wholeMesh,
                onChange = onWholeMesh,
            )
            // Off by default, and the graph marks itself while it is on: the
            // links between other peers are inferred from what each of them
            // reports, never measured from here, and a drawn line that turns
            // out to be a guess is worse than no line at all.
            Text(
                "those extra links are assumed rather than measured",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash,
            )
        }

        Spacer(Modifier.height(28.dp))
        Column(Modifier.padding(horizontal = 24.dp)) {
            Label("STARTUP")
            Spacer(Modifier.height(8.dp))
            // The app reconnects itself on boot and after an update, but only
            // Android can bring it back when the app is killed, and only
            // Android can hold traffic until the tunnel is there. That switch
            // lives in system settings and cannot be set from here, so point
            // at it rather than pretend otherwise.
            Text(
                "start on boot",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Phosphor,
                modifier = Modifier.clickable {
                    runCatching {
                        ctx.startActivity(
                            Intent(Settings.ACTION_VPN_SETTINGS)
                                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                        )
                    }
                },
            )
            Spacer(Modifier.height(4.dp))
            Text(
                "Always-on VPN, in the system settings this opens, is the one " +
                    "that survives the app being killed",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash,
            )
        }

        Spacer(Modifier.height(36.dp))
        Text(
            "back",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Phosphor,
            modifier = Modifier.padding(horizontal = 24.dp).clickable { onClose() },
        )

        Spacer(Modifier.height(20.dp))
        Text(
            buildLabel(),
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
            modifier = Modifier.padding(horizontal = 24.dp),
        )
    }
}

/**
 * Renaming this device.
 *
 * There was no way to do this at all until now: the name was asked for once, at
 * join time, and a phone christened `sm-a536b` or `bobs-phone-2` was stuck with
 * it on every mesh it was on. The rename only rewrites the config — the name
 * rides the announce, so peers keep showing the old one until this device
 * reconnects, and the screen says exactly that rather than letting the user
 * conclude the rename failed.
 */
@Composable
private fun DeviceNameSetting(dir: String) {
    var saved by remember { mutableStateOf(runCatching { Mobile.deviceName(dir) }.getOrDefault("")) }
    var name by remember { mutableStateOf(saved) }
    var error by remember { mutableStateOf("") }
    var renamed by remember { mutableStateOf(false) }

    val changed = name.isNotEmpty() && name != saved

    Column(Modifier.padding(horizontal = 24.dp)) {
        Label("THIS DEVICE")
        Spacer(Modifier.height(6.dp))
        Text(
            "how other devices see this one — the same name on every mesh. " +
                "A rename takes until the next reconnect to reach them.",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
        )
        Spacer(Modifier.height(8.dp))
        KeyField(name, singleLine = true) {
            name = it.trim()
            error = ""
            renamed = false
        }
        Spacer(Modifier.height(8.dp))
        Text(
            if (renamed) "renamed" else "rename",
            style = MaterialTheme.typography.bodySmall,
            // Dead until the field actually differs, because a live-looking
            // link that writes the name it already has reads as a rename that
            // silently did nothing.
            color = if (changed) Palette.Phosphor else Palette.Line,
            modifier = Modifier.clickable(enabled = changed) {
                runCatching { Mobile.setDeviceName(dir, name) }
                    .onSuccess { saved = name; renamed = true }
                    .onFailure { error = it.message ?: "could not rename" }
            },
        )
        if (renamed) {
            Spacer(Modifier.height(4.dp))
            Text(
                "written to this device's config. Other devices go on calling it " +
                    "by the old name until this one reconnects: the name travels " +
                    "in the announce, and nothing announces until then.",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Amber,
            )
        }
        if (error.isNotEmpty()) {
            Spacer(Modifier.height(4.dp))
            Text(error, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
        }
    }
}

/** How much of the network this device carries.
 *
 * It states the measured numbers rather than the names, because "Core" and
 * "Edge" tell nobody anything and this is the setting that costs money on a
 * phone. Core is still the default: someone has to relay, and quietly opting a
 * user out of contributing is as wrong as quietly spending their data.
 *
 * Changing it while connected would mean tearing the tunnel down to alter a
 * preference, so it applies on the next connect and says so.
 */
@Composable
private fun ModeSetting(dir: String, connected: Boolean) {
    var mode by remember { mutableStateOf(runCatching { Mobile.mode(dir) }.getOrDefault("")) }
    var pending by remember { mutableStateOf(false) }
    if (mode.isEmpty()) return

    val edge = mode == "Edge"
    Column(Modifier.padding(horizontal = 24.dp)) {
        Label("THIS NODE")
        Spacer(Modifier.height(8.dp))
        ToggleRow(
            // "Edge", not "Light": it is the word the config takes, the word
            // the daemon logs, and the word every document uses. A phone
            // showing a fourth name for the same thing is how somebody ends up
            // searching for a setting that does not exist.
            title = if (edge) "Edge node" else "Core node (relay)",
            detail = if (edge) "subscribes only  ~3 MB/h" else "relays for the network  ~20 MB/h",
            detailColour = if (edge) Palette.Ash else Palette.Amber,
            checked = edge,
            onChange = { wantEdge ->
                val next = if (wantEdge) "Edge" else "Core"
                runCatching { Mobile.setMode(dir, next) }
                    .onSuccess {
                        mode = next
                        pending = connected
                    }
            },
        )
        if (pending) {
            Text(
                "applies on the next connect",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash,
            )
        }
    }
}

/** A named setting and its switch, so every row on this screen lines up. */
@Composable
private fun ToggleRow(
    title: String,
    detail: String,
    detailColour: Color = Palette.Ash,
    checked: Boolean,
    onChange: (Boolean) -> Unit,
) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(title, style = MaterialTheme.typography.bodyMedium, color = Palette.Bone)
            Text(detail, style = MaterialTheme.typography.labelSmall, color = detailColour)
        }
        Switch(
            checked = checked,
            onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = Palette.Bone,
                checkedTrackColor = Palette.Violet,
                uncheckedThumbColor = Palette.Ash,
                uncheckedTrackColor = Palette.Line,
            ),
        )
    }
}
