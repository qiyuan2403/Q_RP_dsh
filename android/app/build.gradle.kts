// The Android application module.
//
// The Go harness ships as a prebuilt shared library: the JNI build script
// (jni/build.sh) produces libqrpdsh.so with the NDK toolchain, and this
// module packages it as jniLibs. Building Go from Gradle would need the
// same qemu-hosted NDK invocation the script already encodes, for no gain.
//
// The embedded Node runtime rides the same way: the binary as a disguised
// shared object, its libraries as an asset tarball (android/nodebundle.sh
// assembles both).

// AGP 9 carries Kotlin support itself: applying the kotlin.android plugin
// is an error there. The Compose compiler plugin stays explicit.
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "com.sta1n.qrpdsh"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.sta1n.qrpdsh"
        minSdk = 29
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"
    }

    // The Go shared library and the embedded node binary are prebuilt into
    // jni/ by their own scripts; packaging them is this module's job.
    // AGP 9 ignores sourceSets jniLibs srcDirs (verified); the standard
    // app/src/main/jniLibs directory works, so the prebuilt .so files are
    // copied there before Gradle runs. jni/ remains the Go-side output.


    buildTypes {
        release {
            isMinifyEnabled = false
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlin {
        compilerOptions {
            jvmTarget.set(org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17)
        }
    }

    buildFeatures {
        compose = true
        // The embedded node binary and its libs ride in jniLibs and must be
        // extracted to disk: the app execve's node, it never dlopens it.
        packaging { jniLibs { useLegacyPackaging = true } }
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
    }
}

dependencies {
    // BOM pinned to the 2025.01 line: the 2026.09 BOM pulls Compose 1.12,
    // which requires compileSdk 37 (not installed, and this project targets
    // 35). The 2025.01 line resolves to Compose 1.7.x, which compiles
    // against SDK 35.
    val composeBom = platform("androidx.compose:compose-bom:2025.01.00")
    implementation(composeBom)

    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    debugImplementation("androidx.compose.ui:ui-tooling")

    // Streaming markdown: the renderer advertises incremental streaming and
    // full GFM, which is what a chat transcript needs (see ADR 0001).
    implementation("com.mikepenz:multiplatform-markdown-renderer-android:0.34.0")
}
