package com.sta1n.qrpdsh.ui.layout

import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import com.sta1n.qrpdsh.ui.Dsw
import com.sta1n.qrpdsh.ui.Width

/**
 * The three-column shell, ported from ui-layout AppFrame.tsx + columns.ts.
 *
 * Owns the column solve ([Width.computeColumns]), the sidebar drag handle
 * (pointer-captured dx at pointer cadence, clamped by the caller — the same
 * feel contract as the conversation width handles), and the auto-collapse
 * below 1024px: the sidebar becomes a 56px icon rail (columns.ts
 * SIDEBAR_AUTO_COLLAPSE / SIDEBAR_COLLAPSED).
 */
@Composable
fun AppFrame(
    dsw: Dsw,
    sidebar: @Composable (collapsed: Boolean, onToggle: () -> Unit) -> Unit,
    center: @Composable () -> Unit,
) {
    var sidebarPref by remember { mutableIntStateOf(Width.SIDEBAR_DEFAULT) }
    var sidebarOpen by remember { mutableStateOf(true) }

    val density = LocalDensity.current
    androidx.compose.foundation.layout.BoxWithConstraints(Modifier.fillMaxSize()) {
        val viewportPx = with(density) { maxWidth.toPx().toInt() }

        // columns.ts: viewport < 1024 auto-collapses the sidebar to the rail.
        val autoCollapse = Width.sidebarAutoCollapse(viewportPx)
        val open = sidebarOpen && !autoCollapse
        val cols = Width.computeColumns(
            viewport = viewportPx,
            sidebar = if (open) sidebarPref else 0,
            rightbar = 0,
        )
        val sidebarDp = with(density) { Dp(cols.sidebar.toFloat()) }

        Row(Modifier.fillMaxSize().background(dsw.bgBase)) {
            if (cols.sidebar > 0) {
                SidebarColumn(
                    widthDp = sidebarDp,
                    dsw = dsw,
                    onDragPx = { dxPx ->
                        // Screen dx is negated: dragging the border left
                        // shrinks the sidebar. Clamped here to the contract
                        // range the official handle enforces at commit.
                        sidebarPref = (sidebarPref - dxPx)
                            .coerceIn(Width.SIDEBAR_MIN, Width.SIDEBAR_MAX)
                    },
                    content = sidebar,
                )
            } else if (autoCollapse) {
                // The 56px icon rail; tapping re-expands over the squeezed
                // center (stores.ts narrowExpanded).
                Surface(
                    color = dsw.sidebarFill,
                    modifier = Modifier.width(Width.SIDEBAR_COLLAPSED.dp).fillMaxHeight(),
                ) {
                    Box { sidebar(true) { sidebarOpen = true } }
                }
            }
            Column(Modifier.weight(1f).fillMaxSize()) {
                center()
            }
        }
    }
}

/**
 * The sidebar column: specific-sidebar-fill, a 0.5px border-l3 separator on
 * its right edge, and a 10px drag strip centered on that border
 * (AppFrame.module.css .sidebarCol + DragHandle).
 */
@Composable
private fun androidx.compose.foundation.layout.RowScope.SidebarColumn(
    widthDp: Dp,
    dsw: Dsw,
    onDragPx: (Int) -> Unit,
    content: @Composable (collapsed: Boolean, onToggle: () -> Unit) -> Unit,
) {
    val density = LocalDensity.current
    Surface(color = dsw.sidebarFill, modifier = Modifier.width(widthDp).fillMaxHeight()) {
        Box(Modifier.fillMaxSize()) {
            content(false) { }
            Box(
                Modifier
                    .align(Alignment.CenterEnd)
                    .fillMaxHeight()
                    .width(0.5.dp)
                    .background(dsw.borderL3),
            )
            Box(
                Modifier
                    .align(Alignment.CenterEnd)
                    .fillMaxHeight()
                    .width(10.dp)
                    .offset(x = 5.dp)
                    .pointerInput(Unit) {
                    awaitEachGesture {
                        val down = awaitFirstDown()
                        var lastX = down.position.x
                        drag(down.id) { change ->
                            val dxPx = with(density) {
                                (change.position.x - lastX).dp.toPx().toInt()
                            }
                            onDragPx(dxPx)
                            lastX = change.position.x
                        }
                    }
                },
            )
        }
    }
}
