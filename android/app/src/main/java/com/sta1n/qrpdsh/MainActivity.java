package com.sta1n.qrpdsh;

import android.app.Activity;
import android.os.Bundle;
import android.text.InputType;
import android.util.Log;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import java.io.IOException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * The Android entry point.
 *
 * The activity collects the endpoint, credentials, and the task, then runs one
 * harness turn. The turn is blocking and performs network I/O, so it runs on a
 * worker thread and the result is posted back to the UI thread. Doing the work
 * on the UI thread would raise NetworkOnMainThreadException, and blocking it
 * for the length of a turn would trigger an ANR.
 *
 * Anonymous classes rather than lambdas: the build compiles against the
 * android.jar bootclasspath, which does not carry LambdaMetafactory, so a
 * lambda would not link.
 */
public class MainActivity extends Activity {
    private static final String TAG = "qrpdsh";

    /** A single worker is enough: turns are serial, and one thread cannot leak. */
    private final ExecutorService worker = Executors.newSingleThreadExecutor();

    private EditText baseUrlField;
    private EditText apiKeyField;
    private EditText modelField;
    private EditText taskField;
    private Button runButton;
    private TextView output;

    /**
     * Path of the extracted embedded node executable, or null until
     * {@code materializeNode} finishes (or if it failed). The single worker
     * serializes both the extraction and every turn, so no further sync is
     * needed: the write and the reads all happen on that one thread.
     */
    private String nodeExecutable;
    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        if (!Native.isLoaded()) {
            setContentView(message("Go harness failed to load:\n" + Native.loadError()));
            return;
        }

        setContentView(buildUi());
        append("Q_RP_dsh " + safeVersion());
        append("home: " + getFilesDir().getAbsolutePath());
        materializeNode();
    }

    /**
     * Extracts the embedded Node runtime off the UI thread: a 50MB copy plus
     * a tar extraction is not instant, and onCreate runs on the main thread.
     * A failure degrades the plugin layer only — the core harness still runs.
     */
    private void materializeNode() {
        worker.execute(new Runnable() {
            @Override
            public void run() {
                try {
                    nodeExecutable = NodeRuntime.materialize(MainActivity.this);
                    runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            append("node: " + nodeExecutable);
                        }
                    });
                } catch (final IOException e) {
                    Log.e(TAG, "node materialize failed", e);
                    runOnUiThread(new Runnable() {
                        @Override
                        public void run() {
                            append("node runtime unavailable (plugins disabled): " + e.getMessage());
                        }
                    });
                }
            }
        });
    }

    @Override
    protected void onDestroy() {
        super.onDestroy();
        // A turn may still be in flight; let it finish rather than interrupt it
        // mid-write. shutdown() stops accepting new work.
        worker.shutdown();
    }

    private View buildUi() {
        LinearLayout root = new LinearLayout(this);
        root.setOrientation(LinearLayout.VERTICAL);
        int pad = dp(16);
        root.setPadding(pad, pad, pad, pad);

        baseUrlField = field(root, "Base URL", "https://api.deepseek.com/v1", InputType.TYPE_TEXT_VARIATION_URI);
        apiKeyField = field(root, "API key", "", InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_PASSWORD);
        modelField = field(root, "Model", "deepseek-chat", InputType.TYPE_CLASS_TEXT);
        taskField = field(root, "Task", "List the files in the working directory.", InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_FLAG_MULTI_LINE);

        runButton = new Button(this);
        runButton.setText("Run");
        runButton.setOnClickListener(new View.OnClickListener() {
            @Override
            public void onClick(View v) {
                startTurn();
            }
        });
        root.addView(runButton);

        output = new TextView(this);
        output.setPadding(0, dp(12), 0, 0);

        ScrollView scroll = new ScrollView(this);
        scroll.addView(output);
        root.addView(scroll, new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f));

        return root;
    }

    private EditText field(LinearLayout root, String label, String initial, int inputType) {
        TextView caption = new TextView(this);
        caption.setText(label);
        root.addView(caption);

        EditText edit = new EditText(this);
        edit.setInputType(inputType);
        edit.setText(initial);
        edit.setSingleLine((inputType & InputType.TYPE_TEXT_FLAG_MULTI_LINE) == 0);
        root.addView(edit);
        return edit;
    }

    /** Dispatches one turn to the worker thread and returns immediately. */
    private void startTurn() {
        final String baseUrl = baseUrlField.getText().toString().trim();
        final String apiKey = apiKeyField.getText().toString().trim();
        final String model = modelField.getText().toString().trim();
        final String task = taskField.getText().toString().trim();
        final String home = getFilesDir().getAbsolutePath();

        runButton.setEnabled(false);
        append("\n--- running ---");

        worker.execute(new Runnable() {
            @Override
            public void run() {
                // The node runtime may still be extracting on first launch;
                // a null path runs the turn without the JS layer rather than
                // blocking the turn behind a 50MB copy.
                String nodePath = nodeExecutable;
                String result;
                try {
                    result = Native.runTurn(task, home, home, baseUrl, apiKey, model, nodePath);
                } catch (Throwable t) {
                    // A crash across the JNI boundary must not take the activity
                    // down; surface it as text like any other failure.
                    Log.e(TAG, "runTurn threw", t);
                    result = "runTurn threw: " + t;
                }
                final String text = result;
                runOnUiThread(new Runnable() {
                    @Override
                    public void run() {
                        append(text);
                        runButton.setEnabled(true);
                    }
                });
            }
        });
    }

    private String safeVersion() {
        try {
            return Native.version();
        } catch (Throwable t) {
            return "unknown (" + t + ")";
        }
    }

    private void append(String line) {
        output.append(line + "\n");
    }

    private View message(String text) {
        TextView view = new TextView(this);
        view.setText(text);
        view.setTextSize(13);
        view.setPadding(dp(16), dp(24), dp(16), dp(16));
        return view;
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }
}
