package com.lucille.mobile

import android.net.Uri
import android.os.Bundle
import android.text.InputType
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import android.widget.Toast
import androidx.activity.result.contract.ActivityResultContracts
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import java.text.DateFormat
import java.util.Date

/**
 * The private memory drive screen: add, search, delete, and a passphrase-protected
 * backup. Everything stays in this app's encrypted store (see PrivateMemory.kt); the
 * screen is hidden from screenshots and the recent-apps view (FLAG_SECURE).
 */
class PrivateMemoryActivity : AppCompatActivity() {
    private lateinit var store: PrivateMemoryStore
    private lateinit var list: LinearLayout
    private lateinit var search: EditText
    private var pendingExport: CharArray? = null

    private val createBackup = registerForActivityResult(ActivityResultContracts.CreateDocument("application/octet-stream")) { uri ->
        val pass = pendingExport.also { pendingExport = null }
        if (uri == null || pass == null) return@registerForActivityResult
        runCatching {
            val blob = store.export(pass)
            contentResolver.openOutputStream(uri)!!.use { it.write(blob) }
        }.onSuccess { toast("Backup saved. Keep the passphrase: without it the backup can't be opened.") }
            .onFailure { toast(it.message ?: "Couldn't save the backup.") }
    }

    private val openBackup = registerForActivityResult(ActivityResultContracts.OpenDocument()) { uri ->
        if (uri != null) askPassphrase("Passphrase for this backup") { pass -> restore(uri, pass) }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.setFlags(WindowManager.LayoutParams.FLAG_SECURE, WindowManager.LayoutParams.FLAG_SECURE)
        store = PrivateMemoryKey.store(this)
        val pad = (16 * resources.displayMetrics.density).toInt()

        fun label(s: String, size: Float = 13f) = TextView(this).apply {
            text = s; textSize = size; setTextColor(getColor(R.color.text_primary)); setPadding(0, pad / 2, 0, 0)
        }
        val input = EditText(this).apply {
            hint = "Something for Lucille to keep to herself"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_FLAG_MULTI_LINE or InputType.TYPE_TEXT_FLAG_CAP_SENTENCES
            minLines = 2
        }
        val add = Button(this).apply {
            text = "Remember privately"
            setOnClickListener {
                runCatching { store.add(input.text.toString()) }
                    .onSuccess { input.setText(""); refresh() }
                    .onFailure { toast(it.message ?: "Couldn't save that.") }
            }
        }
        search = EditText(this).apply {
            hint = "Search"
            inputType = InputType.TYPE_CLASS_TEXT
            addTextChangedListener(object : android.text.TextWatcher {
                override fun afterTextChanged(s: android.text.Editable?) = refresh()
                override fun beforeTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
                override fun onTextChanged(s: CharSequence?, a: Int, b: Int, c: Int) {}
            })
        }
        list = LinearLayout(this).apply { orientation = LinearLayout.VERTICAL }
        val backup = Button(this).apply { text = "Back up…"; setOnClickListener { askPassphrase("Choose a passphrase for the backup (8+ characters)") { p ->
            pendingExport = p; createBackup.launch("lucille-private-memory.lpm") } } }
        val restore = Button(this).apply { text = "Restore…"; setOnClickListener { openBackup.launch(arrayOf("*/*")) } }

        val col = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
            addView(label("Private memory", 20f))
            addView(label("Stays on this phone, encrypted with a key held by the phone's secure hardware. Never synced, never in swarm-memory or GitHub. If you lose this phone, only a passphrase backup can bring it back."))
            addView(input); addView(add); addView(search)
            addView(list)
            addView(LinearLayout(this@PrivateMemoryActivity).apply { addView(backup); addView(restore) })
        }
        setContentView(ScrollView(this).apply { setBackgroundColor(getColor(R.color.bg)); addView(col); fitSystemBars() })
        refresh()
    }

    private fun refresh() {
        list.removeAllViews()
        val fmt = DateFormat.getDateTimeInstance(DateFormat.MEDIUM, DateFormat.SHORT)
        val items = store.search(search.text.toString())
        if (items.isEmpty()) list.addView(TextView(this).apply { text = "Nothing here yet."; setPadding(0, 24, 0, 0) })
        items.forEach { e ->
            list.addView(TextView(this).apply {
                text = "${e.text}\n${fmt.format(Date(e.time))}  ·  tap to delete"
                setTextColor(getColor(R.color.text_primary))
                setPadding(0, 20, 0, 20)
                setOnClickListener {
                    AlertDialog.Builder(this@PrivateMemoryActivity).setMessage("Delete this memory for good?")
                        .setPositiveButton("Delete") { _, _ -> store.delete(e.id); refresh() }
                        .setNegativeButton("Keep", null).show()
                }
            })
        }
    }

    private fun restore(uri: Uri, pass: CharArray) {
        runCatching { store.import(contentResolver.openInputStream(uri)!!.use { it.readBytes() }, pass) }
            .onSuccess { n -> toast(if (n == 0) "Nothing new in that backup." else "Restored $n memories."); refresh() }
            .onFailure { toast(it.message ?: "Couldn't restore that backup.") }
    }

    private fun askPassphrase(title: String, then: (CharArray) -> Unit) {
        val f = EditText(this).apply { inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_PASSWORD }
        AlertDialog.Builder(this).setTitle(title).setView(f)
            .setPositiveButton("OK") { _, _ -> then(f.text.toString().toCharArray()) }
            .setNegativeButton("Cancel", null).show()
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_LONG).show()
}
