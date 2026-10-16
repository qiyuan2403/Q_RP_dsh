package com.sta1n.qrpdsh.ui

import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.Dp

/**
 * The DSH adaptive width system, ported line-for-line from the official
 * client so phone, split-screen, and tablet get the same geometry.
 *
 * Sources (read, not guessed):
 * - ui-conversation/src/client/skeleton/ConversationWidthControls.tsx
 *   — the content column width solver.
 * - ui-conversation/src/client/skeleton/ConversationRoot.module.css
 *   — the composer card cap and the side clearance.
 * - ui-chat/src/client/chat/ChatView.module.css — transcript padding.
 * - ui-layout/src/client/columns.ts — the three-column shrink contract
 *   (sidebar never concedes; the right track dies before center drops
 *   below its minimum; the sidebar auto-collapses below 1024px).
 */
object Width {

    // ---- ConversationWidthControls.tsx constants ----
    /** Floor for a dragged content width. */
    const val CONTENT_MIN = 640
    /** Horizontal room reserved for both handles and their safe edge zones. */
    const val CONTENT_EDGE_BUDGET = 176

    // ---- columns.ts constants ----
    const val CENTER_MIN = 400
    const val SIDEBAR_MIN = 264
    const val SIDEBAR_MAX = 420
    const val SIDEBAR_DEFAULT = 280
    const val SIDEBAR_COLLAPSED = 56
    const val SIDEBAR_AUTO_COLLAPSE = 1024
    const val RIGHTBAR_MIN = 300
    const val RIGHTBAR_MAX_RATIO = 0.7
    const val RIGHTBAR_DEFAULT_RATIO = 0.45

    /**
     * resolveContentWidth, one-to-one: the transcript column width for a
     * measured conversation column. The user's drag wins inside the clamp;
     * the default is max(680, min(column * 0.64, 920)).
     */
    fun resolveContentWidth(columnWidth: Int, preference: Int?): Int {
        val max = maxOf(CONTENT_MIN, columnWidth - CONTENT_EDGE_BUDGET)
        if (preference != null) return preference.coerceIn(CONTENT_MIN, max)
        return maxOf(680, minOf((columnWidth * 0.64).toInt(), 920))
    }

    /** --dsh-composer-card-max-width: the content column plus both clearances. */
    fun composerCardMax(contentWidth: Int): Int = contentWidth + 32

    /** Clamp a panel width into its contract range (columns.ts clampWidth). */
    fun clampWidth(px: Int, min: Int, max: Int): Int = px.coerceIn(min, max)

    /** The three-column solve for one viewport (columns.ts computeColumns). */
    data class Columns(val sidebar: Int, val center: Int, val rightbar: Int)

    fun computeColumns(viewport: Int, sidebar: Int, rightbar: Int, collapsedWidth: Int = SIDEBAR_COLLAPSED): Columns {
        val s = if (sidebar == 0) collapsedWidth else clampWidth(sidebar, SIDEBAR_MIN, SIDEBAR_MAX)
        val available = viewport - s - CENTER_MIN
        val r = if (rightbar == 0 || available < RIGHTBAR_MIN) 0
        else minOf(available, clampWidth(rightbar, RIGHTBAR_MIN, (viewport * RIGHTBAR_MAX_RATIO).toInt()))
        return Columns(sidebar = s, center = maxOf(0, viewport - s - r), rightbar = r)
    }

    /** Sidebar auto-collapse rule (stores.ts narrowExpanded). */
    fun sidebarAutoCollapse(viewport: Int): Boolean = viewport < SIDEBAR_AUTO_COLLAPSE

    // ---- Composer clearances (ConversationRoot.module.css .body) ----
    val composerSideClearance: Dp = 16.dp
    /** .scroll: 16px calc(clearance+16px) per side. */
    val transcriptSidePadding: Dp = 32.dp
}
