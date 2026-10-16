package com.sta1n.qrpdsh;

import android.app.Activity;
import android.os.Bundle;
import android.widget.TextView;
import android.util.Log;

/**
 * The Android entry point.
 *
 * The harness itself is a Go binary. It is not called from here yet: this
 * activity exists to prove the APK builds, installs, and runs, which is the
 * prerequisite for wiring the binary in.
 */
public class MainActivity extends Activity {
    private static final String TAG = "qrpdsh";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);

        StringBuilder report = new StringBuilder();
        report.append("Q_RP_dsh\n\n");
        report.append("nativeLibraryDir:\n").append(getApplicationInfo().nativeLibraryDir).append("\n\n");
        report.append("filesDir:\n").append(getFilesDir().getAbsolutePath()).append("\n\n");

        if (Native.isLoaded()) {
            String version;
            try {
                version = Native.version();
            } catch (Throwable t) {
                version = "call failed: " + t;
            }
            report.append("Go harness: loaded\nversion: ").append(version).append("\n");
        } else {
            report.append("Go harness: NOT loaded\n").append(Native.loadError()).append("\n");
        }

        TextView view = new TextView(this);
        view.setText(report.toString());
        view.setTextSize(13);
        view.setPadding(40, 80, 40, 40);
        setContentView(view);

        Log.i(TAG, report.toString());
    }
}
