package com.sta1n.qrpdsh

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Build
import androidx.compose.material.icons.filled.KeyboardArrowDown
import androidx.compose.material.icons.filled.KeyboardArrowUp
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Icon
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.TextUnit
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw
import org.json.JSONArray
import org.json.JSONObject

/**
 * One tool call and its result, folded into a card by [parseReplay].
 *
 * WHY COLLAPSED BY DEFAULT. Tool arguments and outputs are often long JSON or
 * search dumps; the transcript should stay scannable, and the header already
 * says which tool ran. Expansion is per-card local state so opening one card
 * never moves another.
 */
@Composable
fun ToolCard(node: ToolNode, dsw: Dsw, turnActive: Boolean) {
    // Plain remember, not rememberSaveable: a LazyColumn can restore a saved
    // expansion state under the same "tool:<seq>" key after a session switch,
    // which would open a card in the new session that the user never opened.
    var expanded by remember(node.key) { mutableStateOf(false) }
    val accent = if (node.failed) dsw.danger else dsw.borderL2

    Surface(
        color = dsw.bgLayer1,
        shape = RoundedCornerShape(12.dp),
        border = BorderStroke(1.dp, accent),
        modifier = Modifier.fillMaxWidth(),
    ) {
        Column(Modifier.fillMaxWidth()) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { expanded = !expanded }
                    .padding(horizontal = 12.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Icon(
                    Icons.Default.Build,
                    contentDescription = null,
                    tint = if (node.failed) dsw.danger else dsw.textSecondary,
                    modifier = Modifier.size(16.dp),
                )
                Spacer(Modifier.width(8.dp))
                Text(
                    text = node.name.ifBlank { "工具" },
                    modifier = Modifier.weight(1f),
                    color = dsw.textPrimary,
                    fontSize = dsw.labelSize,
                    fontWeight = FontWeight.Medium,
                    maxLines = 1,
                )
                if (node.failed) {
                    Spacer(Modifier.width(6.dp))
                    Icon(
                        Icons.Default.Warning,
                        contentDescription = "失败",
                        tint = dsw.danger,
                        modifier = Modifier.size(14.dp),
                    )
                } else if (node.result == null) {
                    Spacer(Modifier.width(8.dp))
                    // A call that never got a result is only "running" while
                    // the turn is; after a cancel or a reload it is an
                    // interrupted call, not an eternal spinner.
                    Text(
                        text = if (turnActive) "运行中" else "已中断",
                        color = dsw.textTertiary,
                        fontSize = 11.sp,
                    )
                }
                Icon(
                    if (expanded) Icons.Default.KeyboardArrowUp else Icons.Default.KeyboardArrowDown,
                    contentDescription = if (expanded) "收起" else "展开",
                    tint = dsw.textTertiary,
                    modifier = Modifier.size(18.dp),
                )
            }
            if (expanded) {
                // Parse only when the card is open: a collapsed card should
                // not pay to pretty-print a potentially large argument JSON.
                val arguments = remember(node.arguments) { prettyJson(node.arguments) }
                Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 2.dp)) {
                    ToolSection(
                        label = "参数",
                        body = arguments.ifBlank { "无" },
                        bodyColor = dsw.textSecondary,
                        labelColor = dsw.textTertiary,
                        fontSize = dsw.textSize,
                    )
                    if (node.result != null) {
                        Spacer(Modifier.height(8.dp))
                        ToolSection(
                            label = if (node.failed) "错误" else "结果",
                            body = node.result.ifBlank { "（无输出）" },
                            bodyColor = if (node.failed) dsw.danger else dsw.textPrimary,
                            labelColor = dsw.textTertiary,
                            fontSize = dsw.textSize,
                        )
                    }
                    Spacer(Modifier.height(10.dp))
                }
            }
        }
    }
}

@Composable
private fun ToolSection(
    label: String,
    body: String,
    bodyColor: Color,
    labelColor: Color,
    fontSize: TextUnit,
) {
    Column(Modifier.fillMaxWidth()) {
        Text(label, color = labelColor, fontSize = 11.sp, fontWeight = FontWeight.Medium)
        Spacer(Modifier.height(4.dp))
        SelectionContainer {
            Text(
                text = body,
                color = bodyColor,
                fontSize = fontSize,
                fontFamily = FontFamily.Monospace,
                lineHeight = 18.sp,
            )
        }
    }
}

/** Pretty-print JSON arguments; leave anything that is not JSON untouched. */
private fun prettyJson(raw: String): String {
    val trimmed = raw.trim()
    if (trimmed.isEmpty()) return ""
    return runCatching {
        when {
            trimmed.startsWith("{") -> JSONObject(trimmed).toString(2)
            trimmed.startsWith("[") -> JSONArray(trimmed).toString(2)
            else -> raw
        }
    }.getOrDefault(raw)
}
