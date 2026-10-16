package com.sta1n.qrpdsh.ui.sidebar

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.sta1n.qrpdsh.ui.Dsw

/**
 * The sidebar column, ported from ui-sidebar SidebarRoot.tsx + module.css and
 * ui-workspace Rows.module.css. Numbers are the source's, not invented:
 *
 * - .root: padding 6px 12px, fill specific-sidebar-fill, 14px text
 * - .logoRow: height 60px, brand at the start, toggle at the end
 * - .newSession: full-width button, hover fill floating-hover
 * - .sessionRow: height 32px, radius-md 12px, padding 0 8px, a 16px status
 *   slot, then a 4px title gap; hover/selected = interactive-bg-hover
 */
@Composable
fun SidebarRoot(
    dsw: Dsw,
    sessions: List<SessionRowUi>,
    currentId: String?,
    onSelect: (String) -> Unit,
    onNewSession: () -> Unit,
    collapsed: Boolean = false,
    onToggle: () -> Unit = {},
    version: String = "",
) {
    Column(
        Modifier.fillMaxHeight().fillMaxWidth().background(dsw.sidebarFill)
            .padding(horizontal = 12.dp, vertical = 6.dp),
    ) {
        // .logoRow: 60px, brand start, collapse toggle end.
        Row(
            Modifier.fillMaxWidth().height(60.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (!collapsed) {
                Column {
                    Text("DSH", fontSize = 16.sp, fontWeight = FontWeight.Medium, color = dsw.textPrimary)
                    if (version.isNotEmpty()) {
                        Text(version, fontSize = 11.sp, color = dsw.textTertiary)
                    }
                }
            }
            Spacer(Modifier.weight(1f))
            Surface(
                shape = RoundedCornerShape(8.dp),
                color = Color.Transparent,
                modifier = Modifier.size(32.dp).clickable(onClick = onToggle),
            ) {
                Box(contentAlignment = Alignment.Center) {
                    Text(if (collapsed) "»" else "«", color = dsw.textSecondary, fontSize = 16.sp)
                }
            }
        }

        // .newSession: the full-width new-chat button.
        Surface(
            shape = RoundedCornerShape(12.dp),
            color = dsw.bgLayer1,
            modifier = Modifier.fillMaxWidth().height(36.dp).clickable(onClick = onNewSession),
        ) {
            Row(
                Modifier.padding(horizontal = 12.dp),
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                Text("+", color = dsw.textPrimary, fontSize = 16.sp)
                if (!collapsed) {
                    Text("New session", color = dsw.textPrimary, fontSize = 14.sp)
                }
            }
        }

        Spacer(Modifier.height(8.dp))

        // regionArea: the session list (WorkspaceBrowser). Rows are 32px,
        // radius-md, pad 0 8px, 16px status slot + 4px gap + title.
        Column(Modifier.weight(1f).fillMaxWidth()) {
            if (!collapsed) {
                sessions.forEach { s ->
                    val selected = s.id == currentId
                    val hoverFill = dsw.bgLayer2
                    Surface(
                        shape = RoundedCornerShape(12.dp),
                        color = if (selected) hoverFill else Color.Transparent,
                        modifier = Modifier.fillMaxWidth().height(32.dp)
                            .clickable { onSelect(s.id) },
                    ) {
                        Row(
                            verticalAlignment = Alignment.CenterVertically,
                            modifier = Modifier.fillMaxSize().padding(horizontal = 8.dp),
                        ) {
                            // The 16px status slot.
                            Box(Modifier.size(16.dp)) {
                                if (s.running) {
                                    Box(
                                        Modifier.size(8.dp).align(Alignment.Center)
                                            .background(dsw.brandPrimary, RoundedCornerShape(100)),
                                    )
                                }
                            }
                            Spacer(Modifier.width(4.dp))
                            Text(
                                s.title,
                                color = dsw.textPrimary,
                                fontSize = 14.sp,
                                maxLines = 1,
                                modifier = Modifier.padding(start = 4.dp, end = 6.dp),
                            )
                        }
                    }
                }
            } else {
                // Rail: 18px icons only, stacked.
                sessions.take(6).forEach { s ->
                    val selected = s.id == currentId
                    Box(
                        Modifier.size(36.dp).padding(2.dp)
                            .background(
                                if (selected) dsw.bgLayer2 else Color.Transparent,
                                RoundedCornerShape(12.dp),
                            )
                            .clickable { onSelect(s.id) },
                        contentAlignment = Alignment.Center,
                    ) {
                        Box(
                            Modifier.size(10.dp)
                                .background(
                                    if (s.running) dsw.brandPrimary else dsw.textTertiary,
                                    RoundedCornerShape(100),
                                ),
                        )
                    }
                }
            }
        }

        // footArea: settings at the foot.
        if (!collapsed) {
            Row(
                Modifier.fillMaxWidth().height(36.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text("Settings", color = dsw.textSecondary, fontSize = 14.sp)
            }
        }
    }
}

/** One row of the session list, already projected for display. */
data class SessionRowUi(
    val id: String,
    val title: String,
    val running: Boolean,
)
