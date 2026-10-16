// Plugin versions for the Android build.
//
// AGP 8.13 pairs with Gradle 8.x; Gradle 9.x requires AGP 9.x. The host's
// cached wrapper distribution is 9.7.1, so the pair is AGP 9.4.1 + Gradle
// 9.7.1 — the combination the cached toolchain can actually run.
plugins {
    id("com.android.application") version "9.4.1" apply false
    id("org.jetbrains.kotlin.android") version "2.4.21" apply false
    id("org.jetbrains.kotlin.plugin.compose") version "2.4.21" apply false
}
