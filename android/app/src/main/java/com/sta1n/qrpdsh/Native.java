package com.sta1n.qrpdsh;

/**
 * The bridge to the Go harness.
 *
 * The harness is compiled as a shared library and loaded here. It is not called
 * yet: this class exists so the loading path is exercised, which is what proves
 * the library was packaged correctly and the ABI matches.
 */
public final class Native {
    private static boolean loaded;
    private static String loadError;

    static {
        try {
            System.loadLibrary("qrpdsh");
            loaded = true;
        } catch (UnsatisfiedLinkError e) {
            // Recorded rather than thrown: a missing library must surface in the
            // UI as a message, not as a crash on activity start.
            loadError = e.getMessage();
            loaded = false;
        }
    }

    private Native() {}

    public static boolean isLoaded() { return loaded; }

    public static String loadError() { return loadError; }

    /** Returns the harness version string reported by the Go side. */
    public static native String version();

    /** Runs one harness turn with the given task and returns its output. */
    public static native String runTurn(String task, String workDir, String baseUrl, String apiKey, String model);
}
