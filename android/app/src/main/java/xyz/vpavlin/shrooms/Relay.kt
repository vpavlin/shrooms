package xyz.vpavlin.shrooms

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import mobile.Mobile

/**
 * Relays run by people who are not on this mesh (docs/blind-relays.md).
 *
 * The setting a phone needs most and can least work out for itself. A phone on
 * mobile data sits behind carrier-grade NAT: it cannot be dialled at all,
 * because the carrier rewrites the port per destination, so no amount of hole
 * punching reaches it. A relay is the only way its traffic moves — and a blind
 * relay cannot announce itself, having no network key and no delivery node, so
 * somebody has to type it in.
 *
 * What it cannot do is read anything. The traffic is WireGuard, encrypted
 * between two devices whose keys the relay does not hold.
 */
@Composable
fun BlindRelaySetting(dir: String) {
    var list by remember { mutableStateOf(runCatching { Mobile.blindRelays(dir) }.getOrDefault("")) }
    var token by remember { mutableStateOf(runCatching { Mobile.blindRelayToken(dir) }.getOrDefault("")) }
    var status by remember { mutableStateOf("") }
    var failed by remember { mutableStateOf(false) }

    Column(Modifier.padding(horizontal = 24.dp)) {
        Label("RELAYS")
        Spacer(Modifier.height(6.dp))
        Text(
            "when this phone cannot be reached directly — which on mobile data " +
                "is always — its traffic goes through a relay. A relay cannot " +
                "read any of it.",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
        )

        Spacer(Modifier.height(10.dp))
        Text(
            "ADDRESSES, COMMA SEPARATED",
            style = MaterialTheme.typography.labelSmall,
            color = Palette.Ash,
        )
        Spacer(Modifier.height(4.dp))
        KeyField(list, singleLine = true) { list = it }

        Spacer(Modifier.height(10.dp))
        Text(
            "TOKEN, IF THE OPERATOR ASKS FOR ONE",
            style = MaterialTheme.typography.labelSmall,
            color = Palette.Ash,
        )
        Spacer(Modifier.height(4.dp))
        KeyField(token, singleLine = true) { token = it.trim() }

        Spacer(Modifier.height(10.dp))
        Row {
            Action(text = "Save", enabled = true) {
                runCatching { Mobile.setBlindRelays(dir, list, token) }
                    .onSuccess {
                        failed = false
                        // Said explicitly, because the tunnel is built from the
                        // config at connect time and an unchanged screen after
                        // saving looks exactly like a setting that did nothing.
                        status = if (list.isBlank()) {
                            "cleared — reconnect to apply"
                        } else {
                            "saved — reconnect to apply"
                        }
                    }
                    .onFailure {
                        failed = true
                        status = it.message ?: "could not save"
                    }
            }
        }

        if (status.isNotEmpty()) {
            Spacer(Modifier.height(10.dp))
            Text(
                status,
                style = MaterialTheme.typography.bodySmall,
                color = if (failed) Palette.Rust else Palette.Ash,
            )
        }

        Spacer(Modifier.height(12.dp))
        Text(
            "a relay has to be told to this phone: it is run by somebody who is " +
                "not on your mesh, so it has no way to announce itself. This " +
                "phone will use at most two of them.",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Line,
        )
    }
}
