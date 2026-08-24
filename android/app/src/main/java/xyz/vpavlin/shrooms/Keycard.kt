package xyz.vpavlin.shrooms

import android.app.Activity
import android.content.Intent
import android.provider.Settings as AndroidSettings
import android.util.Log
import android.nfc.NfcAdapter
import android.nfc.Tag
import android.nfc.tech.IsoDep
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ColumnScope
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withContext
import mobile.Mobile
import org.json.JSONObject
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
internal suspend fun <T> onCardTap(
    activity: Activity,
    onDetected: () -> Unit = {},
    block: (mobile.CardTransport) -> T,
): T =
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
                // The card is answering. Say so before the work starts: what
                // follows is a secure channel and several round trips, seconds
                // of it, and a screen still asking for a card that is already
                // on the glass reads as nothing having happened — which is how
                // somebody comes to move it away half way through.
                activity.runOnUiThread(onDetected)
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
 * What a card said about itself.
 *
 * The Go side answers CardStatus with this rather than a sentence, because the
 * screen has decisions to make from it: whether to ask for a pairing password
 * at all, whether there is any point asking for a PIN, whether freeing the
 * other slots would help. Summary stays a sentence — the four ways a card stops
 * you are things nobody should have to decode from a field.
 */
private data class CardReport(
    val applet: String,
    val hasKey: Boolean,
    val keyUID: String,
    val freeSlots: Int,
    val maxSlots: Int,
    val needsPassword: Boolean,
    val problem: String,
    val summary: String,
) {
    /** An empty problem means this card can be enrolled. */
    val usable: Boolean get() = problem.isEmpty()
}

private fun cardReportOf(json: String): CardReport = JSONObject(json).let {
    CardReport(
        applet = it.optString("applet", "unknown"),
        hasKey = it.optBoolean("hasKey", false),
        keyUID = it.optString("keyUID", ""),
        freeSlots = it.optInt("freeSlots", -1),
        maxSlots = it.optInt("maxSlots", 5),
        needsPassword = it.optBoolean("needsPassword", true),
        problem = it.optString("problem", ""),
        summary = it.optString("summary", ""),
    )
}

/**
 * Why something failed, in a form worth showing.
 *
 * Several of the ways a card conversation ends carry no message at all — a card
 * moved away mid-exchange throws TagLostException with a null one — and "failed"
 * on its own tells somebody holding a card nothing they can act on.
 */
private fun why(t: Throwable): String =
    t.message?.takeIf { it.isNotBlank() } ?: t::class.simpleName ?: "unknown error"

/** What this phone can do about NFC at all. */
private enum class Nfc { Missing, Off, Ready }

/**
 * Asked before offering anything, rather than discovered by failing.
 *
 * Both of these used to surface as an exception thrown from the middle of a
 * tap — which is to say, after somebody had opened a ceremony, typed a PIN and
 * held a card against a phone that was never going to read it.
 */
private fun nfcState(ctx: android.content.Context): Nfc {
    val adapter = NfcAdapter.getDefaultAdapter(ctx) ?: return Nfc.Missing
    return if (adapter.isEnabled) Nfc.Ready else Nfc.Off
}

/** A Keycard PIN is exactly six digits. */
private const val PinLength = 6

/**
 * The PIN, asked for as what it is.
 *
 * It used to be a text field, which on a phone raises the alphabetic keyboard
 * for a value that is six digits, leaves no indication of how many are wanted,
 * and needs a second deliberate tap on a button somewhere else to act on it.
 * Six dots and a keypad say the length without a word, and the last digit is
 * the action — which is the flow Vaclav asked for: enter the PIN, then be told
 * to present the card, rather than hunting for what to press next.
 *
 * No PIN is kept between operations. A cached PIN would make repeated signing
 * one tap instead of two, and it would also mean that anybody holding an
 * unlocked phone and the card can sign without knowing the PIN at all — which
 * is most of what the PIN is for. See docs/keycard-on-mobile.md.
 */
@Composable
internal fun PinPad(
    pin: String,
    onChange: (String) -> Unit,
    onComplete: (String) -> Unit,
    enabled: Boolean = true,
) {
    Column(Modifier.fillMaxWidth()) {
        Row(
            Modifier.fillMaxWidth().padding(vertical = 12.dp),
            horizontalArrangement = Arrangement.Center,
        ) {
            repeat(PinLength) { i ->
                val filled = i < pin.length
                Box(
                    Modifier
                        .padding(horizontal = 7.dp)
                        .size(11.dp)
                        .background(
                            if (filled) Palette.Phosphor else Color.Transparent,
                            CircleShape,
                        )
                        .border(
                            1.dp,
                            if (filled) Palette.Phosphor else Palette.Line,
                            CircleShape,
                        ),
                )
            }
        }
        listOf(
            listOf("1", "2", "3"),
            listOf("4", "5", "6"),
            listOf("7", "8", "9"),
            listOf("", "0", "⌫"),
        ).forEach { row ->
            Row(Modifier.fillMaxWidth()) {
                row.forEach { k ->
                    when (k) {
                        "" -> Spacer(Modifier.weight(1f))
                        "⌫" -> PinKey(k, enabled && pin.isNotEmpty()) {
                            onChange(pin.dropLast(1))
                        }
                        else -> PinKey(k, enabled && pin.length < PinLength) {
                            val next = pin + k
                            onChange(next)
                            // The last digit is the action. Nothing else to
                            // press, and nothing to go looking for.
                            if (next.length == PinLength) onComplete(next)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun RowScope.PinKey(label: String, enabled: Boolean, onClick: () -> Unit) {
    Box(
        Modifier
            .weight(1f)
            .padding(4.dp)
            .height(54.dp)
            .background(Palette.Panel, RoundedCornerShape(10.dp))
            .border(1.dp, Palette.Line, RoundedCornerShape(10.dp))
            .clickable(enabled = enabled) { onClick() },
        contentAlignment = Alignment.Center,
    ) {
        Text(
            label,
            style = MaterialTheme.typography.bodyMedium,
            color = if (enabled) Palette.Bone else Palette.Line,
        )
    }
}

/**
 * Something is happening and the card is involved.
 *
 * The spinner is the point. Everything here takes seconds — a secure channel is
 * several round trips and the card thinks between them — and a screen that says
 * "hold your card" and then goes on saying it looks exactly like a screen where
 * nothing happened, which is how somebody comes to take the card away half way
 * through and burn a pairing slot.
 */
@Composable
internal fun Waiting(title: String, detail: String) {
    Column(
        Modifier.fillMaxWidth().padding(vertical = 22.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        CircularProgressIndicator(
            color = Palette.Phosphor,
            strokeWidth = 2.dp,
            modifier = Modifier.size(30.dp),
        )
        Spacer(Modifier.height(16.dp))
        Text(title, style = MaterialTheme.typography.bodyMedium, color = Palette.Bone)
        Spacer(Modifier.height(6.dp))
        Text(
            detail,
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
            textAlign = TextAlign.Center,
        )
    }
}

/** A failure, in full and selectable, because the useful ones are long. */
@Composable
private fun Problem(text: String) {
    SelectionContainer {
        Text(text, style = MaterialTheme.typography.bodySmall, color = Palette.Rust)
    }
}

/** The modal shell every card ceremony runs in. */
@Composable
private fun CardDialog(
    title: String,
    onDismiss: () -> Unit,
    content: @Composable ColumnScope.() -> Unit,
) {
    Dialog(onDismissRequest = onDismiss) {
        Column(
            Modifier
                .fillMaxWidth()
                .background(Palette.Void, RoundedCornerShape(14.dp))
                .border(1.dp, Palette.Line, RoundedCornerShape(14.dp))
                .padding(20.dp)
                .verticalScroll(rememberScrollState()),
        ) {
            Label(title)
            Spacer(Modifier.height(14.dp))
            content()
        }
    }
}

/** Where setting up a card has got to. */
private enum class Setup { Intro, Looking, Blocked, Ready, Pin, Working, Done }

/**
 * Setting this phone up with a card, as one guided sequence.
 *
 * The order matters and it is not the order the buttons used to be in. It looks
 * at the card first, with SELECT, which costs no pairing slot, spends no PIN
 * attempt and needs no password — and answers three of the four ways this can
 * fail before anything has been spent. Vaclav's first evening with a real card
 * went: pairing refused (slots full), pairing refused (no secure channel),
 * key unreadable (no PIN verified). Every one of those was one free tap away
 * from being a sentence instead of a status word.
 *
 * Only then does it ask for anything, and only for what this particular card
 * needs: applet 4.0 and later pairs with a certificate, so a password field
 * there is a field for something that does not exist.
 */
@Composable
private fun CardSetupDialog(dir: String, onEnrolled: () -> Unit, onDismiss: () -> Unit) {
    val activity = LocalContext.current as? Activity ?: return
    val scope = rememberCoroutineScope()
    val clipboard = LocalClipboardManager.current

    var stage by remember { mutableStateOf(Setup.Intro) }
    var report by remember { mutableStateOf<CardReport?>(null) }
    var password by remember { mutableStateOf(Mobile.CardDefaultPairingPassword) }
    var pin by remember { mutableStateOf("") }
    var key by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var detail by remember { mutableStateOf("") }
    var working by remember { mutableStateOf("") }

    // Whether the pairing is already stored, which changes what the last step
    // does. CardEnrol writes the pairing the moment it succeeds and checks the
    // PIN afterwards, so a mistyped PIN leaves this phone paired — and pairing
    // again would spend a second of five slots to fix a typo. If the pairing is
    // there, the retry only needs the PIN.
    var paired by remember {
        mutableStateOf(
            runCatching { JSONObject(Mobile.cardEnrolment(dir)).optBoolean("paired", false) }
                .getOrDefault(false),
        )
    }

    fun tap(what: String, block: (mobile.CardTransport) -> String, then: (String) -> Unit) {
        error = ""
        detail = "hold your card flat against the back of the phone"
        scope.launch {
            val r = runCatching {
                withContext(Dispatchers.IO) {
                    onCardTap(activity, onDetected = { detail = "$what — hold the card still" }) {
                        block(it)
                    }
                }
            }
            r.onSuccess { then(it) }
                .onFailure {
                    error = why(it)
                    Log.w("shrooms", "keycard $what failed", it)
                    paired = runCatching {
                        JSONObject(Mobile.cardEnrolment(dir)).optBoolean("paired", false)
                    }.getOrDefault(paired)
                    stage = Setup.Blocked
                }
        }
    }

    fun identify() {
        stage = Setup.Looking
        report = null
        tap("reading the card", { Mobile.cardStatus(it) }) {
            val rep = runCatching { cardReportOf(it) }.getOrNull()
            if (rep == null) {
                error = "the card answered, and this app could not read the answer"
                stage = Setup.Blocked
                return@tap
            }
            report = rep
            if (!rep.needsPassword) password = ""
            stage = if (rep.usable) Setup.Ready else Setup.Blocked
        }
    }

    fun finish(withPin: String) {
        working = if (paired) "Reading the key" else "Pairing"
        stage = Setup.Working
        tap(
            if (paired) "reading the key" else "pairing",
            {
                // Already paired means this is a retry after a refused PIN.
                // Pairing again would take another slot for nothing.
                if (paired) Mobile.cardPublicKey(it, dir, withPin)
                else Mobile.cardEnrol(it, dir, password, withPin)
            },
        ) {
            key = it
            paired = true
            pin = ""
            stage = Setup.Done
            onEnrolled()
        }
    }

    CardDialog("SET UP A CARD", onDismiss) {
        when (stage) {
            Setup.Intro -> {
                Text(
                    "Your card holds the mesh's admin key, and this phone signs " +
                        "with it by tapping the card. Setting up happens once.",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Bone,
                )
                Spacer(Modifier.height(10.dp))
                Text(
                    "First a look at the card, which costs nothing: no pairing " +
                        "slot, no PIN attempt, no password. Nothing on the card " +
                        "changes and nothing is used up.",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                )
                Spacer(Modifier.height(18.dp))
                Action(text = "Look at the card", enabled = true) { identify() }
            }

            Setup.Looking -> Waiting("Looking at the card", detail)
            Setup.Working -> Waiting(working, detail)

            Setup.Blocked -> {
                val rep = report
                if (error.isNotEmpty()) Problem(error)
                if (rep != null && !rep.usable) {
                    if (error.isNotEmpty()) Spacer(Modifier.height(10.dp))
                    Problem(rep.summary)
                }
                Spacer(Modifier.height(16.dp))
                // A refused PIN after a successful pairing is the one failure
                // that must not restart the ceremony: the pairing is stored,
                // so going round again would spend a second of five slots to
                // fix a typo. Straight back to the pad.
                if (paired && report?.usable == true) {
                    Action(text = "Try the PIN again", enabled = true) {
                        pin = ""
                        stage = Setup.Pin
                    }
                    Spacer(Modifier.height(10.dp))
                }
                // Freeing the other slots needs this phone to hold one of them
                // already, because unpairing happens inside the channel a
                // pairing opens. Offered only when that is true — a full card
                // this phone has never paired with cannot be rescued from here,
                // and an action that always fails is worse than none.
                if (rep?.problem == "no-slots" && paired) {
                    Text(
                        "This phone is one of the five. It can free the other four.",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Ash,
                    )
                    Spacer(Modifier.height(10.dp))
                    Action(text = "Free the other slots", enabled = true) {
                        working = "Freeing the other slots"
                        stage = Setup.Working
                        tap("freeing the slots", { Mobile.cardUnpairOthers(it, dir) }) {
                            detail = it
                            identify()
                        }
                    }
                    Spacer(Modifier.height(10.dp))
                }
                Row {
                    Action(text = "Look again", enabled = true) { identify() }
                    Spacer(Modifier.width(12.dp))
                    Action(text = "Close", enabled = true) { onDismiss() }
                }
            }

            Setup.Ready -> {
                val rep = report
                Text(
                    "This card can hold a mesh's admin key.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Phosphor,
                )
                Spacer(Modifier.height(8.dp))
                Text(
                    rep?.summary.orEmpty(),
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                )
                if (rep?.needsPassword == true) {
                    Spacer(Modifier.height(18.dp))
                    Label("PAIRING PASSWORD")
                    Spacer(Modifier.height(6.dp))
                    KeyField(password, singleLine = true) { password = it.trim() }
                    Spacer(Modifier.height(6.dp))
                    Text(
                        "The factory default is filled in, which is what a card " +
                            "set up with the Keycard app has. Change it only if " +
                            "you chose your own.",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Ash,
                    )
                } else {
                    Spacer(Modifier.height(14.dp))
                    Text(
                        "Applet ${rep?.applet} pairs with a certificate, so there " +
                            "is no pairing password to type.",
                        style = MaterialTheme.typography.bodySmall,
                        color = Palette.Ash,
                    )
                }
                Spacer(Modifier.height(18.dp))
                Action(
                    text = "Continue",
                    enabled = rep?.needsPassword != true || password.isNotEmpty(),
                ) {
                    pin = ""
                    stage = Setup.Pin
                }
            }

            Setup.Pin -> {
                Text(
                    if (paired) "Enter the card's PIN." else "Enter the card's PIN to finish pairing.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Bone,
                )
                Spacer(Modifier.height(4.dp))
                Text(
                    "Six digits. You will be asked for the card as soon as the last one is in.",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                )
                PinPad(pin, onChange = { pin = it }, onComplete = { finish(it) })
                Spacer(Modifier.height(10.dp))
                Action(text = "Back", enabled = true) { stage = Setup.Ready }
            }

            Setup.Done -> {
                Text(
                    "This phone is set up.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Phosphor,
                )
                Spacer(Modifier.height(10.dp))
                Text(
                    "It can now admit devices to a mesh whose admin key is this " +
                        "card. Every signature is a tap and the PIN.",
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                )
                Spacer(Modifier.height(16.dp))
                AuthorityKey(key) { clipboard.setText(AnnotatedString(key)) }
                Spacer(Modifier.height(18.dp))
                Action(text = "Done", enabled = true) { onDismiss() }
            }
        }
    }
}

/**
 * The key the card signs with, shown in full.
 *
 * In full because it is the public half, it goes in a mesh's admin_keys, and a
 * truncated key is one somebody has to come back and ask for again.
 */
@Composable
private fun AuthorityKey(key: String, onCopy: () -> Unit) {
    var copied by remember(key) { mutableStateOf(false) }
    Column(
        Modifier
            .fillMaxWidth()
            .background(Palette.Panel, RoundedCornerShape(8.dp))
            .border(1.dp, Palette.Line, RoundedCornerShape(8.dp))
            .clickable {
                onCopy()
                copied = true
            }
            .padding(12.dp),
    ) {
        Text(
            if (copied) "AUTHORITY KEY — COPIED" else "AUTHORITY KEY — TAP TO COPY",
            style = MaterialTheme.typography.labelSmall,
            color = if (copied) Palette.Phosphor else Palette.Ash,
        )
        Spacer(Modifier.height(6.dp))
        SelectionContainer {
            Text(key, style = MaterialTheme.typography.bodySmall, color = Palette.Bone)
        }
    }
}

/** One card operation on an already-enrolled phone: PIN if needed, then a tap. */
private class CardOp(
    val title: String,
    val explain: String,
    val needsPin: Boolean,
    val run: (mobile.CardTransport, String) -> String,
)

@Composable
private fun CardOpDialog(op: CardOp, onDismiss: () -> Unit) {
    val activity = LocalContext.current as? Activity ?: return
    val scope = rememberCoroutineScope()
    var pin by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var done by remember { mutableStateOf("") }
    var error by remember { mutableStateOf("") }
    var detail by remember { mutableStateOf("") }

    fun go(withPin: String) {
        busy = true
        error = ""
        detail = "hold your card flat against the back of the phone"
        scope.launch {
            val r = runCatching {
                withContext(Dispatchers.IO) {
                    onCardTap(
                        activity,
                        onDetected = { detail = "${op.title.lowercase()} — hold the card still" },
                    ) { op.run(it, withPin) }
                }
            }
            busy = false
            pin = ""
            r.onSuccess { done = it }
                .onFailure {
                    error = why(it)
                    Log.w("shrooms", "keycard ${op.title} failed", it)
                }
        }
    }

    CardDialog(op.title.uppercase(), onDismiss) {
        when {
            busy -> Waiting(op.title, detail)
            done.isNotEmpty() -> {
                Text(
                    "Done.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Phosphor,
                )
                Spacer(Modifier.height(10.dp))
                SelectionContainer {
                    Text(done, style = MaterialTheme.typography.bodySmall, color = Palette.Bone)
                }
                Spacer(Modifier.height(18.dp))
                Action(text = "Close", enabled = true) { onDismiss() }
            }
            else -> {
                Text(
                    op.explain,
                    style = MaterialTheme.typography.bodySmall,
                    color = Palette.Ash,
                )
                if (error.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    Problem(error)
                }
                if (op.needsPin) {
                    Spacer(Modifier.height(10.dp))
                    PinPad(pin, onChange = { pin = it }, onComplete = { go(it) })
                    Spacer(Modifier.height(10.dp))
                    Action(text = "Cancel", enabled = true) { onDismiss() }
                } else {
                    Spacer(Modifier.height(18.dp))
                    Row {
                        Action(text = "Hold the card", enabled = true) { go("") }
                        Spacer(Modifier.width(12.dp))
                        Action(text = "Cancel", enabled = true) { onDismiss() }
                    }
                }
            }
        }
    }
}

/**
 * The Keycard section of the settings screen.
 *
 * It shows one thing: whether this phone is set up with a card, and what it
 * signs with if it is. Everything else is behind the action that applies.
 *
 * It used to be five buttons and two text fields, all of them always visible
 * and all of them always enabled — check, pair, forget other devices, read key,
 * test a signature. That is the protocol, laid out flat, and it left the person
 * holding a card to work out which step they were on and which button was for
 * it. Vaclav's verdict after an evening of it was "I have no clue what we are
 * doing and what the flow should be", which was a fair description of a screen
 * that never said.
 */
@Composable
fun KeycardSetting(dir: String) {
    val ctx = LocalContext.current
    // Re-read when the screen comes back to the front.
    //
    // Somebody sent to Android's NFC settings by the button below returns to
    // this panel, and returning does not recompose on its own — so a panel that
    // read the state once would still be saying "NFC is switched off" after
    // they had just switched it on, with a button offering to send them back
    // where they came from.
    val lifecycleOwner = LocalLifecycleOwner.current
    var nfc by remember { mutableStateOf(nfcState(ctx)) }
    DisposableEffect(lifecycleOwner) {
        val watch = LifecycleEventObserver { _, event ->
            if (event == Lifecycle.Event.ON_RESUME) nfc = nfcState(ctx)
        }
        lifecycleOwner.lifecycle.addObserver(watch)
        onDispose { lifecycleOwner.lifecycle.removeObserver(watch) }
    }
    var enrolled by remember { mutableStateOf(false) }
    var key by remember { mutableStateOf("") }
    var setup by remember { mutableStateOf(false) }
    var op by remember { mutableStateOf<CardOp?>(null) }
    var forgetting by remember { mutableStateOf(false) }

    fun refresh() {
        runCatching {
            val o = JSONObject(Mobile.cardEnrolment(dir))
            enrolled = o.optBoolean("paired", false)
            key = o.optString("key", "")
        }
    }
    LaunchedEffect(dir) { refresh() }

    Column(Modifier.padding(horizontal = 24.dp)) {
        Label("KEYCARD")
        Spacer(Modifier.height(6.dp))
        Text(
            "hold the mesh's admin key on a card instead of on a device. " +
                "Set up once; after that every signature is a tap and the PIN.",
            style = MaterialTheme.typography.bodySmall,
            color = Palette.Ash,
        )

        Spacer(Modifier.height(16.dp))
        when (nfc) {
            Nfc.Missing -> Text(
                "This phone has no NFC, so it cannot use a card. The card can " +
                    "still hold the admin key — another device has to do the tapping.",
                style = MaterialTheme.typography.bodyMedium,
                color = Palette.Amber,
            )
            Nfc.Off -> {
                Text(
                    "NFC is switched off, so a card cannot be read.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = Palette.Amber,
                )
                Spacer(Modifier.height(14.dp))
                Action(text = "Open NFC settings", enabled = true) {
                    runCatching {
                        ctx.startActivity(Intent(AndroidSettings.ACTION_NFC_SETTINGS))
                    }
                }
            }
            Nfc.Ready -> Unit
        }

        if (nfc == Nfc.Ready && !enrolled) {
            Text(
                "This phone is not set up with a card.",
                style = MaterialTheme.typography.bodyMedium,
                color = Palette.Ash,
            )
            Spacer(Modifier.height(14.dp))
            Action(text = "Set up a card", enabled = true) { setup = true }
        } else if (enrolled) {
            Text(
                "This phone signs with a card.",
                style = MaterialTheme.typography.bodyMedium,
                color = Palette.Phosphor,
            )
            if (key.isNotEmpty()) {
                Spacer(Modifier.height(12.dp))
                val clipboard = LocalClipboardManager.current
                AuthorityKey(key) { clipboard.setText(AnnotatedString(key)) }
            }
            Spacer(Modifier.height(16.dp))
            Action(text = "Check the card", enabled = nfc == Nfc.Ready) {
                op = CardOp(
                    title = "Check the card",
                    // Signs a digest and verifies it against the card's own
                    // key, with the same check a peer runs on a credential.
                    // A card that returns something plausible and unverifiable
                    // is exactly what this is for, and it took a real card in
                    // the field to find one.
                    explain = "Signs a test value and checks it against the card's " +
                        "own key — the same check another device runs on a " +
                        "credential this card signs.",
                    needsPin = true,
                ) { t, pin -> Mobile.cardSelfTest(t, dir, pin) }
            }
            Spacer(Modifier.height(10.dp))
            Action(text = "Free the other pairing slots", enabled = nfc == Nfc.Ready) {
                op = CardOp(
                    title = "Free the other slots",
                    explain = "A card has five pairing slots and every device that " +
                        "has ever paired with it holds one, including earlier " +
                        "attempts from this phone. This frees the four this phone " +
                        "is not using. Every other device paired with this card " +
                        "stops being able to use it, and that cannot be undone.",
                    needsPin = false,
                ) { t, _ -> Mobile.cardUnpairOthers(t, dir) }
            }
            Spacer(Modifier.height(10.dp))
            Action(text = "Forget this card", enabled = true, danger = true) { forgetting = true }
        }
    }

    if (setup) {
        CardSetupDialog(dir, onEnrolled = { refresh() }) {
            setup = false
            refresh()
        }
    }
    op?.let { CardOpDialog(it) { op = null } }

    if (forgetting) {
        CardDialog("FORGET THIS CARD", { forgetting = false }) {
            Text(
                "This phone stops being set up with the card. Nothing on the " +
                    "card changes: it still counts this phone among the five " +
                    "devices it is paired with, and setting up again would take " +
                    "another slot. Free the slot first if you are running out.",
                style = MaterialTheme.typography.bodySmall,
                color = Palette.Ash,
            )
            Spacer(Modifier.height(18.dp))
            Row {
                Action(text = "Forget it", enabled = true, danger = true) {
                    runCatching { Mobile.cardForget(dir) }
                    forgetting = false
                    refresh()
                }
                Spacer(Modifier.width(12.dp))
                Action(text = "Keep it", enabled = true) { forgetting = false }
            }
        }
    }
}
