package com.sta1n.qrpdsh.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

/**
 * The primitives this client draws, each mirroring an official counterpart
 * in `packages/client/ui-primitives` (47 components).
 *
 * The set is deliberately small: a chat surface needs a button, a text
 * field, a switch, a disclosure row, and a terminal/diff block. Anything
 * beyond that is a screen, not a primitive.
 */

/** Control heights and corner radii from the official control specs. */
private val ControlHeightMd = 36.dp
private val ControlHeightSm = 28.dp

/**
 * The standard button. Variants mirror the official `ButtonVariant`:
 * primary (filled brand), ghost (transparent, hover fill), outline (border),
 * toolbar (icon-sized).
 */
@Composable
fun DswButton(
    text: String,
    onClick: () -> Unit,
    dsw: Dsw,
    enabled: Boolean = true,
    primary: Boolean = false,
    small: Boolean = false,
    modifier: Modifier = Modifier,
) {
    val shape = RoundedCornerShape(if (small) 8.dp else 12.dp)
    val colors = if (primary) {
        ButtonDefaults.buttonColors(
            containerColor = dsw.buttonPrimaryFill,
            contentColor = dsw.textInverted,
        )
    } else {
        ButtonDefaults.buttonColors(
            containerColor = Color.Transparent,
            contentColor = dsw.textPrimary,
        )
    }
    Button(
        onClick = onClick,
        enabled = enabled,
        shape = shape,
        colors = colors,
        modifier = modifier,
    ) {
        Text(text, fontSize = if (small) dsw.labelSize else dsw.textSize)
    }
}

/** A labelled text field: the official ConfigField shape. */
@Composable
fun DswField(
    label: String,
    value: String,
    onValueChange: (String) -> Unit,
    dsw: Dsw,
    modifier: Modifier = Modifier,
    mono: Boolean = false,
) {
    Column(modifier = modifier) {
        Text(
            label,
            fontSize = dsw.labelSize,
            color = dsw.textSecondary,
        )
        OutlinedTextField(
            value = value,
            onValueChange = onValueChange,
            singleLine = true,
            textStyle = androidx.compose.ui.text.TextStyle(
                fontSize = dsw.textSize,
                fontFamily = if (mono) FontFamily.Monospace else FontFamily.Default,
                color = dsw.textPrimary,
            ),
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

/**
 * A disclosure row: a title that expands to reveal content. The official
 * client uses it for every collapsible block (reasoning, tool output,
 * diff).
 */
@Composable
fun DswDisclosureRow(
    title: String,
    expanded: Boolean,
    onToggle: () -> Unit,
    dsw: Dsw,
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    Column(modifier = modifier.fillMaxWidth()) {
        Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(6.dp),
            modifier = Modifier
                .fillMaxWidth()
                .padding(vertical = 4.dp),
        ) {
            Text(
                if (expanded) "▾" else "▸",
                fontSize = dsw.labelSize,
                color = dsw.textTertiary,
            )
            DswTextButton(title, onToggle, dsw)
        }
        if (expanded) content()
    }
}

/** A borderless text button, the row-level affordance. */
@Composable
fun DswTextButton(text: String, onClick: () -> Unit, dsw: Dsw) {
    Button(
        onClick = onClick,
        shape = RoundedCornerShape(6.dp),
        colors = ButtonDefaults.buttonColors(
            containerColor = Color.Transparent,
            contentColor = dsw.textSecondary,
        ),
        contentPadding = androidx.compose.foundation.layout.PaddingValues(4.dp),
        modifier = Modifier.size(width = 0.dp, height = ControlHeightSm),
    ) {
        Text(text, fontSize = dsw.labelSize)
    }
}

/**
 * A switch row: label plus a Material switch, the official settings-row
 * shape (36x20 track).
 */
@Composable
fun DswSwitchRow(
    label: String,
    checked: Boolean,
    onCheckedChange: (Boolean) -> Unit,
    dsw: Dsw,
    modifier: Modifier = Modifier,
) {
    Row(
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
        modifier = modifier.fillMaxWidth(),
    ) {
        Text(label, fontSize = dsw.textSize, color = dsw.textPrimary)
        Switch(checked = checked, onCheckedChange = onCheckedChange)
    }
}

/** A monospace block: terminal output, code, or a tool result. */
@Composable
fun DswMonoBlock(text: String, dsw: Dsw, modifier: Modifier = Modifier) {
    Text(
        text,
        fontSize = dsw.codeSize,
        fontFamily = FontFamily.Monospace,
        color = dsw.textPrimary,
        modifier = modifier
            .fillMaxWidth()
            .padding(8.dp),
    )
}
