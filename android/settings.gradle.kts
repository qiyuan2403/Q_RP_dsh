// Android build for the Q_RP_dsh client.
//
// WHY GRADLE. The hand-rolled javac/d8 chain worked while the UI was six
// Java files. Jetpack Compose needs the Kotlin compiler plugin, the
// resource linker, and a dependency resolver; reproducing that by hand is
// a build system, and a bad one. Gradle + AGP is the supported path.
//
// MIRROR. dl.google.com is unreachable from this build host (verified), so
// every repository list pins the Aliyun mirrors first. They carry the same
// artifacts; a mirror lag shows up as a missing version, never a wrong one.

pluginManagement {
    repositories {
        maven("https://maven.aliyun.com/repository/google")
        maven("https://maven.aliyun.com/repository/public")
        maven("https://maven.aliyun.com/repository/gradle-plugin")
        google()
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.PREFER_SETTINGS)
    repositories {
        maven("https://maven.aliyun.com/repository/google")
        maven("https://maven.aliyun.com/repository/public")
        google()
        mavenCentral()
    }
}

rootProject.name = "Q_RP_dsh"
include(":app")
