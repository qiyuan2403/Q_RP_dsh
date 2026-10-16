package com.sta1n.qrpdsh

import android.content.Context
import android.os.Bundle
import android.util.Log
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.material3.Text
import androidx.compose.material3.TextField
import androidx.compose.material3.TextButton
import androidx.compose.ui.unit.sp
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.ModalBottomSheet
import androidx.compose.runtime.remember
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.lifecycle.lifecycleScope
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.DswDark
import com.sta1n.qrpdsh.ui.DswLight
import com.sta1n.qrpdsh.ui.conversation.ConversationRoot
import com.sta1n.qrpdsh.ui.conversation.ChatView
import com.sta1n.qrpdsh.ui.layout.AppFrame
import com.sta1n.qrpdsh.ui.sidebar.SessionRowUi
import com.sta1n.qrpdsh.ui.sidebar.SidebarRoot
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject
import java.io.IOException

/**
 * The activity shell. All UI lives in the ui.* packages, layered like the
 * official client (layout / sidebar / conversation); this class owns only
 * the data path: session open/replay, turn dispatch, the live event poll,
 * and the settings persistence.
 */
class ChatActivity : ComponentActivity() {
    private val tag = "qrpdsh"

    private var sessionId by mutableStateOf<String?>(null)
    private val rows = mutableStateListOf<ChatRow>()
    private var nodeExecutable by mutableStateOf<String?>(null)
    private var turnRunning by mutableStateOf(false)
    private var showSettings by mutableStateOf(false)
    private var sidebarOpen by mutableStateOf(false)
    private var config by mutableStateOf(Config.EMPTY)
    private var dsw: Dsw = DswLight

    override fun onCreate(savedInstanceState: Bundle?) {
        enableEdgeToEdge()
        super.onCreate(savedInstanceState)
        config = Config.load(this)
        dsw = if (isDarkTheme()) DswDark else DswLight
        setContent { Root() }

        lifecycleScope.launch(Dispatchers.IO) {
            val node = try {
                NodeRuntime.materialize(applicationContext)
            } catch (e: IOException) {
                null
            } catch (t: Throwable) {
                Log.e(tag, "node materialize failed", t)
                null
            }
            withContext(Dispatchers.Main) { nodeExecutable = node }
            val session = Native.openSession(getFilesDir().absolutePath)
            withContext(Dispatchers.Main) { applySession(session) }
        }
    }

    @Composable
    private fun Root() {
        // The frame owns the column solve; the phone keeps the drawer.
        AppFrame(
            dsw = dsw,
            sidebar = { collapsed, onToggle ->
                SidebarRoot(
                    dsw = dsw,
                    sessions = listOf(
                        SessionRowUi(
                            id = sessionId ?: "",
                            title = "Current session",
                            running = turnRunning,
                        ),
                    ),
                    currentId = sessionId,
                    onSelect = { },
                    onNewSession = { onNewSession() },
                    collapsed = collapsed,
                    onToggle = onToggle,
                    version = "",
                )
            },
            center = {
                ConversationRoot(
                    dsw = dsw,
                    title = "Current session",
                    sidebarToggle = { sidebarOpen = true },
                ) {
                    ChatView(dsw, rows, turnRunning) { send(it) }
                }
            },
        )
        if (showSettings) {
            SettingsSheet(config) { updated ->
                config = updated
                updated.save(this)
                showSettings = false
            }
        }
    }

    private fun onNewSession() {
        // Close the current session and open a fresh one.
        val sid = sessionId ?: return
        lifecycleScope.launch(Dispatchers.IO) {
            Native.closeSession(sid)
            val session = Native.openSession(getFilesDir().absolutePath)
            withContext(Dispatchers.Main) { applySession(session) }
        }
    }

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
}

/** One rendered row of the conversation; the projection type shared with the ui layer. */
sealed class ChatRow {
    data class User(val text: String) : ChatRow()
    data class Assistant(val text: String) : ChatRow()
    data class AssistantWithTools(val text: String, val toolNames: List<String>) : ChatRow()
    data class ToolCall(val name: String, val arguments: String) : ChatRow()
    data class ToolResult(val name: String, val output: String, val error: String) : ChatRow()
    data class Error(val text: String) : ChatRow()
}


@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
private fun SettingsSheet(current: ChatActivity.Config, onSave: (ChatActivity.Config) -> Unit) {
    ModalBottomSheet(onDismissRequest = { onSave(current) }) {
        Column(Modifier.fillMaxWidth().padding(16.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
            Text("Settings", fontSize = 16.sp)
            LabeledField("Base URL", current.baseUrl) { onSave(ChatActivity.Config(it, current.apiKey, current.model)) }
            LabeledField("API key", current.apiKey) { onSave(ChatActivity.Config(current.baseUrl, it, current.model)) }
            LabeledField("Model", current.model) { onSave(ChatActivity.Config(current.baseUrl, current.apiKey, it)) }
            TextButton(
                onClick = { },
                modifier = Modifier.fillMaxWidth(),
            ) { Text("Done") }
            androidx.compose.foundation.layout.Box(Modifier.navigationBarsPadding())
        }
    }
}

@Composable
private fun LabeledField(label: String, value: String, onChange: (String) -> Unit) {
    var text by mutableStateOf(value)
    Column {
        Text(label, fontSize = 12.sp)
        TextField(
            value = text,
            onValueChange = { text = it; onChange(it) },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
    }
}
