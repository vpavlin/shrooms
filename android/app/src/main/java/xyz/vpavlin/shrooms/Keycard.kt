package xyz.vpavlin.shrooms

import android.app.Activity
import android.nfc.NfcAdapter
import android.nfc.Tag
import android.nfc.tech.IsoDep
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import mobile.Mobile
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/**
 * The card as the mesh authority (ADR-022, docs/keycard-on-mobile.md).
 *
 * Everything about the Keycard protocol — pairing, the secure channel, the PIN,
 * signing — is in Go, shared with the rest of the project. This file supplies
 * the one thing Android has and Go does not: a way to move bytes to a card.
 */

/**
 * A tag in the field, wired to Go's CardTransport.
 *
 * `transceive` is exactly the shape Go asks for, which is why there is no
 * protocol code here. It throws on a lost tag, and gomobile turns a thrown
 * exception into the error half of Go's return — so a card pulled away
 * mid-signature surfaces as a Go error rather than as a hang.
 */
private class IsoTransport(private val iso: IsoDep) : mobile.CardTransport {
    override fun transmit(apdu: ByteArray): ByteArray = iso.transceive(apdu)
}

/**
 * Run [block] against a card the next time one is held to the phone.
 *
 * Reader mode rather than the intent-based dispatch, because it only runs while
 * this screen is in front and it does not pop the "new tag" chooser over the
 * app. Android only reads NFC in the foreground, so this cannot be moved into
 * the service: the tap has to happen with the screen open, which is why the
 * flow is a deliberate ceremony rather than something that can be answered in
 * the background.
 *
 * The reader callback arrives on a binder thread, which is what we want — the
 * Go call is blocking and a card conversation is many round trips.
 */
private suspend fun <T> onCardTap(activity: Activity, block: (mobile.CardTransport) -> T): T =
    suspendCancellableCoroutine { cont ->
        val adapter = NfcAdapter.getDefaultAdapter(activity)
        if (adapter == null) {
            cont.resumeWithException(IllegalStateException("this phone has no NFC"))
            return@suspendCancellableCoroutine
        }
        if (!adapter.isEnabled) {
            cont.resumeWithException(IllegalStateException("NFC is switched off"))
            return@suspendCancellableCoroutine
        }

        val reader = NfcAdapter.ReaderCallback { tag: Tag ->
            if (!cont.isActive) return@ReaderCallback
            val iso = IsoDep.get(tag)
            if (iso == null) {
                cont.resumeWithException(IllegalStateException("that is not a Keycard"))
                return@ReaderCallback
            }
            try {
                // A card conversation is many exchanges and some of them make
                // the card think. The default timeout is short enough to lose
                // a pairing half way, which costs a slot.
                iso.timeout = 15_000
                iso.connect()
                cont.resume(block(IsoTransport(iso)))
            } catch (e: Throwable) {
                if (cont.isActive) cont.resumeWithException(e)
            } finally {
                runCatching { iso.close() }
                runCatching { adapter.disableReaderMode(activity) }
            }
        }

        adapter.enableReaderMode(
            activity,
            reader,
            NfcAdapter.FLAG_READER_NFC_A or
                NfcAdapter.FLAG_READER_NFC_B or
                // Without this Android tries to read an NDEF message first,
                // which a Keycard does not have, and the tag is gone by the
                // time we get it.
                NfcAdapter.FLAG_READER_SKIP_NDEF_CHECK,
            null,
        )
        cont.invokeOnCancellation { runCatching { adapter.disableReaderMode(activity) } }
    }

/**
 * The Keycard section of the settings screen.
 *
 * Two operations for now, which are the two that have to work before anything
 * else can: enrol this phone with a card, and read back the authority key it
 * holds. Issuing credentials builds on both.
 */
@Composable
fun KeycardSetting(dir: String) {
    val activity = LocalContext.current as? Activity ?: return
    val scope = rememberCoroutineScope()

    var pairing by remember { mutableStateOf("") }
    var pin by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var key by remember { mutableStateOf("") }
    var waiting by remember { mutableStateOf(false) }
    var failed by remember { mutableStateOf(false) }

    fun run(what: String, block: (mobile.CardTransport) -> String) {
        waiting = true
        failed = false
        status = "hold your card to the back of the phone"
        scope.launch {
            val result = runCatching {
                // Off the main thread: this blocks for as long as the card
                // takes, and on the main thread that is an ANR.
                withContext(Dispatchers.IO) { onCardTap(activity) { block(it) } }
            }
            waiting = false
            result
                .onSuccess {
                    key = it
                    failed = false
                    status = "$what ok"
                }
                .onFailure {
                    failed = true
                    status = it.message ?: "$what failed"
                }
        }
    }

    Column(Modifier.padding(horizontal = 24.dp)) {
        Label("KEYCARD")
        Spacer(Modifier.height(6.dp))
        Text(
            "hold the mesh's admin key on a card instead of on a device. " +
                "Pair once; after that every signature is a tap.",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
        )

        Spacer(Modifier.height(10.dp))
        Text(
            "PAIRING PASSWORD",
            style = MaterialTheme.typography.labelSmall,
            color = Palette.Ash,
        )
        Spacer(Modifier.height(4.dp))
        KeyField(pairing, singleLine = true) { pairing = it.trim() }

        Spacer(Modifier.height(10.dp))
        Row {
            Action(
                text = if (waiting) "waiting…" else "Pair this phone",
                enabled = !waiting && pairing.isNotEmpty(),
            ) {
                run("pairing") { Mobile.cardEnrol(it, dir, pairing) }
            }
            Spacer(Modifier.width(12.dp))
            Action(text = "Read key", enabled = !waiting) {
                run("read") { Mobile.cardPublicKey(it, dir) }
            }
        }

        Spacer(Modifier.height(14.dp))
        Text(
            "PIN",
            style = MaterialTheme.typography.labelSmall,
            color = Palette.Ash,
        )
        Spacer(Modifier.height(4.dp))
        KeyField(pin, singleLine = true) { pin = it.trim() }
        Spacer(Modifier.height(10.dp))
        // Signs a digest and checks it against the card's own key, using the
        // same check a peer runs on a credential. A card that returns something
        // plausible and unverifiable is exactly what this is for, and it cannot
        // be found without a card in the field.
        Action(
            text = if (waiting) "waiting…" else "Test a signature",
            enabled = !waiting && pin.isNotEmpty(),
        ) {
            run("signature") { Mobile.cardSelfTest(it, dir, pin) }
        }

        if (status.isNotEmpty()) {
            Spacer(Modifier.height(8.dp))
            Text(
                status,
                style = MaterialTheme.typography.bodySmall,
                color = if (failed) Palette.Amber else Palette.Phosphor,
            )
        }
        if (key.isNotEmpty()) {
            Spacer(Modifier.height(8.dp))
            Text(
                "authority key",
                style = MaterialTheme.typography.labelSmall,
                color = Palette.Ash,
            )
            // Shown in full: this is the public half, it goes in admin_keys,
            // and a truncated key is one somebody has to ask for twice.
            Text(
                key,
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Bone,
            )
        }
    }
}
