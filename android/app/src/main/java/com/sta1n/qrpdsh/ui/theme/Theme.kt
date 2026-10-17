package com.sta1n.qrpdsh.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.graphics.Color
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.DswDark
import com.sta1n.qrpdsh.ui.DswLight

/**
 * The DSW theme. Compose's MaterialTheme is bridged only where Material
 * components need a scheme; every DSH-specific read goes through
 * [LocalDsw], the port of the official token sheet (ui-theme
 * design-platform.css), light and dark bound exactly as the source does.
 */
val LocalDsw = staticCompositionLocalOf<Dsw> { DswLight }

@Composable
fun DswTheme(dark: Boolean, content: @Composable () -> Unit) {
    val dsw = if (dark) DswDark else DswLight
    // Bridge the tokens Material actually reads. DSH controls are custom;
    // the Material defaults only need to not clash with the surface.
    val scheme = if (dark) {
        darkColorScheme(
            background = dsw.bgBase,
            surface = dsw.bgLayer1,
            primary = dsw.brandPrimary,
            onPrimary = dsw.textInverted,
            onBackground = dsw.textPrimary,
            onSurface = dsw.textPrimary,
            error = dsw.danger,
        )
    } else {
        lightColorScheme(
            background = dsw.bgBase,
            surface = dsw.bgLayer1,
            primary = dsw.brandPrimary,
            onPrimary = dsw.textInverted,
            onBackground = dsw.textPrimary,
            onSurface = dsw.textPrimary,
            error = dsw.danger,
        )
    }
    CompositionLocalProvider(LocalDsw provides dsw) {
        MaterialTheme(colorScheme = scheme, content = content)
    }
}

/** The DSW accessor inside composables: `val dsw = LocalDsw.current`. */
@Composable
fun dswTheme(): Dsw = LocalDsw.current

/** Color bridge for the few Material components that take raw Colors. */
val Dsw.materialBackground: Color get() = bgBase
