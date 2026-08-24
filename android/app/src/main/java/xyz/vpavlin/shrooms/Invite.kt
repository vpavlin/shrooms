package xyz.vpavlin.shrooms

import android.app.Activity
import android.graphics.Bitmap
import android.graphics.Color
import androidx.compose.foundation.Image
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.google.zxing.BarcodeFormat
import com.google.zxing.EncodeHintType
import com.google.zxing.qrcode.QRCodeWriter
import com.google.zxing.qrcode.decoder.ErrorCorrectionLevel
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONObject

/**
 * Admitting a device from the phone (docs/keycard-on-mobile.md).
 *
 * The screen is the flow: a token becomes a QR, somebody scans it, what they
 * asked for appears here, and a tap of the card signs their credential. Three
 * states, because there are three Go calls and each one is a thing the person
 * holding the phone has to wait for or act on.
 *
 * It has to be a foreground screen. Android reads NFC only while an activity is
 * in front of it, so the card cannot be tapped in the background whatever this
 * looked like — which makes the constraint an honest part of the design rather
 * than something to apologise for.
 */

/** Where the flow has got to. */
private enum class Stage {
    /** Nothing minted yet. */
    Idle,

    /** A token exists and is on screen; nobody has redeemed it. */
    Showing,

    /** Somebody redeemed it, and their request is waiting for a signature. */
    Asked,

    /** Signed and published. */
    Done,
}

/**
 * Renders a token as a QR.
 *
 * zxing is already here for scanning and its core does both directions, so
 * this costs no dependency. Error correction stays low: an invite URI with a
 * bootstrap multiaddr is around 140 characters, and a denser code is harder for
 * the other phone's camera than a slightly less redundant one.
 */
private fun qrBitmap(text: String, size: Int = 640): Bitmap? = runCatching {
    val hints = mapOf(
        EncodeHintType.ERROR_CORRECTION to ErrorCorrectionLevel.L,
        EncodeHintType.MARGIN to 1,
    )
    val matrix = QRCodeWriter().encode(text, BarcodeFormat.QR_CODE, size, size, hints)
    val bmp = Bitmap.createBitmap(matrix.width, matrix.height, Bitmap.Config.RGB_565)
    for (x in 0 until matrix.width) {
        for (y in 0 until matrix.height) {
            // Drawn light-on-dark to match everything else here, which also
            // scans: cameras look for contrast, not for which side is which.
            bmp.setPixel(x, y, if (matrix[x, y]) Color.BLACK else Color.WHITE)
        }
    }
    bmp
}.getOrNull()

@Composable
fun InviteScreen(dir: String, meshLabel: String, onClose: () -> Unit) {
    val activity = LocalContext.current as? Activity ?: return
    val scope = rememberCoroutineScope()

    var stage by remember { mutableStateOf(Stage.Idle) }
    var token by remember { mutableStateOf("") }
    var uri by remember { mutableStateOf("") }
    var requestJSON by remember { mutableStateOf("") }
    var joiner by remember { mutableStateOf("") }
    var pin by remember { mutableStateOf("") }
    var status by remember { mutableStateOf("") }
    var detail by remember { mutableStateOf("") }
    var failed by remember { mutableStateOf(false) }
    var busy by remember { mutableStateOf(false) }

    // Which mesh this invite admits to.
    //
    // A phone on one mesh has nothing to choose and is not asked. A phone on
    // several used to admit to whichever the config listed first, silently —
    // the invite was minted, scanned, and the device landed somewhere the
    // person inviting had not picked.
    //
    // Only meshes that are RUNNING: holding an invite means subscribing to the
    // token's topic on that mesh's rendezvous plane, which a switched-off mesh
    // is not doing. Offering one would produce an invite nobody could answer.
    val meshes = remember {
        runCatching {
            val arr = org.json.JSONArray(Mobile.meshesJSON(dir))
            (0 until arr.length())
                .map { arr.getJSONObject(it) }
                .filter { !it.optBoolean("disabled", false) }
                .map { it.optString("label", "") }
                .filter { it.isNotEmpty() }
        }.getOrDefault(emptyList())
    }
    var chosen by remember { mutableStateOf(meshLabel.ifEmpty { meshes.firstOrNull().orEmpty() }) }

    val qr = remember(uri) { if (uri.isEmpty()) null else qrBitmap(uri) }

    // Whether this phone has a card to sign with, asked before an invite is
    // minted rather than at the tap.
    //
    // Without one the flow used to run its whole length — mint a token, show a
    // QR, have somebody scan it, take their request — and fail at the last
    // step, by which point the other person has to be asked to do it again.
    val ready = remember {
        runCatching { JSONObject(Mobile.cardEnrolment(dir)).optBoolean("paired", false) }
            .getOrDefault(false)
    }

    /**
     * Mint a token and start waiting in the same step.
     *
     * Separate calls in Go, because minting is instant and waiting is not, but
     * there is no state between them worth showing: an invite nobody is
     * listening for is not a thing anyone wants.
     */
    fun open() {
        failed = false
        status = ""
        scope.launch {
            val minted = runCatching { withContext(Dispatchers.IO) { Mobile.mintInvite() } }
            minted.onFailure {
                failed = true
                status = it.message ?: "could not create an invite"
                return@launch
            }
            token = minted.getOrThrow()
            uri = runCatching { Mobile.inviteURI(token, dir) }.getOrDefault("")
            if (uri.isEmpty()) {
                // Without a bootstrap address the token still works; it just
                // leans on the public fleet (ADR-031). Falling back rather than
                // failing, because the fleet is usually fine.
                uri = "shrooms://enrol?token=$token"
            }
            stage = Stage.Showing
            status = "waiting for a device to scan this"

            // Fifteen minutes, held by the daemon that is already connected.
            // The screen has to stay in front for the card, so this is not a
            // background task pretending to be one.
            //
            // Closing the screen does not cancel it. The call is blocking on an
            // IO thread and coroutine cancellation does not reach across the
            // gomobile boundary, so the hold runs to its own timeout in Go and
            // the thread comes back then. Harmless — a held invite costs a
            // subscription on a topic only the token names — but it does mean
            // an invite stays live for its full fifteen minutes even after the
            // person who made it walks away from the screen.
            val held = runCatching {
                withContext(Dispatchers.IO) { Mobile.awaitInvite(token, 15 * 60L, chosen) }
            }
            held
                .onSuccess {
                    requestJSON = it
                    joiner = runCatching { JSONObject(it).optString("name") }.getOrDefault("")
                    if (joiner.isEmpty()) joiner = "a device"
                    stage = Stage.Asked
                    status = "$joiner is asking to join"
                }
                .onFailure {
                    failed = true
                    stage = Stage.Idle
                    status = it.message ?: "nobody used the invite"
                }
        }
    }

    /** The tap. Everything before this was arranging for something to sign. */
    fun admit(withPin: String) {
        busy = true
        failed = false
        status = ""
        detail = "hold your card flat against the back of the phone"
        scope.launch {
            val result = runCatching {
                withContext(Dispatchers.IO) {
                    onCardTap(
                        activity,
                        onDetected = { detail = "signing — hold the card still" },
                    ) {
                        Mobile.admitWithCard(it, dir, withPin, token, requestJSON, joiner, 0L, chosen)
                    }
                }
            }
            busy = false
            // Cleared either way: on success it is spent, and on failure the
            // usual cause is a mistyped one, which is retyped rather than
            // corrected.
            pin = ""
            result
                .onSuccess {
                    stage = Stage.Done
                    status = it
                }
                .onFailure {
                    failed = true
                    // Deliberately stays on Asked. The invite is still open and
                    // the usual cause is a mistyped PIN or a card that moved,
                    // both of which are fixed by tapping again — throwing the
                    // request away here would mean the other person rescanning.
                    status = it.message ?: "the card did not sign"
                }
        }
    }

    Column(
        Modifier.fillMaxSize().padding(vertical = 32.dp).verticalScroll(rememberScrollState()),
    ) {
        Column(Modifier.padding(horizontal = 24.dp)) {
            Label("INVITE A DEVICE")
            Spacer(Modifier.height(6.dp))
            Text(
                "admit a device with the admin key on your card. An invite is " +
                    "good for one device and fifteen minutes.",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Ash,
            )

            // Shown only when there is a choice to make. One mesh is the
            // ordinary case and an extra control there is noise.
            if (meshes.size > 1) {
                Spacer(Modifier.height(18.dp))
                Label("ADMIT TO")
                Spacer(Modifier.height(6.dp))
                meshes.forEach { label ->
                    val picked = label == chosen
                    Text(
                        (if (picked) "› " else "  ") + label,
                        style = MaterialTheme.typography.bodyMedium,
                        color = if (picked) Palette.Phosphor else Palette.Ash,
                        modifier = Modifier
                            .fillMaxWidth()
                            .padding(vertical = 6.dp)
                            // Locked once an invite is live: the token is held
                            // on one mesh's topic, so changing it here would
                            // show a QR code for a mesh nobody is listening on.
                            .clickable(enabled = stage == Stage.Idle) { chosen = label },
                    )
                }
            }

            if (!ready) {
                Spacer(Modifier.height(18.dp))
                Text(
                    "This phone is not set up with a card, so it cannot sign a " +
                        "credential. Settings → Keycard → Set up a card.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Amber,
                )
            }

            if (stage == Stage.Showing && qr != null) {
                Spacer(Modifier.height(18.dp))
                Image(
                    bitmap = qr.asImageBitmap(),
                    contentDescription = "the invite, as a QR code",
                    modifier = Modifier.fillMaxWidth().size(280.dp).align(Alignment.CenterHorizontally),
                )
                Spacer(Modifier.height(10.dp))
                Text(
                    uri,
                    style = MaterialTheme.typography.labelSmall,
                    color = Palette.Line,
                )
            }

            if (stage == Stage.Asked) {
                Spacer(Modifier.height(18.dp))
                Text(
                    "$joiner wants to join",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Phosphor,
                )
                if (busy) {
                    Waiting("Signing their credential", detail)
                } else {
                    Spacer(Modifier.height(10.dp))
                    Text(
                        "Enter the card's PIN to admit them.",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Bone,
                    )
                    Spacer(Modifier.height(4.dp))
                    Text(
                        "Six digits. You will be asked for the card as soon as " +
                            "the last one is in.",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Ash,
                    )
                    // The last digit signs. There is no separate approve
                    // button, because there was nothing left to decide by the
                    // time somebody had typed six digits into this screen.
                    PinPad(pin, onChange = { pin = it }, onComplete = { admit(it) })
                }
            }

            Spacer(Modifier.height(14.dp))
            Row {
                when (stage) {
                    Stage.Idle -> Action(text = "Create an invite", enabled = ready) { open() }
                    Stage.Showing -> Action(text = "waiting…", enabled = false) {}
                    // No action of its own: the PIN pad above is the action,
                    // and its last digit is the tap prompt.
                    Stage.Asked -> Unit
                    Stage.Done -> Action(text = "Invite another", enabled = true) {
                        stage = Stage.Idle
                        token = ""; uri = ""; requestJSON = ""; joiner = ""; status = ""
                    }
                }
                Spacer(Modifier.width(12.dp))
                Action(text = "Close", enabled = !busy) { onClose() }
            }

            if (status.isNotEmpty()) {
                Spacer(Modifier.height(12.dp))
                Text(
                    status,
                    style = MaterialTheme.typography.bodySmall,
                    color = if (failed) Palette.Rust else Palette.Ash,
                )
            }

            Spacer(Modifier.height(18.dp))
            Text(
                "keep this screen in front while you wait. Android reads a card " +
                    "only for the app on screen, so the tap has to happen here.",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Line,
            )
        }
    }
}
