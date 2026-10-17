package com.sta1n.qrpdsh;

/**
 * The bridge to the Go harness.
 *
 * The harness is compiled as a shared library and loaded here. It runs the
 * whole turn on the calling thread, so callers MUST NOT call it from the UI
 * thread: a turn performs network I/O and can run for minutes.
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

    /**
     * Runs one harness turn and returns its result as JSON.
     *
     * <p>{@code home} is the directory the harness owns: session logs and the
     * default working directory live under it. It must be a path the app can
     * write, which on Android means its private files directory — the harness
     * cannot derive that itself, and /tmp is not writable by an app.
     *
     * <p>{@code nodePath} is the embedded node executable extracted by
     * {@link NodeRuntime}, or {@code null} when the runtime is unavailable.
     * A null path disables the JS layer for the turn: config expressions are
     * refused and npm plugins stay dormant. The harness runs without it.
     *
     * <p>Blocking. Call from a worker thread.
     */
    public static native String runTurn(String task, String home, String workDir,
                                        String baseUrl, String apiKey, String model,
                                        String nodePath, String sessionId);

    /**
     * Opens (or resumes) the newest session under {@code home} and returns a
     * JSON document: {@code {"id": "...", "events": [...]}}. The events are
     * the conversation recorded so far, for the UI to replay.
     */
    public static native String openSession(String home);

    /** Closes one session, flushing its log to disk. */
    public static native void closeSession(String id);

    /**
     * Blocks up to {@code timeoutMs} waiting for one live event from the
     * session's running turn. Returns the event as JSON, or "" when the
     * window expired (poll again) or the stream ended (turn finished).
     * MUST be called off the UI thread; it blocks.
     */
    public static native String pollEvent(String sessionId, int timeoutMs);

    /** Lists every session under home, newest first. Rows mirror the
     * official SessionSummary: id/title/blank/running/updatedAt. */
    public static native String listSessions(String home);

    /** Opens one session by id and returns its replay document
     * ({@code {"id","events":[...]}}), or {@code {"error"}}. */
    public static native String openSessionId(String home, String id);

    /** Starts a new blank session (the official startSession) and returns
     * its replay document. */
    public static native String createSession(String home);
}
