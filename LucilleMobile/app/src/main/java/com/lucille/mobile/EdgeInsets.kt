package com.lucille.mobile

import android.view.View
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat

/**
 * Android 15 draws apps edge to edge, so anything at the top sits under the status bar and
 * camera cutout (taps there never reach the app) and anything at the bottom sits under the
 * gesture bar or keyboard. Pad [this] by the real system bars, the cutout and the keyboard,
 * plus a little breathing room so the app isn't jammed against the edge.
 */
fun View.fitSystemBars(extraTopDp: Int = 8, extraSideDp: Int = 4) {
    val d = resources.displayMetrics.density
    val top = (extraTopDp * d).toInt()
    val side = (extraSideDp * d).toInt()
    ViewCompat.setOnApplyWindowInsetsListener(this) { v, insets ->
        val bars = insets.getInsets(WindowInsetsCompat.Type.systemBars() or WindowInsetsCompat.Type.displayCutout())
        val ime = insets.getInsets(WindowInsetsCompat.Type.ime())
        v.setPadding(bars.left + side, bars.top + top, bars.right + side, maxOf(bars.bottom, ime.bottom))
        WindowInsetsCompat.CONSUMED
    }
    ViewCompat.requestApplyInsets(this)
}
