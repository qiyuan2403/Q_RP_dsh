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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.theme.LocalDsw

/**
 * The only entry point for rendered assistant text, so colors cannot drift.
 *
 * Parsing lives in [MarkdownBody]; this wrapper exists so call sites do not
 * need to resolve the theme themselves.
 */
@Composable
fun MarkdownText(text: String, modifier: Modifier = Modifier) {
    val dsw = LocalDsw.current
    Column(modifier) { MarkdownBody(text, dsw) }
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
