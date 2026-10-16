package com.sta1n.qrpdsh.ui.conversation

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw

/**
 * The conversation column, ported from ui-conversation ConversationRoot.
 *
 * Header (ConversationRoot.module.css .header + .titleRow + .crumbs):
 * 48px phone header (the desktop's 76px carries the tab strip; the phone has
 * none), 0.5px border-l3 rule beneath, current crumb in 500 weight
 * label-primary.
 */
@Composable
fun ConversationRoot(
    dsw: Dsw,
    title: String,
    sidebarToggle: (() -> Unit)?,
    content: @Composable () -> Unit,
) {
    Column(Modifier.fillMaxSize()) {
        Row(
            Modifier.fillMaxWidth()
                .height(48.dp)
                .padding(start = 12.dp, end = 28.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            if (sidebarToggle != null) {
                TextButton(
                    onClick = sidebarToggle,
                    colors = ButtonDefaults.textButtonColors(contentColor = dsw.textTertiary),
                    contentPadding = androidx.compose.foundation.layout.PaddingValues(4.dp),
                ) { Text("☰", fontSize = 18.sp) }
            }
            Text(
                title,
                color = dsw.textPrimary,
                fontSize = 14.sp,
                lineHeight = 20.sp,
                fontWeight = FontWeight.Medium,
                maxLines = 1,
            )
        }
        Box(Modifier.fillMaxWidth().height(0.5.dp).background(dsw.borderL3))
        Column(Modifier.weight(1f).fillMaxSize()) {
            content()
        }
    }
}
