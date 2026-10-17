package com.sta1n.qrpdsh

import org.json.JSONArray
import org.json.JSONObject

/**
 * One row of the transcript, after the event log has been folded.
 *
 * WHY A TYPED TREE. The session log stores a tool call and its result as two
 * separate events; a chat row must show them as one card. Folding here keeps
 * the composables free of event-protocol knowledge and gives the LazyColumn a
 * stable key per row.
 */
sealed interface ChatNode {
    val key: String
}

data class UserMessage(val seq: Long, val text: String) : ChatNode {
    override val key: String = "user:$seq"
}

data class AssistantMessage(val seq: Long, val text: String) : ChatNode {
    // Durable assistant text is rendered with the Markdown renderer; the live
    // stream segment stays plain text until the turn commits.
    override val key: String = "assistant:$seq"
}

data class ToolNode(
    val seq: Long,
    val callId: String,
    val name: String,
    val arguments: String,
    val result: String?,
    val failed: Boolean,
) : ChatNode {
    override val key: String = "tool:$seq"
}

data class Replay(val id: String, val nodes: List<ChatNode>, val error: String?)

/**
 * Get a JSON value as text without turning an absent/null field into the
 * literal "null" JSONObject.optString would produce.
 */
private fun JSONObject.textOf(name: String): String {
    if (!has(name) || isNull(name)) return ""
    val value = opt(name) ?: return ""
    return if (value is String) value else value.toString()
}

/**
 * Fold one durable event into [nodes]; return true when the transcript grew or
 * changed.
 *
 * WHY A MUTABLE FOLD. The same rule runs twice: over the whole session log
 * after a turn, and once per event while the turn is still running, so the
 * screen can show the user's own message and each tool step immediately. A
 * copy per live event would copy the transcript on every step, and a second
 * implementation for replay could disagree with the live one.
 */
private fun foldEvent(nodes: MutableList<ChatNode>, event: JSONObject): Boolean {
    val data = event.optJSONObject("data") ?: return false
    val seq = event.optLong("seq")
    when (event.optString("type")) {
        "user/message" -> {
            val text = data.textOf("text")
            if (text.isBlank()) return false
            // The screen appends an optimistic bubble before the turn starts.
            // The log echo replaces that placeholder instead of showing the
            // same text twice; a replayed log only has durable rows with
            // positive seqs, so this branch cannot match there.
            val last = nodes.lastOrNull()
            if (last is UserMessage && last.seq < 0 && last.text == text) {
                nodes[nodes.size - 1] = UserMessage(seq, text)
            } else {
                nodes += UserMessage(seq, text)
            }
            return true
        }

        "assistant/message" -> {
            // toolCalls carried on the message duplicate the tool/call
            // events; only the text belongs on this row.
            val text = data.textOf("text")
            if (text.isBlank()) return false
            nodes += AssistantMessage(seq, text)
            return true
        }

        "tool/call" -> {
            nodes += ToolNode(
                seq = seq,
                callId = data.textOf("callId"),
                name = data.textOf("name"),
                arguments = data.textOf("arguments"),
                result = null,
                failed = false,
            )
            return true
        }

        "tool/result" -> {
            val callId = data.textOf("callId")
            // The contract says prefer text, fall back to output: some
            // providers put the human-readable result in text only.
            val result = data.textOf("text").ifEmpty { data.textOf("output") }
            val failed = data.has("error") && !data.isNull("error")
            val index = if (callId.isEmpty()) -1 else nodes.indexOfLast { it is ToolNode && it.callId == callId }
            val previous = nodes.getOrNull(index) as? ToolNode
            if (previous != null) {
                // Keep seq: it is the row's identity, and replacing it would
                // discard the card's expansion state.
                nodes[index] = previous.copy(
                    name = previous.name.ifBlank { data.textOf("name") },
                    result = result,
                    failed = failed,
                )
            } else {
                // A result without its call still matters: show it rather
                // than dropping the only trace of the execution.
                nodes += ToolNode(
                    seq = seq,
                    callId = callId,
                    name = data.textOf("name"),
                    arguments = "",
                    result = result,
                    failed = failed,
                )
            }
            return true
        }
    }
    return false
}

/**
 * Fold one live durable event into the rows shown so far.
 *
 * The replay after the turn remains the final word; this only keeps the screen
 * current while the turn runs. Returning the same list when the event does not
 * touch the transcript keeps LazyColumn from recomposing for turn/start or
 * step/end.
 */
fun appendEvent(nodes: List<ChatNode>, event: JSONObject): List<ChatNode> {
    val folded = nodes.toMutableList()
    return if (foldEvent(folded, event)) folded else nodes
}

private fun foldEvents(events: JSONArray): List<ChatNode> {
    val nodes = ArrayList<ChatNode>(events.length())
    for (index in 0 until events.length()) {
        val event = events.optJSONObject(index) ?: continue
        foldEvent(nodes, event)
    }
    return nodes
}

/**
 * Fold one replay document into rows. Failures are returned as [Replay.error]
 * instead of thrown: the caller is a UI callback and must keep rendering the
 * rest of the screen.
 */
fun parseReplay(raw: String): Replay = runCatching {
    val root = JSONObject(raw)
    val events = root.optJSONArray("events") ?: JSONArray()
    val nodes = foldEvents(events)
    Replay(root.optString("id"), nodes, root.optString("error").takeIf(String::isNotBlank))
}.getOrElse { error ->
    Replay("", emptyList(), error.message ?: "无法读取会话")
}
