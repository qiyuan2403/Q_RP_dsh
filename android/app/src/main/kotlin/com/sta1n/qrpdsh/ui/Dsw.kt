package com.sta1n.qrpdsh.ui

import androidx.compose.runtime.Immutable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.sp

/**
 * The DSH design tokens this client uses.
 *
 * Ported from the official client's `--dsw-*` CSS custom properties
 * (packages/client/ui-theme/src/styles/design-platform.css, 190 unique
 * tokens). The subset here is what the mobile surface actually draws: base
 * and layered surfaces, border ladder, brand, text ladder, and the diff
 * pair. Values are the light-theme ones; [DswDark] overrides the same names.
 *
 * WHY AN OBJECT AND NOT A MAP. A token table a compiler can check catches
 * a renamed token at build time; a string-keyed map fails at paint time.
 */
@Immutable
data class Dsw(
    val bgBase: Color,
    val bgLayer1: Color,
    val bgLayer2: Color,
    val bgLayer3: Color,
    val bgOverlay: Color,
    val borderL1: Color,
    val borderL2: Color,
    val borderL3: Color,
    val brandPrimary: Color,
    val brandText: Color,
    val textPrimary: Color,
    val textSecondary: Color,
    val textTertiary: Color,
    val textInverted: Color,
    val diffAdded: Color,
    val diffDeleted: Color,
    // --dsw-specific-bubble: the user bubble fill. Light binds deepseek-50,
    // dark binds bluish-850 (design-platform.css:268/386).
    val bubble: Color,
    // --dsw-specific-input-major: the composer card fill. Light bluish-00,
    // dark bluish-850 (269/387).
    val inputMajor: Color,
    val buttonPrimaryFill: Color,
    val buttonPrimaryHover: Color,
    val buttonGhostActiveFill: Color,
    val danger: Color,
    val type: DswType = DswType(),
) {
    // Typography forwarders: the primitives read sizes straight off the
    // theme (dsw.textSize), keeping call sites one indirection shorter than
    // threading DswType through every signature.
    val textSize: TextUnit get() = type.textSize.sp
    val codeSize: TextUnit get() = type.codeSize.sp
    val labelSize: TextUnit get() = type.labelSize.sp
    val terminalSize: TextUnit get() = type.terminalSize.sp
    val titleSize: TextUnit get() = type.titleSize.sp
}

/** The light theme: the token values the official light build ships. */
val DswLight = Dsw(
    bgBase = Color(0xFFFFFFFF),
    bgLayer1 = Color(0xFFFAFAFA),
    bgLayer2 = Color(0xFFF5F5F5),
    bgLayer3 = Color(0xFFEDEDED),
    bgOverlay = Color(0x14000000),
    borderL1 = Color(0xFFEDEDED),
    borderL2 = Color(0xFFE5E5E5),
    borderL3 = Color(0xFFD4D4D4),
    // --dsw-alias-brand-primary = deepseek-500 rgb(65,118,230)
    brandPrimary = Color(0xFF4176E6),
    brandText = Color(0xFF4176E6),
    textPrimary = Color(0xFF0F0F0F),
    textSecondary = Color(0xFF545557),
    textTertiary = Color(0xFF7F8287),
    textInverted = Color(0xFFFFFFFF),
    diffAdded = Color(0xFFE6FAED),
    diffDeleted = Color(0xFFFEF2F2),
    buttonPrimaryFill = Color(0xFF4176E6),
    buttonPrimaryHover = Color(0xFF2551A8),
    buttonGhostActiveFill = Color(0xFFEDEDED),
    danger = Color(0xFFEC1313),
    // deepseek-50 rgb(237,243,254)
    bubble = Color(0xFFEDF3FE),
    // bluish-00
    inputMajor = Color(0xFFFFFFFF),
)

/** The dark theme: the token values the official dark build ships. */
val DswDark = DswLight.copy(
    bgBase = Color(0xFF0F0F0F),
    bgLayer1 = Color(0xFF1B1B1C),
    bgLayer2 = Color(0xFF212123),
    bgLayer3 = Color(0xFF292929),
    bgOverlay = Color(0x66000000),
    borderL1 = Color(0xFF292929),
    borderL2 = Color(0xFF333335),
    borderL3 = Color(0xFF444446),
    // dark mode brightens the brand so it reads on the dark surface
    brandPrimary = Color(0xFF7AAAFF),
    brandText = Color(0xFF7AAAFF),
    textPrimary = Color(0xFFEBEEF2),
    textSecondary = Color(0xFFADB2B8),
    textTertiary = Color(0xFF979DA6),
    textInverted = Color(0xFF0F0F0F),
    diffAdded = Color(0xFF233C2C),
    diffDeleted = Color(0xFF571212),
    buttonPrimaryFill = Color(0xFF4176E6),
    buttonPrimaryHover = Color(0xFF5686FE),
    buttonGhostActiveFill = Color(0xFF292929),
    // bluish-850 rgb(44,44,46)
    bubble = Color(0xFF2C2C2E),
    inputMajor = Color(0xFF2C2C2E),
)

/**
 * The typography ladder. The official client lets the user tune three sizes
 * (text/code/terminal); the mobile client keeps the same three knobs at the
 * same defaults.
 */
@Immutable
data class DswType(
    val textSize: Float = 14f,
    val codeSize: Float = 11f,
    val terminalSize: Float = 13f,
    val titleSize: Float = 16f,
    val labelSize: Float = 12f,
)

/**
 * The corner ladder, from the official control specs: a 36px control carries
 * 12px corners, a 28px control 8px corners.
 */
@Immutable
data class DswShape(
    // The official radius ladder (base.css:19-24).
    val xs: Int = 4,
    val sm: Int = 8,
    val md: Int = 12,
    val lg: Int = 16,
    val xl: Int = 20,
    // Panels and the composer card (base.css:23).
    val panel: Int = 28,
)
