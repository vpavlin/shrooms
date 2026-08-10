package xyz.vpavlin.shrooms

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

/**
 * Dark only, and unapologetically so — a terminal palette rather than a
 * fashionable one.
 *
 * Colour carries meaning here and is not decoration: Phosphor means traffic is
 * actually flowing, Amber means working on it, Rust means broken. Nothing else
 * is coloured, so the one coloured thing on screen is the answer.
 */
object Palette {
    val Void = Color(0xFF07090B)      // background
    val Panel = Color(0xFF0E1216)     // raised surfaces
    val Line = Color(0xFF1C2229)      // hairline dividers
    val Ash = Color(0xFF6B7680)       // secondary text
    val Bone = Color(0xFFD6DDE3)      // primary text

    val Phosphor = Color(0xFF35F0A0)  // connected, carrying traffic
    val Amber = Color(0xFFF0B429)     // reaching, not there yet
    val Rust = Color(0xFFE05252)      // failed
    val Violet = Color(0xFF9A7BFF)    // relayed — distinct, not a warning
}

/**
 * Monospace throughout. Addresses and keys are read character by character, and
 * a proportional font makes `fd3b` and `fd8b` look alike at a glance.
 */
private val Mono = FontFamily.Monospace

private val typography = Typography(
    displaySmall = TextStyle(fontFamily = Mono, fontSize = 28.sp, fontWeight = FontWeight.Light, letterSpacing = (-0.5).sp),
    titleMedium = TextStyle(fontFamily = Mono, fontSize = 15.sp, fontWeight = FontWeight.Medium),
    bodyMedium = TextStyle(fontFamily = Mono, fontSize = 13.sp),
    bodySmall = TextStyle(fontFamily = Mono, fontSize = 11.sp, letterSpacing = 0.4.sp),
    labelSmall = TextStyle(fontFamily = Mono, fontSize = 10.sp, letterSpacing = 1.2.sp, fontWeight = FontWeight.Medium),
)

@Composable
fun LogosTheme(content: @Composable () -> Unit) {
    @Suppress("UNUSED_EXPRESSION") isSystemInDarkTheme() // dark regardless; the system preference is noted and ignored
    MaterialTheme(
        colorScheme = darkColorScheme(
            primary = Palette.Phosphor,
            onPrimary = Palette.Void,
            background = Palette.Void,
            onBackground = Palette.Bone,
            surface = Palette.Panel,
            onSurface = Palette.Bone,
            surfaceVariant = Palette.Panel,
            onSurfaceVariant = Palette.Ash,
            error = Palette.Rust,
            outline = Palette.Line,
        ),
        typography = typography,
        content = content,
    )
}
