package com.sta1n.qrpdsh

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.Immutable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.mikepenz.markdown.compose.Markdown
import com.mikepenz.markdown.model.MarkdownColors
import com.mikepenz.markdown.model.MarkdownTypography
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.theme.LocalDsw

/**
 * DSH tokens as the renderer's colors. The renderer asks for the values it
 * needs instead of reading MaterialTheme, so the dark/light switch reaches it
 * through the same source as the rest of the screen.
 */
@Immutable
@Suppress("OVERRIDE_DEPRECATION")
private class DshMarkdownColors(dsw: Dsw) : MarkdownColors {
    override val text: Color = dsw.textPrimary
    override val codeText: Color = dsw.textPrimary
    override val inlineCodeText: Color = dsw.textPrimary
    override val linkText: Color = dsw.brandText
    override val codeBackground: Color = dsw.bgLayer2
    override val inlineCodeBackground: Color = dsw.bgLayer2
    override val dividerColor: Color = dsw.borderL2
    override val tableText: Color = dsw.textPrimary
    override val tableBackground: Color = dsw.bgLayer1
}

/**
 * DSH tokens as the renderer's type ramp. The renderer has no Material
 * adapter in the cached artifact (verified with javap against 0.34.0), so the
 * mapping lives here; one place to change when the token sheet moves.
 */
@Immutable
@Suppress("OVERRIDE_DEPRECATION")
private class DshMarkdownTypography(dsw: Dsw) : MarkdownTypography {
    private val base = TextStyle(
        color = dsw.textPrimary,
        fontSize = dsw.textSize,
        lineHeight = 22.sp,
    )

    override val text: TextStyle = base
    override val code: TextStyle = TextStyle(
        color = dsw.textPrimary,
        fontSize = dsw.codeSize,
        lineHeight = 16.sp,
        fontFamily = FontFamily.Monospace,
    )
    override val inlineCode: TextStyle = code.copy(background = dsw.bgLayer2)
    override val h1: TextStyle = base.copy(fontSize = 22.sp, lineHeight = 30.sp, fontWeight = FontWeight.Bold)
    override val h2: TextStyle = base.copy(fontSize = 20.sp, lineHeight = 28.sp, fontWeight = FontWeight.Bold)
    override val h3: TextStyle = base.copy(fontSize = 18.sp, lineHeight = 26.sp, fontWeight = FontWeight.SemiBold)
    override val h4: TextStyle = base.copy(fontSize = 16.sp, lineHeight = 24.sp, fontWeight = FontWeight.SemiBold)
    override val h5: TextStyle = base.copy(fontSize = 15.sp, lineHeight = 23.sp, fontWeight = FontWeight.Medium)
    override val h6: TextStyle = base.copy(fontSize = 14.sp, lineHeight = 22.sp, fontWeight = FontWeight.Medium)
    override val quote: TextStyle = base.copy(color = dsw.textSecondary)
    override val paragraph: TextStyle = base
    override val ordered: TextStyle = base
    override val bullet: TextStyle = base
    override val list: TextStyle = base
    override val link: TextStyle = base.copy(color = dsw.brandText)
    override val textLink: TextLinkStyles = TextLinkStyles(style = SpanStyle(color = dsw.brandText))
    override val table: TextStyle = base
}

/** The only entry point for rendered assistant text, so colors cannot drift. */
@Composable
fun MarkdownText(text: String, modifier: Modifier = Modifier) {
    val dsw = LocalDsw.current
    // The renderer compares these by identity-ish reference during
    // recomposition; recreating them on every keystroke would invalidate its
    // cached parse for no reason.
    val colors = remember(dsw) { DshMarkdownColors(dsw) }
    val typography = remember(dsw) { DshMarkdownTypography(dsw) }
    Markdown(
        content = text,
        colors = colors,
        typography = typography,
        modifier = modifier,
    )
}

@Composable
fun UserBubble(text: String, dsw: Dsw) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
        Surface(color = dsw.bubble, shape = RoundedCornerShape(20.dp)) {
            Text(
                text = text,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                color = dsw.textPrimary,
                fontSize = dsw.textSize,
            )
        }
    }
}

/**
 * The live segment: plain text only. Streaming chunks arrive every few
 * milliseconds and running the Markdown parser per frame would spend more
 * CPU than the extra formatting is worth. Durable messages switch to
 * [MarkdownText] once the turn is replayed.
 */
@Composable
fun StreamingBlock(liveText: String, liveTool: String?, busy: Boolean, dsw: Dsw) {
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        if (liveTool != null) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(14.dp), strokeWidth = 2.dp, color = dsw.brandPrimary)
                Spacer(Modifier.width(8.dp))
                // The name often arrives one delta after the call exists.
                val label = if (liveTool.isBlank() || liveTool == "工具") "正在准备工具调用" else "正在调用 $liveTool"
                Text(label, color = dsw.textSecondary, fontSize = dsw.labelSize)
            }
        }
        if (liveText.isNotEmpty()) {
            Text(
                text = liveText,
                modifier = Modifier.fillMaxWidth(),
                color = dsw.textPrimary,
                fontSize = dsw.textSize,
                lineHeight = 22.sp,
            )
        } else if (busy && liveTool == null) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                CircularProgressIndicator(Modifier.size(16.dp), strokeWidth = 2.dp, color = dsw.brandPrimary)
                Spacer(Modifier.width(8.dp))
                Text("正在思考", color = dsw.textSecondary, fontSize = dsw.labelSize)
            }
        }
    }
}
