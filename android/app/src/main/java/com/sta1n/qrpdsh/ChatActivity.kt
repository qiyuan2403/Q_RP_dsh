package com.sta1n.qrpdsh

import android.content.Context
import android.os.Bundle
import android.util.Log
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.waitForUpOrCancellation
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
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
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.lifecycleScope
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.Width
import com.sta1n.qrpdsh.ui.DswDark
import com.sta1n.qrpdsh.ui.DswLight
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException

/**
 * The conversation surface, drawn in DSH tokens.
 *
 * One activity, one session, one column. Events arrive two ways: the replay
 * on open (openSession returns the recorded events) and the live poll
 * (pollEvent on a worker thread while a turn runs). Both feed the same
 * append-only projection — rows — which is what a session log is.
 */
class ChatActivity : ComponentActivity() {
    private val tag = "qrpdsh"

    private var sessionId by mutableStateOf<String?>(null)
    private val rows = mutableStateListOf<ChatRow>()
    private var nodeExecutable by mutableStateOf<String?>(null)
    private var turnRunning by mutableStateOf(false)
    private var showSettings by mutableStateOf(false)
    private var followingTail by mutableStateOf(true)
    private var config by mutableStateOf(Config.EMPTY)
    private var dsw: Dsw = DswLight

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        config = Config.load(this)
        dsw = if (isDarkTheme()) DswDark else DswLight
        setContent { ChatSurface() }

        lifecycleScope.launch(Dispatchers.IO) {
            // The node runtime materializes on first launch; both the session
            // open and this copy are IO, so they run off the main thread.
            val node = try {
                NodeRuntime.materialize(applicationContext)
            } catch (e: IOException) {
                null
            } catch (t: Throwable) {
                Log.e(tag, "node materialize failed", t)
                null
            }
            withContext(Dispatchers.Main) {
                nodeExecutable = node
            }
            val session = Native.openSession(getFilesDir().absolutePath)
            withContext(Dispatchers.Main) {
                applySession(session)
            }
        }
    }

    /** Projects a session document (id + replayed events) into rows. */
    private fun applySession(document: String) {
        try {
            val obj = JSONObject(document)
            if (obj.has("error")) {
                toast("session open failed: " + obj.getString("error"))
                return
            }
            sessionId = obj.getString("id")
            rows.clear()
            val events = obj.optJSONArray("events") ?: JSONArray()
            for (i in 0 until events.length()) {
                project(events.getJSONObject(i))?.let { rows.add(it) }
            }
        } catch (e: Exception) {
            Log.e(tag, "session replay failed", e)
            toast("session replay failed: " + e.message)
        }
    }

    /**
     * Sends one user turn and starts the live event poll. The poll thread
     * owns the blocking JNI call; each event lands on the main thread and
     * mutates rows, which is the whole render pipeline.
     */
    private fun send(task: String) {
        val sid = sessionId ?: return
        if (turnRunning || task.isBlank()) return
        val cfg = config
        if (cfg.apiKey.isBlank()) {
            toast("set a base URL, API key, and model in settings first")
            showSettings = true
            return
        }
        turnRunning = true
        rows.add(ChatRow.User(task))
        val node = nodeExecutable
        lifecycleScope.launch(Dispatchers.IO) {
            // The poller runs while the turn runs: Native.runTurn blocks this
            // coroutine until the turn ends, so the poller gets its own.
            val poller = launch {
                while (true) {
                    val raw = try {
                        Native.pollEvent(sid, 250)
                    } catch (t: Throwable) {
                        Log.e(tag, "pollEvent failed", t)
                        break
                    }
                    if (raw.isEmpty()) continue
                    val row = try {
                        project(JSONObject(raw))
                    } catch (e: Exception) {
                        null
                    }
                    withContext(Dispatchers.Main) { row?.let { rows.add(it) } }
                }
            }
            val result = try {
                Native.runTurn(task, getFilesDir().absolutePath, getFilesDir().absolutePath,
                        cfg.baseUrl, cfg.apiKey, cfg.model, node, sid)
            } catch (t: Throwable) {
                Log.e(tag, "runTurn threw", t)
                "{\"error\":\"" + t.message + "\"}"
            }
            poller.cancel()
            withContext(Dispatchers.Main) {
                turnRunning = false
                applyResult(result)
            }
        }
    }

    /** Applies a turn result: error surfaces as a row; success is in the log. */
    private fun applyResult(result: String) {
        try {
            val obj = JSONObject(result)
            if (obj.has("error")) {
                rows.add(ChatRow.Error(obj.getString("error")))
            }
        } catch (e: Exception) {
            rows.add(ChatRow.Error(result))
        }
    }

    /** One event → one row, or null for events the transcript does not show. */
    private fun project(e: JSONObject): ChatRow? {
        return when (e.optString("type")) {
            "user/message" -> {
                val data = e.optJSONObject("data")
                ChatRow.User(data?.optString("text") ?: "")
            }
            "assistant/message" -> {
                val data = e.optJSONObject("data")
                val text = data?.optString("text") ?: ""
                val calls = data?.optJSONArray("toolCalls")
                if (calls != null && calls.length() > 0) {
                    val names = (0 until calls.length()).mapNotNull { i ->
                        calls.optJSONObject(i)?.optString("name")
                    }
                    ChatRow.AssistantWithTools(text, names)
                } else {
                    ChatRow.Assistant(text)
                }
            }
            "tool/call" -> {
                val data = e.optJSONObject("data")
                ChatRow.ToolCall(data?.optString("name") ?: "?", data?.optString("arguments") ?: "")
            }
            "tool/result" -> {
                val data = e.optJSONObject("data")
                val err = data?.optString("error", "") ?: ""
                ChatRow.ToolResult(data?.optString("name") ?: "?", data?.optString("output") ?: "", err)
            }
            else -> null
        }
    }

    private fun toast(msg: String) {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
    }

    private fun isDarkTheme(): Boolean {
        val mode = resources.configuration.uiMode and
                android.content.res.Configuration.UI_MODE_NIGHT_MASK
        return mode == android.content.res.Configuration.UI_MODE_NIGHT_YES
    }

    // ------------------------------------------------------------------ UI

    // The user's dragged width preference, persisted like the official
    // client's localStorage key. Null = follow the default solver.
    private var widthPref by mutableStateOf<Int?>(null)
    // The live content-column width (the --dsh-chat-content-width axis).
    private var contentDp by mutableStateOf(androidx.compose.ui.unit.Dp.Unspecified)

    @Composable
    private fun ChatSurface() {
        Surface(modifier = Modifier.fillMaxSize(), color = dsw.bgBase) {
            Column(modifier = Modifier.fillMaxSize().imePadding().navigationBarsPadding()) {
                TopBar(onSettings = { showSettings = true })
                // The adaptive axis: content width from the ported solver,
                // exactly what --dsh-chat-content-width resolves to.
                androidx.compose.foundation.layout.BoxWithConstraints(
                    Modifier.weight(1f).fillMaxWidth(),
                ) {
                    val columnPx = with(androidx.compose.ui.platform.LocalDensity.current) {
                        maxWidth.toPx().toInt()
                    }
                    val contentWidth = Width.resolveContentWidth(columnPx, widthPref)
                    contentDp = with(androidx.compose.ui.platform.LocalDensity.current) {
                        androidx.compose.ui.unit.Dp(contentWidth.toFloat())
                    }
                    val left = Width.handleVisible(columnPx, contentWidth)
                    Box(Modifier.fillMaxSize()) {
                        Transcript(rows, Modifier.fillMaxSize(), contentDp)
                        if (left) {
                            WidthHandle(leftHandle = true, columnPx, contentWidth)
                        }
                        WidthHandle(leftHandle = false, columnPx, contentWidth)
                        if (!followingTail) {
                            ToBottomButton { followingTail = false }
                        }
                    }
                }
                Composer(contentDp)
            }
            if (showSettings) {
                SettingsSheet(config) { updated ->
                    config = updated
                    updated.save(this)
                    showSettings = false
                }
            }
        }
    }

    /**
     * A width handle: a 10px edge strip with the drag feel of the official
     * client — pointer-captured, frame-coalesced by composition itself, ×2
     * symmetric delta, no inertia, commit only on a real drag. Sits 24px
     * outside the content column (ConversationRoot.module.css .widthHandle).
     */
    @Composable
    private fun androidx.compose.foundation.layout.BoxScope.WidthHandle(
        leftHandle: Boolean,
        columnPx: Int,
        contentWidth: Int,
    ) {
        var originX by remember { mutableStateOf(0f) }
        var baseWidth by remember { mutableStateOf(0) }
        var latestX by remember { mutableStateOf(0f) }
        val density = androidx.compose.ui.platform.LocalDensity.current
        val stripPx = with(density) { Width.handleWidthPx(columnPx, contentWidth).dp.toPx() }
        Surface(
            color = Color.Transparent,
            modifier = Modifier
                .align(if (leftHandle) Alignment.CenterStart else Alignment.CenterEnd)
                .widthIn(min = 10.dp)
                .fillMaxSize(0.02f)
                .pointerInput(leftHandle) {
                    awaitPointerEventScope {
                        while (true) {
                            val down = awaitFirstDown()
                            originX = down.position.x
                            latestX = originX
                            baseWidth = Width.resolveContentWidth(columnPx, widthPref)
                            var moved = false
                            // Pointer capture is implicit in pointerInput scope:
                            // events keep arriving while the finger is down.
                            drag(down.id) {
                                latestX = it.position.x
                                moved = moved || kotlin.math.abs(latestX - originX) > 0.5f
                                widthPref = Width.draggedWidth(baseWidth, originX, latestX, leftHandle)
                            }
                            // Commit only on a real drag (the onCommit guard).
                            if (moved) {
                                getSharedPreferences("qrpdsh", Context.MODE_PRIVATE)
                                    .edit().putInt(Width.WIDTH_PREF_KEY, widthPref ?: 0).apply()
                            } else {
                                widthPref = baseWidth
                            }
                        }
                    }
                },
        ) {}
    }

    @Composable
    private fun TopBar(onSettings: () -> Unit) {
        Row(
            modifier = Modifier.fillMaxWidth()
                .windowInsetsPadding(WindowInsets.statusBars)
                .padding(horizontal = 16.dp, vertical = 10.dp),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("Q_RP_dsh", fontSize = dsw.titleSize, color = dsw.textPrimary)
            Button(
                onClick = onSettings,
                shape = RoundedCornerShape(8.dp),
                colors = ButtonDefaults.buttonColors(
                    containerColor = Color.Transparent,
                    contentColor = dsw.textSecondary,
                ),
            ) { Text("Settings", fontSize = dsw.labelSize) }
        }
    }

    // The DSH flow gaps (ChatView.module.css): 6px default, 12px between a
    // response row and its neighbours, 16px around a turn boundary.
    private val FlowGap = 6.dp
    private val ResponseGap = 12.dp

    @Composable
    private fun Transcript(rows: List<ChatRow>, modifier: Modifier = Modifier, contentWidth: androidx.compose.ui.unit.Dp = androidx.compose.ui.unit.Dp.Unspecified) {
        val listState = rememberLazyListState()
        // The follow contract (data-chat-following-tail): pinned to the tail
        // until the user scrolls away; then the to-bottom float appears.
        LaunchedEffect(rows.size, followingTail) {
            if (followingTail && rows.isNotEmpty()) {
                listState.scrollToItem(rows.size - 1)
            }
        }
        LaunchedEffect(listState.isScrollInProgress) {
            if (!listState.isScrollInProgress && listState.canScrollBackward) {
                val lastVisible = listState.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: 0
                followingTail = lastVisible >= rows.size - 1
            }
        }
        // .scroll: padding 16px calc(clearance+16px) — the transcript sits 16px
        // further in than the composer card's clearance. The message column is
        // the content-width axis, centered (.column max-width + margin auto).
        Column(
            modifier = modifier.fillMaxWidth().padding(horizontal = Width.transcriptSidePadding),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            if (rows.isEmpty()) {
                Text(
                    "No messages yet. Set the API key in settings, then send a message.",
                    modifier = Modifier.fillMaxWidth().padding(top = 96.dp),
                    color = dsw.textTertiary,
                    fontSize = dsw.textSize,
                )
            } else {
                LazyColumn(
                    state = listState,
                    modifier = Modifier.fillMaxSize(),
                    verticalArrangement = Arrangement.spacedBy(FlowGap),
                ) {
                    itemsIndexed(rows) { index, row ->
                        // Per-row spacing overrides from the flow-gap contract.
                        val topGap = when {
                            index == 0 -> 0.dp
                            rows[index - 1] is ChatRow.ToolResult && row is ChatRow.Assistant -> ResponseGap
                            rows[index - 1] is ChatRow.Assistant -> ResponseGap
                            else -> FlowGap
                        }
                        Box(
                            Modifier.padding(top = if (index == 0) 16.dp else topGap)
                                .then(if (contentWidth != androidx.compose.ui.unit.Dp.Unspecified)
                                    Modifier.widthIn(max = contentWidth) else Modifier),
                        ) {
                            when (row) {
                                is ChatRow.User -> UserBubble(row.text)
                                is ChatRow.Assistant -> AssistantText(row.text)
                                is ChatRow.AssistantWithTools -> AssistantWithTools(row)
                                is ChatRow.ToolCall -> ProcessRow(row.name, row.arguments, pending = true)
                                is ChatRow.ToolResult -> ProcessRow(row.name, row.output, pending = false, error = row.error)
                                is ChatRow.Error -> ErrorRow(row.text)
                            }
                        }
                    }
                }
            }
        }
    }

    /**
     * The user message, MessageItem.module.css .userRow/.bubble: right
     * aligned, deepseek-50 fill, radius-xl 20px, padding 10x16, capped at
     * 70.2% of the column width.
     */
    @Composable
    private fun UserBubble(text: String) {
        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
            Surface(
                shape = RoundedCornerShape(20.dp),
                color = dsw.bubble,
                modifier = Modifier.widthIn(max = 280.dp),
            ) {
                Text(
                    text,
                    color = dsw.textPrimary,
                    fontSize = dsw.textSize,
                    lineHeight = 22.sp,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
            }
        }
    }

    /** The assistant reply: no bubble — bare text on the column (AssistantMarkdown). */
    @Composable
    private fun AssistantText(text: String) {
        Text(
            text,
            color = dsw.textPrimary,
            fontSize = dsw.textSize,
            lineHeight = 22.sp,
            modifier = Modifier.fillMaxWidth(),
        )
    }

    /** Assistant text followed by the process rows its turn produced. */
    @Composable
    private fun AssistantWithTools(row: ChatRow.AssistantWithTools) {
        Column {
            if (row.text.isNotEmpty()) AssistantText(row.text)
        }
    }

    /**
     * The tool process row (TurnProcessNodeView .root): NOT a card — a 33px
     * disclosure line with a 0.5px bottom border in border-l2, tertiary
     * label, chevron that flips when open.
     */
    @Composable
    private fun ProcessRow(name: String, body: String, pending: Boolean, error: String = "") {
        var expanded by remember { mutableStateOf(false) }
        Column(modifier = Modifier.fillMaxWidth()) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(6.dp),
                modifier = Modifier.fillMaxWidth()
                    .heightIn(min = 33.dp)
                    .border(0.5.dp, dsw.borderL2, RectangleShape)
                    .clickable { expanded = !expanded }
                    .padding(horizontal = 4.dp),
            ) {
                Text(
                    if (expanded) "▾" else "▸",
                    color = dsw.textTertiary,
                    fontSize = dsw.textSize,
                )
                Text(
                    name,
                    color = if (error.isNotEmpty()) dsw.danger else dsw.textTertiary,
                    fontSize = dsw.textSize,
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
                    fontSize = dsw.textSize,
                )
            }
            if (expanded && body.isNotEmpty()) {
                Text(
                    body,
                    color = dsw.textSecondary,
                    fontSize = dsw.codeSize,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier.fillMaxWidth().padding(top = 6.dp, start = 20.dp),
                )
            }
        }
    }

    @Composable
    private fun ErrorRow(text: String) {
        Text(
            text,
            color = dsw.danger,
            fontSize = dsw.textSize,
            modifier = Modifier.fillMaxWidth(),
        )
    }

    /** The to-bottom float (ChatView .toBottom): a 34px round button. */
    @Composable
    private fun ToBottomButton(onClick: () -> Unit) {
        Box(modifier = Modifier.fillMaxSize()) {
            Surface(
                shape = RoundedCornerShape(100.dp),
                color = dsw.bgLayer1,
                shadowElevation = 2.dp,
                modifier = Modifier.align(Alignment.BottomEnd)
                    .padding(end = 16.dp, bottom = 16.dp)
                    .size(34.dp)
                    .clickable(onClick = onClick),
            ) {
                Box(contentAlignment = Alignment.Center) {
                    Text("↓", color = dsw.textPrimary, fontSize = dsw.textSize)
                }
            }
        }
    }

    /**
     * The composer card (InputBar.module.css .card): radius-panel 28px,
     * fill specific-input-major, hairline stroke border-l2 with the soft
     * elevation, 8px top pad, 12px gap between text area and button row,
     * side clearance 32px.
     */
    @Composable
    private fun Composer(contentWidth: androidx.compose.ui.unit.Dp) {
        var text by remember { mutableStateOf("") }
        Column(
            modifier = Modifier.fillMaxWidth().padding(horizontal = Width.composerSideClearance * 2, vertical = 4.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Column(
                modifier = Modifier.fillMaxWidth().widthIn(max = contentWidth)
                    .background(dsw.inputMajor, RoundedCornerShape(28.dp))
                    .border(0.5.dp, dsw.borderL2, RoundedCornerShape(28.dp))
                    .padding(top = 8.dp, bottom = 8.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                TextField(
                    value = text,
                    onValueChange = { text = it },
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
                    placeholder = { Text("Message", color = dsw.textTertiary, fontSize = dsw.textSize) },
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
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp),
                    horizontalArrangement = Arrangement.End,
                ) {
                    Button(
                        onClick = {
                            send(text)
                            text = ""
                        },
                        enabled = text.isNotBlank() && !turnRunning,
                        shape = RoundedCornerShape(100.dp),
                        contentPadding = androidx.compose.foundation.layout.PaddingValues(
                            horizontal = 20.dp, vertical = 6.dp),
                        colors = ButtonDefaults.buttonColors(
                            containerColor = dsw.buttonPrimaryFill,
                            contentColor = dsw.textInverted,
                            disabledContainerColor = dsw.bgLayer2,
                            disabledContentColor = dsw.textTertiary,
                        ),
                    ) {
                        Text(if (turnRunning) "…" else "Send", fontSize = dsw.textSize)
                    }
                }
            }
        }
    }

    @OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
    @Composable
    private fun SettingsSheet(current: Config, onSave: (Config) -> Unit) {
        var baseUrl by remember { mutableStateOf(current.baseUrl) }
        var apiKey by remember { mutableStateOf(current.apiKey) }
        var model by remember { mutableStateOf(current.model) }
        androidx.compose.material3.ModalBottomSheet(
            onDismissRequest = { onSave(current) },
            containerColor = dsw.bgLayer1,
        ) {
            Column(
                modifier = Modifier.fillMaxWidth().padding(16.dp),
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                Text("Settings", fontSize = dsw.titleSize, color = dsw.textPrimary)
                LabeledField("Base URL", baseUrl) { baseUrl = it }
                LabeledField("API key", apiKey) { apiKey = it }
                LabeledField("Model", model) { model = it }
                Button(
                    onClick = { onSave(Config(baseUrl.trim(), apiKey.trim(), model.trim())) },
                    modifier = Modifier.fillMaxWidth(),
                    shape = RoundedCornerShape(12.dp),
                    colors = ButtonDefaults.buttonColors(
                        containerColor = dsw.buttonPrimaryFill,
                        contentColor = dsw.textInverted,
                    ),
                ) { Text("Save", fontSize = dsw.textSize) }
                Box(Modifier.navigationBarsPadding())
            }
        }
    }

    @Composable
    private fun LabeledField(label: String, value: String, onChange: (String) -> Unit) {
        Column {
            Text(label, fontSize = dsw.labelSize, color = dsw.textSecondary)
            TextField(
                value = value,
                onValueChange = onChange,
                singleLine = true,
                shape = RoundedCornerShape(12.dp),
                colors = TextFieldDefaults.colors(
                    focusedContainerColor = dsw.bgLayer2,
                    unfocusedContainerColor = dsw.bgLayer2,
                    focusedIndicatorColor = dsw.brandPrimary,
                    unfocusedIndicatorColor = dsw.borderL2,
                    cursorColor = dsw.brandPrimary,
                    focusedTextColor = dsw.textPrimary,
                    unfocusedTextColor = dsw.textPrimary,
                ),
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

/** One rendered row of the conversation. */
sealed class ChatRow {
    data class User(val text: String) : ChatRow()
    data class Assistant(val text: String) : ChatRow()
    data class AssistantWithTools(val text: String, val toolNames: List<String>) : ChatRow()
    data class ToolCall(val name: String, val arguments: String) : ChatRow()
    data class ToolResult(val name: String, val output: String, val error: String) : ChatRow()
    data class Error(val text: String) : ChatRow()
}

/** Connection settings for turns, persisted in SharedPreferences. */
data class Config(
    val baseUrl: String,
    val apiKey: String,
    val model: String,
) {
    fun save(context: Context) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString("baseUrl", baseUrl)
            .putString("apiKey", apiKey)
            .putString("model", model)
            .apply()
    }

    companion object {
        private const val PREFS = "qrpdsh"

        val EMPTY = Config("", "", "")

        fun load(context: Context): Config {
            val p = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            return Config(
                baseUrl = p.getString("baseUrl", "https://api.deepseek.com/v1") ?: "",
                apiKey = p.getString("apiKey", "") ?: "",
                model = p.getString("model", "deepseek-chat") ?: "",
            )
        }
    }
}
