package com.sta1n.qrpdsh

import android.content.Context
import android.os.Bundle
import android.util.Log
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.navigationBars
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.statusBars
import androidx.compose.foundation.layout.windowInsetsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
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
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.lifecycleScope
import com.sta1n.qrpdsh.ui.Dsw
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

    @Composable
    private fun ChatSurface() {
        Surface(modifier = Modifier.fillMaxSize(), color = dsw.bgBase) {
            Column(modifier = Modifier.fillMaxSize().imePadding().navigationBarsPadding()) {
                TopBar(onSettings = { showSettings = true })
                Transcript(rows, Modifier.weight(1f))
                Composer()
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

    @Composable
    private fun Transcript(rows: List<ChatRow>, modifier: Modifier = Modifier) {
        val listState = rememberLazyListState()
        // Follow the tail while new rows land, the DSH scroll contract: a
        // streaming transcript stays pinned to the bottom until the user
        // scrolls away.
        LaunchedEffect(rows.size) {
            if (rows.isNotEmpty()) listState.animateScrollToItem(rows.size - 1)
        }
        Box(modifier = modifier.fillMaxWidth()) {
            if (rows.isEmpty()) {
                Text(
                    "No messages yet. Set the API key in settings, then send a message.",
                    modifier = Modifier.align(Alignment.Center).padding(24.dp),
                    color = dsw.textTertiary,
                    fontSize = dsw.textSize,
                )
            } else {
                LazyColumn(
                    state = listState,
                    modifier = Modifier.fillMaxSize(),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                ) {
                    items(rows) { row ->
                        when (row) {
                            is ChatRow.User -> Bubble(row.text, user = true)
                            is ChatRow.Assistant -> Bubble(row.text, user = false)
                            is ChatRow.AssistantWithTools -> AssistantToolRow(row)
                            is ChatRow.ToolCall -> ToolCard(row.name, row.arguments, pending = true)
                            is ChatRow.ToolResult -> ToolCard(row.name, row.output, pending = false, error = row.error)
                            is ChatRow.Error -> Bubble("error: " + row.text, user = false, error = true)
                        }
                    }
                }
            }
        }
    }

    /** One transcript bubble: right-aligned brand bubble for the user. */
    @Composable
    private fun Bubble(text: String, user: Boolean, error: Boolean = false) {
        val bg = when {
            error -> dsw.diffDeleted
            user -> dsw.buttonPrimaryFill
            else -> dsw.bgLayer1
        }
        val fg = if (user) dsw.textInverted else dsw.textPrimary
        Box(modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
            Surface(
                shape = RoundedCornerShape(12.dp),
                color = bg,
                modifier = Modifier.widthIn(max = 320.dp)
                    .align(if (user) Alignment.CenterEnd else Alignment.CenterStart),
            ) {
                Text(
                    text,
                    color = fg,
                    fontSize = dsw.textSize,
                    modifier = Modifier.padding(10.dp),
                )
            }
        }
    }

    /** Assistant message plus the tool names it asked to run. */
    @Composable
    private fun AssistantToolRow(row: ChatRow.AssistantWithTools) {
        Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
            if (row.text.isNotEmpty()) {
                Text(row.text, color = dsw.textPrimary, fontSize = dsw.textSize)
            }
            Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                row.toolNames.forEach { name -> ToolPill(name) }
            }
        }
    }

    @Composable
    private fun ToolPill(name: String) {
        Surface(
            shape = RoundedCornerShape(6.dp),
            color = dsw.bgLayer2,
        ) {
            Text(
                name,
                color = dsw.textSecondary,
                fontSize = dsw.labelSize,
                modifier = Modifier.padding(horizontal = 8.dp, vertical = 3.dp),
            )
        }
    }

    /**
     * A tool card, the DisclosureRow shape: header row with the tool name,
     * expandable monospace body for arguments or output.
     */
    @Composable
    private fun ToolCard(name: String, body: String, pending: Boolean, error: String = "") {
        var expanded by remember { mutableStateOf(false) }
        Column(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp)
                .background(dsw.bgLayer1, RoundedCornerShape(12.dp))
                .border(1.dp, dsw.borderL1, RoundedCornerShape(12.dp)),
        ) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.SpaceBetween,
                modifier = Modifier.fillMaxWidth().padding(horizontal = 10.dp, vertical = 8.dp),
            ) {
                Text(
                    name,
                    color = dsw.textPrimary,
                    fontSize = dsw.labelSize,
                    fontFamily = FontFamily.Monospace,
                )
                Text(
                    when {
                        error.isNotEmpty() -> "error"
                        pending -> "running"
                        else -> "done"
                    },
                    color = if (error.isNotEmpty()) dsw.danger else dsw.textTertiary,
                    fontSize = dsw.labelSize,
                )
            }
            if (expanded) {
                Text(
                    body,
                    color = dsw.textSecondary,
                    fontSize = dsw.codeSize,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier.padding(horizontal = 10.dp).padding(bottom = 8.dp),
                )
            }
            Text(
                if (expanded) "▾ hide" else "▸ show",
                color = dsw.textTertiary,
                fontSize = dsw.labelSize,
                modifier = Modifier
                    .padding(horizontal = 10.dp)
                    .padding(bottom = 8.dp),
            )
        }
        // Toggle needs to be clickable; keep the simple gesture surface.
        Box(Modifier.padding(horizontal = 12.dp)) {}
    }

    @Composable
    private fun Composer() {
        var text by remember { mutableStateOf("") }
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 8.dp),
            verticalAlignment = Alignment.Bottom,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            TextField(
                value = text,
                onValueChange = { text = it },
                modifier = Modifier.weight(1f),
                placeholder = { Text("Message", color = dsw.textTertiary, fontSize = dsw.textSize) },
                maxLines = 5,
                shape = RoundedCornerShape(12.dp),
                colors = TextFieldDefaults.colors(
                    focusedContainerColor = dsw.bgLayer1,
                    unfocusedContainerColor = dsw.bgLayer1,
                    focusedIndicatorColor = Color.Transparent,
                    unfocusedIndicatorColor = Color.Transparent,
                    cursorColor = dsw.brandPrimary,
                    focusedTextColor = dsw.textPrimary,
                    unfocusedTextColor = dsw.textPrimary,
                ),
            )
            Button(
                onClick = {
                    send(text)
                    text = ""
                },
                enabled = text.isNotBlank() && !turnRunning,
                shape = RoundedCornerShape(12.dp),
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
