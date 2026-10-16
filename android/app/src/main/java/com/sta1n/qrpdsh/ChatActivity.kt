package com.sta1n.qrpdsh

import android.os.Bundle
import java.io.IOException
import android.util.Log
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

/**
 * The Compose conversation surface.
 *
 * One activity, one session, one column: the mobile form of the DSH frame.
 * Events arrive from the Go side as JSON strings and are projected into
 * [rows] — the projection is append-only, which is what a session log is.
 */
class ChatActivity : ComponentActivity() {
    private val tag = "qrpdsh"

    private var sessionId by mutableStateOf<String?>(null)
    private val rows = mutableStateListOf<ChatRow>()
    private var nodeExecutable by mutableStateOf<String?>(null)
    private var turnRunning by mutableStateOf(false)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent { ChatSurface() }

        lifecycleScope.launch(Dispatchers.IO) {
            // The node runtime materializes on first launch; both the session
            // open and this copy are IO, so they run off the main thread.
            val node = try {
                NodeRuntime.materialize(applicationContext)
            } catch (e: IOException) {
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
                rows.add(project(events.getJSONObject(i)))
            }
        } catch (e: Exception) {
            Log.e(tag, "session replay failed", e)
            toast("session replay failed: " + e.message)
        }
    }

    /** Sends one user turn on the worker path; the Go side appends events. */
    private fun send(task: String) {
        val sid = sessionId ?: return
        if (turnRunning || task.isBlank()) return
        turnRunning = true
        rows.add(ChatRow.User(task))
        val node = nodeExecutable
        val baseUrl = "https://api.deepseek.com/v1"
        val apiKey = ""
        val model = "deepseek-chat"
        lifecycleScope.launch(Dispatchers.IO) {
            val result = try {
                Native.runTurn(task, getFilesDir().absolutePath, getFilesDir().absolutePath,
                        baseUrl, apiKey, model, node, sid)
            } catch (t: Throwable) {
                Log.e(tag, "runTurn threw", t)
                "{\"error\":\"" + t.message + "\"}"
            }
            withContext(Dispatchers.Main) {
                turnRunning = false
                applyResult(result)
            }
        }
    }

    /**
     * Applies a turn result. The turn's events are already in the session
     * log; re-reading them here (a second openSession round-trip would work)
     * is deferred until streaming: this projection replaces only the tail
     * with the turn's final text, which is what the log records last.
     */
    private fun applyResult(result: String) {
        try {
            val obj = JSONObject(result)
            if (obj.has("error")) {
                rows.add(ChatRow.Error(obj.getString("error")))
            } else {
                rows.add(ChatRow.Assistant(obj.optString("text", "")))
            }
        } catch (e: Exception) {
            rows.add(ChatRow.Error(result))
        }
    }

    private fun project(e: JSONObject): ChatRow {
        return when (e.optString("type")) {
            "user/message" -> ChatRow.User(e.optJSONObject("data")?.optString("text") ?: "")
            "assistant/message" -> ChatRow.Assistant(e.optJSONObject("data")?.optString("text") ?: "")
            else -> ChatRow.System(e.optString("type"))
        }
    }

    private fun toast(msg: String) {
        Toast.makeText(this, msg, Toast.LENGTH_SHORT).show()
    }

    @Composable
    private fun ChatSurface() {
        Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
            Column(modifier = Modifier.fillMaxSize().imePadding()) {
                Transcript(rows)
                Composer { send(it) }
            }
        }
    }

    @Composable
    private fun Transcript(rows: List<ChatRow>) {
        Box(modifier = Modifier.fillMaxSize()) {
            LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                items(rows) { row ->
                    when (row) {
                        is ChatRow.User -> Bubble(row.text, primary = true)
                        is ChatRow.Assistant -> Bubble(row.text, primary = false)
                        is ChatRow.Error -> Bubble("error: " + row.text, primary = false)
                        is ChatRow.System -> {}
                    }
                }
            }
        }
    }

    @Composable
    private fun Bubble(text: String, primary: Boolean) {
        val dsw = if (primary) MaterialTheme.colorScheme.primaryContainer
        else MaterialTheme.colorScheme.surfaceVariant
        Box(modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
            Surface(shape = MaterialTheme.shapes.medium, color = dsw) {
                Text(text, modifier = Modifier.padding(10.dp), fontSize = 14.sp)
            }
        }
    }

    @Composable
    private fun Composer(onSend: (String) -> Unit) {
        var text by mutableStateOf("")
        Row(
                modifier = Modifier.fillMaxWidth().padding(8.dp),
                verticalAlignment = Alignment.Bottom,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            OutlinedTextField(
                    value = text,
                    onValueChange = { text = it },
                    modifier = Modifier.weight(1f),
                    placeholder = { Text("Message") },
                    maxLines = 5,
            )
            androidx.compose.material3.Button(
                    onClick = {
                        onSend(text)
                        text = ""
                    },
                    enabled = text.isNotBlank() && !turnRunning,
            ) {
                Text("Send")
            }
        }
    }
}

/** One rendered line of the conversation. */
sealed class ChatRow {
    data class User(val text: String) : ChatRow()
    data class Assistant(val text: String) : ChatRow()
    data class Error(val text: String) : ChatRow()
    data class System(val text: String) : ChatRow()
}
