package com.sta1n.qrpdsh

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.theme.LocalDsw

/**
 * A small, self-contained Markdown renderer for assistant messages.
 *
 * WHY NOT A LIBRARY. The cached third-party renderer (mikepenz 0.34.0) was
 * compiled against a newer Compose foundation than this project's pinned BOM
 * (2025.01 -> Compose 1.7.x), so it threw NoSuchMethodError at the first
 * rendered paragraph on a real device and killed the app. Upgrading the BOM
 * needs compileSdk 37, which is not installed. This renderer covers what a
 * chat transcript actually uses and depends only on the Compose version that
 * already ships.
 *
 * WHAT IT COVERS. Headings, paragraphs, fenced code, bullet and numbered
 * lists, block quotes, horizontal rules, and inline bold/italic/code/links.
 * Tables and nested lists fall back to plain paragraphs rather than pretending
 * to be perfect.
 *
 * PARSING HAPPENS ONCE per durable message, in the composition's remember, not
 * per frame: the live streaming segment stays plain Text on purpose.
 */

sealed interface MdBlock {
    data class Heading(val level: Int, val text: String) : MdBlock
    data class Paragraph(val text: String) : MdBlock
    data class CodeBlock(val language: String, val content: String) : MdBlock
    data class BulletList(val items: List<String>) : MdBlock
    data class NumberedList(val items: List<String>) : MdBlock
    data class Quote(val lines: List<String>) : MdBlock
    data object Rule : MdBlock
}

private val bulletPattern = Regex("^[-*+]\\s+")
private val numberedPattern = Regex("^\\d+[.)]\\s+")
private val rulePattern = Regex("^([-*_])\\1{2,}$")
private val headingPattern = Regex("^(#{1,6})\\s+(.*)$")

/** Split one Markdown document into renderable blocks. */
fun parseMarkdown(source: String): List<MdBlock> {
    val blocks = mutableListOf<MdBlock>()
    val lines = source.replace("\r\n", "\n").replace('\r', '\n').split('\n')
    val paragraph = StringBuilder()
    var i = 0

    fun flushParagraph() {
        val text = paragraph.toString().trim()
        if (text.isNotEmpty()) blocks += MdBlock.Paragraph(text)
        paragraph.clear()
    }

    while (i < lines.size) {
        val raw = lines[i]
        val line = raw.trim()
        when {
            line.isEmpty() -> {
                flushParagraph()
                i += 1
            }

            line.startsWith("```") -> {
                flushParagraph()
                val language = line.removePrefix("```").trim()
                i += 1
                val code = StringBuilder()
                while (i < lines.size && !lines[i].trim().startsWith("```")) {
                    if (code.isNotEmpty()) code.append('\n')
                    code.append(lines[i])
                    i += 1
                }
                if (i < lines.size) i += 1
                blocks += MdBlock.CodeBlock(language, code.toString())
            }

            headingPattern.matches(line) -> {
                flushParagraph()
                val match = headingPattern.matchEntire(line)!!
                blocks += MdBlock.Heading(match.groupValues[1].length, match.groupValues[2].trim())
                i += 1
            }

            rulePattern.matches(line) -> {
                flushParagraph()
                blocks += MdBlock.Rule
                i += 1
            }

            line.startsWith(">") -> {
                flushParagraph()
                val quote = mutableListOf<String>()
                while (i < lines.size && lines[i].trim().startsWith(">")) {
                    quote += lines[i].trim().removePrefix(">").trimStart()
                    i += 1
                }
                blocks += MdBlock.Quote(quote)
            }

            bulletPattern.containsMatchIn(line) -> {
                flushParagraph()
                val items = mutableListOf<String>()
                while (i < lines.size && bulletPattern.containsMatchIn(lines[i].trim())) {
                    items += lines[i].trim().replaceFirst(bulletPattern, "")
                    i += 1
                }
                blocks += MdBlock.BulletList(items)
            }

            numberedPattern.containsMatchIn(line) -> {
                flushParagraph()
                val items = mutableListOf<String>()
                while (i < lines.size && numberedPattern.containsMatchIn(lines[i].trim())) {
                    items += lines[i].trim().replaceFirst(numberedPattern, "")
                    i += 1
                }
                blocks += MdBlock.NumberedList(items)
            }

            else -> {
                if (paragraph.isNotEmpty()) paragraph.append('\n')
                paragraph.append(line)
                i += 1
            }
        }
    }
    flushParagraph()
    return blocks
}

/** Style one line of inline Markdown (bold, italic, code, links). */
private fun inlineString(
    source: String,
    textColor: Color,
    linkColor: Color,
    codeColor: Color,
    codeBackground: Color,
    baseStyle: SpanStyle? = null,
): AnnotatedString = buildAnnotatedString {
    appendInline(source, textColor, linkColor, codeColor, codeBackground, baseStyle)
}

private fun AnnotatedString.Builder.appendInline(
    source: String,
    textColor: Color,
    linkColor: Color,
    codeColor: Color,
    codeBackground: Color,
    style: SpanStyle?,
) {
    val plain = StringBuilder()

    fun emit(text: String, span: SpanStyle) {
        if (text.isEmpty()) return
        val merged = if (style == null) span else style.merge(span)
        withStyle(merged) { append(text) }
    }

    fun flushPlain() {
        if (plain.isEmpty()) return
        val text = plain.toString()
        plain.clear()
        if (style == null) append(text) else withStyle(style) { append(text) }
    }

    var i = 0
    while (i < source.length) {
        when {
            source.startsWith("**", i) -> {
                val end = source.indexOf("**", i + 2)
                if (end > i + 2) {
                    flushPlain()
                    emit(source.substring(i + 2, end), SpanStyle(fontWeight = FontWeight.SemiBold))
                    i = end + 2
                } else {
                    plain.append(source[i])
                    i += 1
                }
            }

            source[i] == '`' -> {
                val end = source.indexOf('`', i + 1)
                if (end > i + 1) {
                    flushPlain()
                    emit(
                        source.substring(i + 1, end),
                        SpanStyle(fontFamily = FontFamily.Monospace, color = codeColor, background = codeBackground),
                    )
                    i = end + 1
                } else {
                    plain.append(source[i])
                    i += 1
                }
            }

            source[i] == '*' || source[i] == '_' -> {
                val marker = source[i]
                val end = source.indexOf(marker, i + 1)
                val next = if (end + 1 < source.length) source[end + 1] else ' '
                if (end > i + 1 && !next.isLetterOrDigit()) {
                    flushPlain()
                    emit(source.substring(i + 1, end), SpanStyle(fontStyle = FontStyle.Italic))
                    i = end + 1
                } else {
                    plain.append(source[i])
                    i += 1
                }
            }

            source.startsWith("[", i) -> {
                val labelEnd = source.indexOf("](", i + 1)
                val urlEnd = if (labelEnd >= 0) source.indexOf(')', labelEnd + 2) else -1
                if (labelEnd > i && urlEnd > labelEnd) {
                    flushPlain()
                    emit(
                        source.substring(i + 1, labelEnd),
                        SpanStyle(color = linkColor, textDecoration = androidx.compose.ui.text.style.TextDecoration.Underline),
                    )
                    i = urlEnd + 1
                } else {
                    plain.append(source[i])
                    i += 1
                }
            }

            else -> {
                plain.append(source[i])
                i += 1
            }
        }
    }
    flushPlain()
}

/** Render one durable assistant message. */
@Composable
fun MarkdownBody(text: String, dsw: Dsw) {
    val blocks = remember(text) { parseMarkdown(text) }
    Column(Modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        for (block in blocks) {
            when (block) {
                is MdBlock.Heading -> {
                    val size = when (block.level) {
                        1 -> 22.sp
                        2 -> 19.sp
                        3 -> 17.sp
                        else -> 15.sp
                    }
                    Text(
                        text = inlineString(
                            block.text, dsw.textPrimary, dsw.brandText, dsw.textPrimary, dsw.bgLayer2,
                            SpanStyle(fontWeight = FontWeight.SemiBold, color = dsw.textPrimary),
                        ),
                        fontSize = size,
                        color = dsw.textPrimary,
                        lineHeight = (size.value + 8).sp,
                    )
                }

                is MdBlock.Paragraph -> Text(
                    text = inlineString(block.text, dsw.textPrimary, dsw.brandText, dsw.textPrimary, dsw.bgLayer2),
                    color = dsw.textPrimary,
                    fontSize = dsw.textSize,
                    lineHeight = 22.sp,
                )

                is MdBlock.CodeBlock -> Surface(
                    color = dsw.bgLayer2,
                    shape = RoundedCornerShape(8.dp),
                    modifier = Modifier.fillMaxWidth(),
                ) {
                    Text(
                        text = block.content,
                        modifier = Modifier.padding(12.dp),
                        color = dsw.textPrimary,
                        fontFamily = FontFamily.Monospace,
                        fontSize = 12.sp,
                        lineHeight = 18.sp,
                    )
                }

                is MdBlock.BulletList -> Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    for (item in block.items) {
                        Row(Modifier.fillMaxWidth()) {
                            Text("•", color = dsw.textSecondary, modifier = Modifier.width(14.dp))
                            Text(
                                text = inlineString(item, dsw.textPrimary, dsw.brandText, dsw.textPrimary, dsw.bgLayer2),
                                color = dsw.textPrimary,
                                fontSize = dsw.textSize,
                                lineHeight = 22.sp,
                                modifier = Modifier.weight(1f),
                            )
                        }
                    }
                }

                is MdBlock.NumberedList -> Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    block.items.forEachIndexed { index, item ->
                        Row(Modifier.fillMaxWidth()) {
                            Text("${index + 1}.", color = dsw.textSecondary, modifier = Modifier.width(22.dp), fontSize = dsw.textSize)
                            Text(
                                text = inlineString(item, dsw.textPrimary, dsw.brandText, dsw.textPrimary, dsw.bgLayer2),
                                color = dsw.textPrimary,
                                fontSize = dsw.textSize,
                                lineHeight = 22.sp,
                                modifier = Modifier.weight(1f),
                            )
                        }
                    }
                }

                is MdBlock.Quote -> Row(Modifier.fillMaxWidth()) {
                    Box(Modifier.width(2.dp).height(24.dp).background(dsw.borderL2))
                    Spacer(Modifier.width(10.dp))
                    Column {
                        for (line in block.lines) {
                            Text(
                                text = inlineString(line, dsw.textSecondary, dsw.brandText, dsw.textPrimary, dsw.bgLayer2),
                                color = dsw.textSecondary,
                                fontSize = dsw.textSize,
                                lineHeight = 22.sp,
                            )
                        }
                    }
                }

                MdBlock.Rule -> Box(Modifier.fillMaxWidth().height(1.dp).background(dsw.borderL2))
            }
        }
    }
}
