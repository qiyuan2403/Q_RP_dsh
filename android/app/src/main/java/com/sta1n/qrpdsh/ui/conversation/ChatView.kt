package com.sta1n.qrpdsh.ui.conversation

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.material3.TextFieldDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.sta1n.qrpdsh.ChatRow
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.Width

/**
 * The chat flow + composer, ported from ui-chat ChatView.module.css,
 * MessageItem.module.css, TurnProcessNodeView.module.css, and
 * ui-conversation InputBar.module.css. All numbers are the source's.
 *
 * ChatView.contract: the transcript and the composer share the content axis
 * (BoxWithConstraints resolves it); the composer card caps at content + 32.
 */
@Composable
fun ChatView(
    dsw: Dsw,
    rows: List<ChatRow>,
    turnRunning: Boolean,
    onSend: (String) -> Unit,
) {
    var widthPref by remember { mutableStateOf<Int?>(null) }
    BoxWithConstraints(Modifier.fillMaxSize().imePadding().navigationBarsPadding()) {
        val density = LocalDensity.current
        val columnPx = with(density) { maxWidth.toPx().toInt() }
        val contentWidth = Width.resolveContentWidth(columnPx, widthPref)
        val contentDp = with(density) { Dp(contentWidth.toFloat()) }

        var followingTail by remember { mutableStateOf(true) }
        Box(Modifier.fillMaxSize()) {
            Transcript(dsw, rows, Modifier.fillMaxSize(), contentDp, followingTail) { followingTail = it }
            if (Width.handleVisible(columnPx, contentWidth)) {
                WidthHandle(dsw, columnPx, contentWidth, widthPref) { widthPref = it }
            }
            if (!followingTail) {
                ToBottomButton(dsw) { followingTail = true }
            }
        }
        Composer(dsw, turnRunning, contentDp, onSend)
    }
}

private val FlowGap = 6.dp
private val ResponseGap = 12.dp

@Composable
private fun Transcript(dsw: Dsw, rows: List<ChatRow>, modifier: Modifier, contentWidth: Dp, followingTail: Boolean, onFollowing: (Boolean) -> Unit) {
    val listState = rememberLazyListState()
    LaunchedEffect(rows.size, followingTail) {
        if (followingTail && rows.isNotEmpty()) listState.scrollToItem(rows.size - 1)
    }
    LaunchedEffect(listState.isScrollInProgress) {
        if (!listState.isScrollInProgress && listState.canScrollBackward) {
            val lastVisible = listState.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0
            onFollowing(lastVisible >= rows.size - 1)
        }
    }
    Column(
        modifier = modifier.fillMaxWidth().padding(horizontal = Width.transcriptSidePadding),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        if (rows.isEmpty()) {
            Text(
                "No messages yet. Set the API key in settings, then send a message.",
                modifier = Modifier.fillMaxWidth().padding(top = 96.dp),
                color = dsw.textTertiary,
                fontSize = 14.sp2(),
            )
        } else {
            LazyColumn(
                state = listState,
                modifier = Modifier.fillMaxSize(),
                verticalArrangement = Arrangement.spacedBy(FlowGap),
            ) {
                itemsIndexed(rows) { index, row ->
                    val topGap = when {
                        index == 0 -> 0.dp
                        rows[index - 1] is ChatRow.ToolResult && row is ChatRow.Assistant -> ResponseGap
                        rows[index - 1] is ChatRow.Assistant -> ResponseGap
                        else -> FlowGap
                    }
                    Box(
                        Modifier.padding(top = if (index == 0) 16.dp else topGap)
                            .widthIn(max = contentWidth),
                    ) {
                        when (row) {
                            is ChatRow.User -> UserBubble(dsw, row.text)
                            is ChatRow.Assistant -> AssistantText(dsw, row.text)
                            is ChatRow.AssistantWithTools -> {
                                if (row.text.isNotEmpty()) AssistantText(dsw, row.text)
                            }
                            is ChatRow.ToolCall -> ProcessRow(dsw, row.name, row.arguments, pending = true)
                            is ChatRow.ToolResult -> ProcessRow(dsw, row.name, row.output, pending = false, error = row.error)
                            is ChatRow.Error -> ErrorRow(dsw, row.text)
                        }
                    }
                }
            }
        }
    }
}

private fun Int.sp2() = androidx.compose.ui.unit.TextUnit(this.toFloat(), androidx.compose.ui.unit.TextUnitType.Sp)

/** MessageItem.module.css .userRow/.bubble: deepseek-50 fill, radius-xl, 10x16. */
@Composable
private fun UserBubble(dsw: Dsw, text: String) {
    Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
        Surface(
            shape = RoundedCornerShape(20.dp),
            color = dsw.bubble,
            modifier = Modifier.widthIn(max = 280.dp),
        ) {
            Text(
                text,
                color = dsw.textPrimary,
                fontSize = 14.sp2(),
                lineHeight = 22.sp2(),
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
            )
        }
    }
}

/** AssistantMarkdown: no bubble, bare text on the column. */
@Composable
private fun AssistantText(dsw: Dsw, text: String) {
    Text(
        text,
        color = dsw.textPrimary,
        fontSize = 14.sp2(),
        lineHeight = 22.sp2(),
        modifier = Modifier.fillMaxWidth(),
    )
}

/**
 * TurnProcessNodeView .root: a 33px disclosure line, 0.5px border-l2 bottom
 * rule, tertiary label, chevron flip. Not a card.
 */
@Composable
private fun ProcessRow(dsw: Dsw, name: String, body: String, pending: Boolean, error: String = "") {
    var expanded by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth()) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            modifier = Modifier.fillMaxWidth()
                .heightIn(min = 33.dp)
                .border(0.5.dp, dsw.borderL2, RectangleShape)
                .clickable { expanded = !expanded }
                .padding(horizontal = 4.dp),
        ) {
            Text(if (expanded) "▾" else "▸", color = dsw.textTertiary, fontSize = 14.sp2())
            Text(
                name,
                color = if (error.isNotEmpty()) dsw.danger else dsw.textTertiary,
                fontSize = 14.sp2(),
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
            Text(
                when {
                    error.isNotEmpty() -> "error"
                    pending -> "…"
                    else -> ""
                },
                color = if (error.isNotEmpty()) dsw.danger else dsw.textTertiary,
                fontSize = 14.sp2(),
            )
        }
        if (expanded && body.isNotEmpty()) {
            Text(
                body,
                color = dsw.textSecondary,
                fontSize = 11.sp2(),
                fontFamily = FontFamily.Monospace,
                modifier = Modifier.fillMaxWidth().padding(top = 6.dp, start = 20.dp),
            )
        }
    }
}

@Composable
private fun ErrorRow(dsw: Dsw, text: String) {
    Text(text, color = dsw.danger, fontSize = 14.sp2(), modifier = Modifier.fillMaxWidth())
}

/** ChatView .toBottom: a 34px round float, bottom-right. */
@Composable
private fun androidx.compose.foundation.layout.BoxScope.ToBottomButton(dsw: Dsw, onClick: () -> Unit) {
    Surface(
        shape = RoundedCornerShape(100.dp),
        color = dsw.bgLayer1,
        shadowElevation = 2.dp,
        modifier = Modifier
            .align(Alignment.BottomEnd)
            .padding(end = 16.dp, bottom = 16.dp)
            .size(34.dp)
            .clickable(onClick = onClick),
    ) {
        Box(contentAlignment = Alignment.Center) {
            Text("↓", color = dsw.textPrimary, fontSize = 14.sp2())
        }
    }
}

/**
 * WidthHandle: ConversationWidthControls.tsx — ×2 symmetric delta, no
 * inertia, frame-coalesced by recomposition, commit only on a real drag
 * (the official guard is origin != latest; 2px here to survive jitter),
 * preference persisted under the official key name.
 */
@Composable
private fun androidx.compose.foundation.layout.BoxScope.WidthHandle(
    dsw: Dsw,
    columnPx: Int,
    contentWidth: Int,
    pref: Int?,
    onPref: (Int?) -> Unit,
) {
    var originX by remember { mutableStateOf(0f) }
    var baseWidth by remember { mutableStateOf(0) }
    var latestX by remember { mutableStateOf(0f) }
    Box(
        Modifier
            .align(Alignment.CenterEnd)
            .fillMaxSize()
            .widthIn(max = 10.dp)
            .pointerInput(columnPx, contentWidth) {
                awaitEachGesture {
                    val down = awaitFirstDown()
                    originX = down.position.x
                    latestX = originX
                    baseWidth = Width.resolveContentWidth(columnPx, pref)
                    var moved = false
                    drag(down.id) { change ->
                        latestX = change.position.x
                        if (kotlin.math.abs(latestX - originX) >= 2f) moved = true
                        onPref(Width.draggedWidth(baseWidth, originX, latestX, leftHandle = false))
                    }
                    // The onCommit guard: a tap never overwrites the pref.
                    if (!moved) onPref(pref)
                }
            },
    )
}

/**
 * InputBar.module.css .card: radius-panel 28px, fill specific-input-major,
 * hairline border-l2 stroke, 8px top pad, 12px gap, side clearance 16.
 */
@Composable
private fun Composer(dsw: Dsw, turnRunning: Boolean, contentWidth: Dp, onSend: (String) -> Unit) {
    var text by remember { mutableStateOf("") }
    Column(
        Modifier.fillMaxWidth().padding(horizontal = Width.composerSideClearance * 2, vertical = 4.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Column(
            Modifier.fillMaxWidth().widthIn(max = contentWidth)
                .background(dsw.inputMajor, RoundedCornerShape(28.dp))
                .border(0.5.dp, dsw.borderL2, RoundedCornerShape(28.dp))
                .padding(top = 8.dp, bottom = 8.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            TextField(
                value = text,
                onValueChange = { text = it },
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
                placeholder = { Text("Message", color = dsw.textTertiary, fontSize = 14.sp2()) },
                maxLines = 5,
                colors = TextFieldDefaults.colors(
                    focusedContainerColor = Color.Transparent,
                    unfocusedContainerColor = Color.Transparent,
                    focusedIndicatorColor = Color.Transparent,
                    unfocusedIndicatorColor = Color.Transparent,
                    cursorColor = dsw.brandPrimary,
                    focusedTextColor = dsw.textPrimary,
                    unfocusedTextColor = dsw.textPrimary,
                ),
            )
            Row(
                Modifier.fillMaxWidth().padding(horizontal = 12.dp),
                horizontalArrangement = Arrangement.End,
            ) {
                Button(
                    onClick = {
                        onSend(text)
                        text = ""
                    },
                    enabled = text.isNotBlank() && !turnRunning,
                    shape = RoundedCornerShape(100.dp),
                    contentPadding = androidx.compose.foundation.layout.PaddingValues(horizontal = 20.dp, vertical = 6.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = dsw.buttonPrimaryFill,
                        contentColor = dsw.textInverted,
                        disabledContainerColor = dsw.bgLayer2,
                        disabledContentColor = dsw.textTertiary,
                    ),
                ) {
                    Text(if (turnRunning) "…" else "Send", fontSize = 14.sp2())
                }
            }
        }
    }
}
