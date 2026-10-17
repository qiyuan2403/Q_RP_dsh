package com.sta1n.qrpdsh

import android.content.Context
import android.os.Bundle
import android.os.SystemClock
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.safeDrawing
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Clear
import androidx.compose.material.icons.filled.Menu
import androidx.compose.material.icons.automirrored.filled.Send
import androidx.compose.material.icons.filled.Settings
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.DrawerValue
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.ModalDrawerSheet
import androidx.compose.material3.ModalNavigationDrawer
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.rememberDrawerState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.theme.DswTheme
import com.sta1n.qrpdsh.ui.theme.LocalDsw
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

private const val SETTINGS_NAME = "connection"
private const val KEY_BASE_URL = "baseUrl"
private const val KEY_API_KEY = "apiKey"
private const val KEY_MODEL = "model"
private const val KEY_SEARCH_API_KEY = "searchApiKey"

// The stream arrives as one pollEvent per few milliseconds. Flushing the
// accumulated text on a 50ms clock keeps composition off the token path.
private const val LIVE_FLUSH_MS = 50L
private const val POLL_TIMEOUT_MS = 100

// An empty poll means either "nothing arrived in this window" or "the stream is
// closed"; both return immediately. Pausing between empty reads keeps the poll
// from turning into a tight JNI loop while the turn winds down. The delay is
// cancellable, so stopping the turn still releases the poll at once.
private const val EMPTY_POLL_DELAY_MS = 20L
private const val BOTTOM_SCROLL_OFFSET = 100_000

/**
 * The connection settings. [searchApiKey] is separate from the model key
 * because web search may be disabled or pointed at another provider; an
 * empty value must not stop normal chat.
 */
private data class ConnectionSettings(
    val baseUrl: String,
    val apiKey: String,
    val model: String,
    val searchApiKey: String = "",
)

data class SessionRow(val id: String, val title: String, val blank: Boolean, val running: Boolean, val updatedAt: Long)

private data class TurnResult(val error: String?, val replay: Replay, val rows: List<SessionRow>?)

private fun loadSettings(context: Context): ConnectionSettings {
    val prefs = context.getSharedPreferences(SETTINGS_NAME, Context.MODE_PRIVATE)
    return ConnectionSettings(
        prefs.getString(KEY_BASE_URL, "https://api.deepseek.com/v1") ?: "",
        prefs.getString(KEY_API_KEY, "") ?: "",
        prefs.getString(KEY_MODEL, "deepseek-chat") ?: "deepseek-chat",
        prefs.getString(KEY_SEARCH_API_KEY, "") ?: "",
    )
}

private fun saveSettings(context: Context, settings: ConnectionSettings) {
    context.getSharedPreferences(SETTINGS_NAME, Context.MODE_PRIVATE).edit()
        .putString(KEY_BASE_URL, settings.baseUrl.trim())
        .putString(KEY_API_KEY, settings.apiKey)
        .putString(KEY_MODEL, settings.model.trim())
        .putString(KEY_SEARCH_API_KEY, settings.searchApiKey)
        .apply()
}

private fun parseSessions(raw: String): List<SessionRow> = runCatching {
    val array = JSONArray(raw)
    List(array.length()) { index ->
        val row = array.getJSONObject(index)
        SessionRow(row.optString("id"), row.optString("title"), row.optBoolean("blank"), row.optBoolean("running"), row.optLong("updatedAt"))
    }
}.getOrDefault(emptyList())

/**
 * The status text for a finished turn. A turn can end without an error string
 * (cancelled, step cap) and a blank status line leaves the user wondering
 * whether the request failed; the reason is the only explanation there is.
 */
private fun turnErrorText(response: JSONObject): String? {
    response.optString("error").takeIf(String::isNotBlank)?.let { return it }
    if (response.optBoolean("ok", false)) return null
    return when (val reason = response.optString("reason")) {
        "" -> null
        "cancelled" -> "已取消"
        "max-steps" -> "达到步数上限，回合未完成"
        "model-error" -> "模型调用失败"
        "deadline" -> "请求超时"
        else -> "回合结束：$reason"
    }
}

/**
 * What a viewport re-anchor is allowed to depend on. The session id is part of
 * it so switching conversations scrolls to the new transcript even when both
 * sessions happen to have the same row count.
 */
private data class AutoScrollSnapshot(
    val sessionId: String?,
    val rows: Int,
    val liveLength: Int,
    val liveTool: String?,
    val liveActive: Boolean,
    val busy: Boolean,
)

/**
 * Auto-follows growing content, isolated from the screen that owns the
 * transcript.
 *
 * WHY THIS IS ITS OWN COMPOSABLE. The snapshot lambda reads the live text
 * states while *this* scope composes, so a 50ms chunk invalidates only this
 * tiny function and the streaming row. Reading them in DshApp would recompose
 * the drawer, toolbar and every visible message on each flush.
 */
@Composable
private fun AutoScrollEffect(
    listState: LazyListState,
    snapshot: () -> AutoScrollSnapshot,
    stickToBottom: () -> Boolean,
    setStickToBottom: (Boolean) -> Unit,
) {
    val signal = snapshot()

    // Follow only while the user is parked near the bottom. Up-scroll turns
    // following off until the user returns to the end.
    LaunchedEffect(listState) {
        snapshotFlow { listState.isScrollInProgress }
            .distinctUntilChanged()
            .collect { scrolling ->
                if (!scrolling) setStickToBottom(listState.isNearBottom())
            }
    }

    LaunchedEffect(listState, signal) {
        if (!stickToBottom() || listState.isScrollInProgress) return@LaunchedEffect
        val streaming = signal.busy || signal.liveActive
        val lastIndex = signal.rows + if (streaming) 1 else 0 - 1
        if (lastIndex < 0) return@LaunchedEffect
        if (streaming) {
            // Instant and clamped to the end: an animation restarted every
            // 50ms would never settle while tokens flow.
            listState.scrollToItem(lastIndex, BOTTOM_SCROLL_OFFSET)
        } else {
            listState.animateScrollToItem(lastIndex, BOTTOM_SCROLL_OFFSET)
        }
    }
}

/**
 * Whether the transcript is close enough to its end that appending content
 * should move the viewport. The slack absorbs a partially visible last row.
 */
private fun LazyListState.isNearBottom(slackPx: Int = 120): Boolean {
    val info = layoutInfo
    val last = info.visibleItemsInfo.lastOrNull() ?: return true
    if (last.index < info.totalItemsCount - 1) return false
    return last.offset + last.size - info.viewportEndOffset <= slackPx
}

class ChatActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val home = filesDir.absolutePath
        setContent { DswTheme { DshApp(home) } }
    }
}

@Composable
private fun DshApp(home: String) {
    val context = LocalContext.current
    val activity = context as ChatActivity
    val dsw = LocalDsw.current
    // Every action below crosses JNI. Without the library the first call would
    // throw UnsatisfiedLinkError on a coroutine and take the process down, so
    // the screen stops here and reports the load failure the bridge recorded.
    if (!Native.isLoaded()) {
        NativeMissing(dsw)
        return
    }
    val scope = rememberCoroutineScope()
    val drawer = rememberDrawerState(DrawerValue.Closed)
    var settings by remember { mutableStateOf(loadSettings(context)) }
    var showSettings by remember { mutableStateOf(false) }
    var sessions by remember { mutableStateOf(emptyList<SessionRow>()) }
    var selected by remember { mutableStateOf<SessionRow?>(null) }
    var messages by remember { mutableStateOf(emptyList<ChatNode>()) }
    var input by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var status by remember { mutableStateOf<String?>(null) }

    // The negative seq keeps an optimistic bubble's key distinct from durable
    // seqs (which start at 1). Each send takes the next one.
    var optimisticSeq by remember { mutableStateOf(-1L) }

    // The live segment is deliberately separate from [messages]: appending a
    // chunk must invalidate one fixed item, not the durable transcript.
    var liveText by remember { mutableStateOf("") }
    var liveTool by remember { mutableStateOf<String?>(null) }
    var liveActive by remember { mutableStateOf(false) }

    // Every session switch or new turn bumps this counter. A late result from
    // the previous turn sees a mismatch and leaves the new screen alone.
    var turnEpoch by remember { mutableStateOf(0) }
    var cancelRequested by remember { mutableStateOf(false) }
    var stickToBottom by remember { mutableStateOf(true) }
    // A fresh scroll state per session: the previous session's position (and
    // any in-flight scroll) must not leak into the next conversation.
    val listState = remember(selected?.id) { LazyListState() }

    // Rotation or a system theme change destroys this composition while the Go
    // turn keeps running on the JNI thread the coroutine blocked. Cancelling on
    // dispose prevents the rebuilt screen from starting a second turn on the
    // same session; the next refresh replays whatever the log already holds.
    // rememberUpdatedState is what keeps onDispose reading the current session
    // instead of the one captured when the effect was installed.
    val latestSessionId by rememberUpdatedState(selected?.id)
    val latestBusy by rememberUpdatedState(busy)
    DisposableEffect(Unit) {
        onDispose {
            val sessionId = latestSessionId
            if (latestBusy && sessionId != null) {
                runCatching { Native.cancelTurn(sessionId) }
            }
        }
    }

    fun resetLive() {
        liveText = ""
        liveTool = null
        liveActive = false
    }

    /**
     * Poll the session while runTurn blocks on the JNI thread. Text deltas are
     * accumulated off the UI thread and published on a 50ms clock; the
     * durable replay after runTurn remains the source of truth.
     */
    suspend fun pollStream(sessionId: String, epoch: Int) {
        val buffer = StringBuilder()
        var streamTool: String? = null
        var streamActive = false
        var dirty = false
        var lastFlush = 0L

        suspend fun publish(force: Boolean) {
            if (!dirty && !force) return
            val now = SystemClock.elapsedRealtime()
            if (!force && now - lastFlush < LIVE_FLUSH_MS) return
            lastFlush = now
            dirty = false
            val text = buffer.toString()
            val tool = streamTool
            val active = streamActive
            withContext(Dispatchers.Main) {
                if (epoch != turnEpoch) return@withContext
                liveText = text
                liveTool = tool
                liveActive = active
            }
        }

        while (currentCoroutineContext().isActive) {
            val raw = try {
                Native.pollEvent(sessionId, POLL_TIMEOUT_MS)
            } catch (error: Throwable) {
                // A broken poll must not take the turn down with it: the
                // durable replay after runTurn still delivers the result.
                break
            }
            if (raw.isEmpty()) {
                // Also flushes a chunk that arrived just under the throttle.
                publish(false)
                // An empty read also means the stream is already closed while
                // runTurn is still returning; without the pause this branch
                // spins a JNI call every few microseconds and leaks the Go
                // side's per-call buffers until the turn ends.
                delay(EMPTY_POLL_DELAY_MS)
                continue
            }
            val event = runCatching { JSONObject(raw) }.getOrNull() ?: continue
            if (event.optString("type") != "agent/assistant-stream") {
                // Durable events are facts from the session log. Folding them
                // in now is what makes the user's own message and each tool
                // step visible during the turn instead of only after the
                // final replay. The replay after runTurn still replaces the
                // whole list, so these increments cannot duplicate rows.
                withContext(Dispatchers.Main) {
                    if (epoch != turnEpoch) return@withContext
                    messages = appendEvent(messages, event)
                }
                continue
            }
            val frame = event.optJSONObject("data") ?: continue
            when (frame.optString("type")) {
                "start" -> {
                    buffer.setLength(0)
                    streamTool = null
                    streamActive = true
                    dirty = true
                    publish(true)
                }

                "chunk" -> {
                    val chunk = frame.optJSONObject("chunk")
                    if (chunk != null) {
                        val delta = chunk.optString("textDelta")
                        if (delta.isNotEmpty()) buffer.append(delta)
                        val toolDelta = chunk.optJSONObject("toolCallDelta")
                        if (toolDelta != null) {
                            val name = toolDelta.optString("name")
                            if (name.isNotEmpty()) streamTool = name
                            else if (streamTool == null) streamTool = "工具"
                        }
                        dirty = true
                        publish(false)
                    }
                }

                "end" -> {
                    val outcome = frame.optJSONObject("outcome")?.optString("kind").orEmpty()
                    // Committed: the durable assistant/message is the copy to
                    // keep, so the live segment must not show it again. The
                    // message event may reach the merged channel just after
                    // this frame; the replay after runTurn covers that gap.
                    // Abandoned: no durable message follows, so dropping the
                    // live text is the correct final state.
                    if (outcome == "abandoned" || outcome == "committed") {
                        buffer.setLength(0)
                        streamTool = null
                    }
                    streamActive = false
                    dirty = true
                    publish(true)
                }
            }
        }
    }

    fun openSession(row: SessionRow) {
        val previous = selected
        val previousId = previous?.id
        val wasBusy = busy
        turnEpoch += 1
        val epoch = turnEpoch
        busy = false
        cancelRequested = false
        resetLive()
        selected = row
        stickToBottom = true
        if (wasBusy && previousId != null) {
            // The old turn would otherwise keep burning time against a screen
            // the user already left; its completion is ignored by the epoch.
            scope.launch(Dispatchers.IO) { runCatching { Native.cancelTurn(previousId) } }
        }
        scope.launch(Dispatchers.IO) {
            runCatching { parseReplay(Native.openSessionId(home, row.id)) }
                .fold(
                    onSuccess = { replay ->
                        withContext(Dispatchers.Main) {
                            if (epoch != turnEpoch) return@withContext
                            if (replay.id.isEmpty()) {
                                // An error document instead of a session: keep
                                // the old transcript and its selection rather
                                // than blanking the screen under a new title.
                                selected = previous
                                status = replay.error ?: "打开会话失败"
                            } else {
                                messages = replay.nodes
                                status = replay.error
                            }
                        }
                    },
                    onFailure = { error ->
                        // A missing JNI symbol must not kill the process or
                        // leave another session's transcript under this title;
                        // revert the selection and keep the rows that were
                        // already on screen.
                        withContext(Dispatchers.Main) {
                            if (epoch != turnEpoch) return@withContext
                            selected = previous
                            status = "打开会话失败：${error.message ?: "未知错误"}"
                        }
                    },
                )
        }
    }

    fun refresh() {
        val epoch = turnEpoch
        val openNewest = selected == null
        scope.launch(Dispatchers.IO) {
            // The list and the replay are fetched separately so a failed open
            // still leaves the drawer with the rows it just read.
            val rows = runCatching { parseSessions(Native.listSessions(home)) }
            val replay = if (openNewest) runCatching { parseReplay(Native.openSession(home)) } else null
            withContext(Dispatchers.Main) {
                if (epoch != turnEpoch) return@withContext
                rows.getOrNull()?.let { sessions = it }
                replay?.getOrNull()?.takeIf { it.id.isEmpty() }?.error?.let { status = it }
                replay?.getOrNull()?.takeIf { it.id.isNotEmpty() }?.let { restored ->
                    selected = sessions.firstOrNull { it.id == restored.id }
                        ?: SessionRow(restored.id, "", true, false, 0L)
                    messages = restored.nodes
                    status = restored.error
                    stickToBottom = true
                }
                // Keep the previous sidebar on failure instead of blanking it.
                val failure = rows.exceptionOrNull() ?: replay?.exceptionOrNull()
                if (failure != null) {
                    status = "无法读取会话：${failure.message ?: "未知错误"}"
                }
            }
        }
    }

    fun createSession() {
        val previous = selected
        val previousId = previous?.id
        val wasBusy = busy
        turnEpoch += 1
        val epoch = turnEpoch
        busy = false
        cancelRequested = false
        resetLive()
        if (wasBusy && previousId != null) {
            scope.launch(Dispatchers.IO) { runCatching { Native.cancelTurn(previousId) } }
        }
        scope.launch(Dispatchers.IO) {
            runCatching {
                val replay = parseReplay(Native.createSession(home))
                // The new session is usable even when the list refresh fails;
                // that only means the drawer lags one row behind.
                val rows = runCatching { parseSessions(Native.listSessions(home)) }.getOrNull()
                replay to rows
            }.fold(
                onSuccess = { (replay, rows) ->
                    withContext(Dispatchers.Main) {
                        if (epoch != turnEpoch) return@withContext
                        if (replay.id.isNotEmpty()) {
                            selected = SessionRow(replay.id, "", true, false, System.currentTimeMillis())
                            rows?.let { sessions = it }
                            messages = replay.nodes
                            status = replay.error
                            stickToBottom = true
                        } else {
                            status = replay.error
                        }
                        scope.launch { drawer.close() }
                    }
                },
                onFailure = { error ->
                    withContext(Dispatchers.Main) {
                        if (epoch != turnEpoch) return@withContext
                        selected = previous
                        status = "新建会话失败：${error.message ?: "未知错误"}"
                        scope.launch { drawer.close() }
                    }
                },
            )
        }
    }

    fun send() {
        val task = input.trim()
        val sessionId = selected?.id ?: return
        if (task.isEmpty() || busy) return
        val current = settings
        if (current.baseUrl.isBlank() || current.model.isBlank()) {
            status = "请先配置 API 地址和模型"
            showSettings = true
            return
        }
        input = ""
        // The durable user/message only reaches the screen after a JNI round
        // trip, and the replay is not refreshed until the turn ends. Appending
        // the bubble here keeps the composer honest; appendEvent replaces it
        // with the logged row when the poll sees it.
        messages = messages + UserMessage(optimisticSeq, task)
        optimisticSeq -= 1
        busy = true
        status = null
        cancelRequested = false
        resetLive()
        stickToBottom = true
        turnEpoch += 1
        val epoch = turnEpoch
        scope.launch(Dispatchers.IO) {
            val result = runCatching {
                val nodePath = runCatching { NodeRuntime.materialize(activity) }.getOrNull()
                val poll = launch { pollStream(sessionId, epoch) }
                val response = try {
                    // A stop tapped while the node runtime was still
                    // materializing has no running turn to cancel; honor it
                    // here instead of starting the turn anyway.
                    if (cancelRequested) {
                        JSONObject()
                    } else {
                        JSONObject(
                            Native.runTurn(
                                task, home, home,
                                current.baseUrl, current.apiKey, current.model,
                                nodePath, sessionId, current.searchApiKey,
                            )
                        )
                    }
                } finally {
                    // pollEvent blocks for at most POLL_TIMEOUT_MS, so this
                    // join is bounded and the poll never outlives the turn.
                    poll.cancelAndJoin()
                }
                // A missing library or a broken replay must not throw past the
                // fold below; it would skip the busy reset and strand the UI.
                val replay = parseReplay(Native.openSessionId(home, sessionId))
                // Keep the old sidebar rows if this refresh fails.
                val rows = runCatching { parseSessions(Native.listSessions(home)) }.getOrNull()
                TurnResult(turnErrorText(response), replay, rows)
            }
            withContext(Dispatchers.Main) {
                if (epoch != turnEpoch) return@withContext
                busy = false
                resetLive()
                result.fold(
                    onSuccess = { turn ->
                        // An error document has no session id; replacing the
                        // transcript with its empty node list would erase the
                        // rows the live fold already showed.
                        if (turn.replay.id.isNotEmpty()) messages = turn.replay.nodes
                        turn.rows?.let { sessions = it }
                        status = when {
                            cancelRequested -> "已取消"
                            turn.replay.error != null -> turn.replay.error
                            else -> turn.error
                        }
                    },
                    onFailure = { error ->
                        // The turn never reached a durable replay, so the
                        // optimistic echo is not in the log: drop it instead
                        // of leaving a message that cannot survive a reload.
                        messages = messages.filterNot { it is UserMessage && it.seq < 0 }
                        status = error.message ?: "请求失败"
                    },
                )
                cancelRequested = false
            }
        }
    }

    fun cancel() {
        if (!busy) return
        val sessionId = selected?.id ?: return
        cancelRequested = true
        status = "正在取消"
        scope.launch(Dispatchers.IO) { runCatching { Native.cancelTurn(sessionId) } }
    }

    LaunchedEffect(Unit) { refresh() }

    if (showSettings) {
        ConnectionDialog(settings, dsw, onDismiss = { showSettings = false }) { updated ->
            saveSettings(context, updated)
            settings = updated
            showSettings = false
            status = null
        }
    }

    ModalNavigationDrawer(
        drawerState = drawer,
        drawerContent = {
            ModalDrawerSheet(drawerContainerColor = dsw.sidebarFill) {
                Row(Modifier.fillMaxWidth().padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text("会话", fontSize = 18.sp, fontWeight = FontWeight.SemiBold, color = dsw.textPrimary)
                    Spacer(Modifier.weight(1f))
                    IconButton(onClick = { showSettings = true }) { Icon(Icons.Default.Settings, "设置", tint = dsw.textPrimary) }
                    IconButton(onClick = { createSession() }) { Icon(Icons.Default.Add, "新建", tint = dsw.textPrimary) }
                }
                LazyColumn(contentPadding = PaddingValues(horizontal = 8.dp, vertical = 4.dp)) {
                    items(sessions, key = { it.id }) { row ->
                        val active = selected?.id == row.id
                        Row(
                            Modifier.fillMaxWidth().height(32.dp)
                                .background(if (active) dsw.interactiveHover else Color.Transparent, RoundedCornerShape(8.dp))
                                .clickable { openSession(row); scope.launch { drawer.close() } }
                                .padding(horizontal = 8.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            if (row.running) Box(Modifier.size(7.dp).background(Color(0xFF22C55E), RoundedCornerShape(4.dp))) else Spacer(Modifier.size(7.dp))
                            Spacer(Modifier.width(8.dp))
                            Text(if (row.blank) "新会话" else row.title.ifBlank { "未命名" }, maxLines = 1, color = dsw.textPrimary, fontSize = 13.sp)
                        }
                    }
                }
            }
        },
    ) {
        Surface(Modifier.fillMaxSize(), color = dsw.bgBase) {
            // Edge-to-edge is enforced at targetSdk 35: without consuming the
            // insets the composer sits under the navigation bar and the top bar
            // under the status bar. safeDrawing covers system bars, cutout and
            // IME in one padding, so the keyboard cannot double it.
            Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.safeDrawing)) {
                Row(Modifier.fillMaxWidth().height(56.dp).padding(horizontal = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    IconButton(onClick = { scope.launch { drawer.open() } }) { Icon(Icons.Default.Menu, "会话", tint = dsw.textPrimary) }
                    Text(selected?.title?.ifBlank { "新会话" } ?: "新会话", color = dsw.textPrimary, fontSize = 16.sp, fontWeight = FontWeight.Medium)
                    Spacer(Modifier.weight(1f))
                    IconButton(onClick = { showSettings = true }) { Icon(Icons.Default.Settings, "设置", tint = dsw.textPrimary) }
                }
                if (status != null) Text(status!!, Modifier.fillMaxWidth().padding(horizontal = 16.dp), color = dsw.textSecondary, fontSize = 12.sp)
                LazyColumn(
                    Modifier.weight(1f).fillMaxWidth(),
                    state = listState,
                    contentPadding = PaddingValues(16.dp, 16.dp, 16.dp, 24.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp),
                ) {
                    items(messages, key = { it.key }) { node ->
                        when (node) {
                            is UserMessage -> UserBubble(node.text, dsw)
                            is AssistantMessage -> MarkdownText(node.text, Modifier.fillMaxWidth())
                            is ToolNode -> ToolCard(node, dsw, turnActive = busy)
                        }
                    }
                    if (busy || liveActive) {
                        // Fixed key: growing live text reuses this item instead
                        // of producing a new one per chunk.
                        item(key = "streaming") {
                            StreamingBlock(liveText = liveText, liveTool = liveTool, busy = busy, dsw = dsw)
                        }
                    }
                }
                // Sits after the list so its effect starts in the same frame
                // the new content enters the item provider.
                AutoScrollEffect(
                    listState = listState,
                    snapshot = { AutoScrollSnapshot(selected?.id, messages.size, liveText.length, liveTool, liveActive, busy) },
                    stickToBottom = { stickToBottom },
                    setStickToBottom = { stickToBottom = it },
                )
                Composer(input, { input = it }, ::send, ::cancel, busy, dsw)
            }
        }
    }
}

/**
 * Shown when libqrpdsh failed to load. The bridge records the linker error
 * instead of throwing from its static initializer, so the app can explain the
 * failure instead of dying on the first JNI call.
 */
@Composable
private fun NativeMissing(dsw: Dsw) {
    Surface(Modifier.fillMaxSize(), color = dsw.bgBase) {
        Column(
            Modifier.fillMaxSize().padding(24.dp),
            verticalArrangement = Arrangement.Center,
        ) {
            Text("原生库加载失败", color = dsw.textPrimary, fontSize = 18.sp, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(8.dp))
            Text("聊天功能当前不可用，请重新安装应用；若问题仍在，请附带下面的信息反馈。", color = dsw.textSecondary, fontSize = 13.sp)
            Native.loadError()?.takeIf { it.isNotBlank() }?.let { detail ->
                Spacer(Modifier.height(12.dp))
                Text(detail, color = dsw.textTertiary, fontSize = 12.sp)
            }
        }
    }
}

@Composable
private fun ConnectionDialog(initial: ConnectionSettings, dsw: Dsw, onDismiss: () -> Unit, onSave: (ConnectionSettings) -> Unit) {
    var baseUrl by remember(initial) { mutableStateOf(initial.baseUrl) }
    var apiKey by remember(initial) { mutableStateOf(initial.apiKey) }
    var model by remember(initial) { mutableStateOf(initial.model) }
    var searchApiKey by remember(initial) { mutableStateOf(initial.searchApiKey) }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("模型连接", color = dsw.textPrimary) },
        text = {
            Column(
                Modifier.heightIn(max = 360.dp).verticalScroll(rememberScrollState()),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                OutlinedTextField(baseUrl, { baseUrl = it }, label = { Text("API 地址") }, singleLine = true)
                OutlinedTextField(apiKey, { apiKey = it }, label = { Text("API 密钥") }, singleLine = true, visualTransformation = PasswordVisualTransformation())
                OutlinedTextField(model, { model = it }, label = { Text("模型") }, singleLine = true)
                OutlinedTextField(
                    searchApiKey,
                    { searchApiKey = it },
                    label = { Text("搜索 API Key（可选）") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                )
            }
        },
        confirmButton = {
            TextButton(onClick = { onSave(ConnectionSettings(baseUrl, apiKey, model, searchApiKey)) }) { Text("保存") }
        },
        dismissButton = { TextButton(onClick = onDismiss) { Text("取消") } },
    )
}

@Composable
private fun Composer(
    value: String,
    onValue: (String) -> Unit,
    send: () -> Unit,
    cancel: () -> Unit,
    busy: Boolean,
    dsw: Dsw,
) {
    Row(Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 12.dp), verticalAlignment = Alignment.Bottom) {
        OutlinedTextField(
            value = value,
            onValueChange = onValue,
            modifier = Modifier.weight(1f),
            placeholder = { Text("输入消息", color = dsw.textTertiary) },
            shape = RoundedCornerShape(28.dp),
            maxLines = 5,
        )
        Spacer(Modifier.width(8.dp))
        if (busy) {
            IconButton(onClick = cancel) { Icon(Icons.Default.Clear, "停止", tint = dsw.danger) }
        } else {
            IconButton(onClick = send, enabled = value.isNotBlank()) {
                Icon(Icons.AutoMirrored.Filled.Send, "发送", tint = dsw.brandPrimary)
            }
        }
    }
}
