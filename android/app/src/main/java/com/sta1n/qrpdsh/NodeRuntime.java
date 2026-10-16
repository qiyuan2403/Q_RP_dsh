package com.sta1n.qrpdsh;

import android.content.Context;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.util.zip.ZipEntry;
import java.util.zip.ZipInputStream;

/**
 * Materializes the embedded Node runtime into the app's private directory.
 *
 * The APK carries the runtime in two forms: the node binary rides in jniLibs
 * as a disguised shared object ({@code libnodebin.so}, because Android only
 * extracts that directory), and the runtime's dynamic libraries ride as a
 * compressed tarball in assets. On first use (and after every app update,
 * because the extraction is versioned by the installed APK's own directory)
 * both are copied under {@code filesDir/node}, where the app may execute and
 * write.
 *
 * The {@code chmod 700} matters: the copied binary must be executable by this
 * app's uid, and a private-files executable works on every Android release
 * this project targets (verified on corot/Android 15 and PHJ110/Android 13).
 */
final class NodeRuntime {
    private NodeRuntime() {}

    /** Returns the node executable path, extracting the runtime if needed. */
    static String materialize(Context context) throws IOException {
        File nodeDir = new File(context.getFilesDir(), "node");
        File bin = new File(nodeDir, "bin/node");
        File stamp = new File(nodeDir, ".ready");

        if (stamp.exists()) return bin.getAbsolutePath();

        File binDir = bin.getParentFile();
        File libDir = new File(nodeDir, "lib");
        if (!binDir.isDirectory() && !binDir.mkdirs()) {
            throw new IOException("cannot create " + binDir);
        }
        if (!libDir.isDirectory() && !libDir.mkdirs()) {
            throw new IOException("cannot create " + libDir);
        }

        copyFromNativeLibDir(context, bin);
        extractLibs(context, nodeDir);

        if (!bin.setExecutable(true, true)) {
            throw new IOException("cannot chmod node executable: " + bin);
        }
        if (!stamp.createNewFile()) {
            throw new IOException("cannot write node stamp: " + stamp);
        }
        return bin.getAbsolutePath();
    }

    /** Copies the disguised .so out of nativeLibraryDir as bin/node. */
    private static void copyFromNativeLibDir(Context context, File target) throws IOException {
        String dir = context.getApplicationInfo().nativeLibraryDir;
        File src = new File(dir, "libnodebin.so");
        copy(src, target);
    }

    /**
     * Extracts the libs tarball from assets into nodeDir.
     *
     * The asset is staged to a temp file first: toybox tar reads a gzip
     * stream from stdin unreliably ("zcat: not gzip" seen on Android 15),
     * but reads a real file fine. The tarball carries the lib/ prefix
     * itself, so it extracts straight into the node root.
     */
    private static void extractLibs(Context context, File nodeDir) throws IOException {
        File staged = new File(context.getCacheDir(), "nodebundle_libs.tgz");
        try (InputStream in = context.getAssets().open("nodebundle_libs.tgz");
             OutputStream out = new FileOutputStream(staged)) {
            copyStream(in, out);
        }
        Process p = new ProcessBuilder("tar", "xzf", staged.getAbsolutePath(),
                "-C", nodeDir.getAbsolutePath()).start();
        try {
            int rc = p.waitFor();
            if (rc != 0) {
                throw new IOException("tar extraction failed: rc=" + rc);
            }
        } catch (InterruptedException e) {
            Thread.currentThread().interrupt();
            throw new IOException("tar extraction interrupted", e);
        } finally {
            //noinspection ResultOfMethodCallIgnored
            staged.delete();
        }
    }

    private static void copy(File src, File dst) throws IOException {
        try (InputStream in = new java.io.FileInputStream(src);
             OutputStream out = new FileOutputStream(dst)) {
            copyStream(in, out);
        }
    }

    private static void copyStream(InputStream in, OutputStream out) throws IOException {
        byte[] buf = new byte[64 * 1024];
        int n;
        while ((n = in.read(buf)) > 0) {
            out.write(buf, 0, n);
        }
    }
}
