plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "xyz.vpavlin.shrooms"
    compileSdk = 35

    defaultConfig {
        applicationId = "xyz.vpavlin.shrooms"
        // The prebuilt liblogosdelivery is compiled against API 24, so nothing
        // older can load it.
        minSdk = 26
        targetSdk = 35
        // F-Droid orders by versionCode, not by name, so a publish with a code
        // that is not higher than the last is silently ignored. The publish
        // script supplies it; the default is only for local builds.
        versionCode = (project.findProperty("versionCode") as String?)?.toInt() ?: 1
        versionName = (project.findProperty("versionName") as String?) ?: "0.1-prototype"

        // arm64 only. There is no x86_64 liblogosdelivery, so an emulator has
        // no node — building the other ABIs would produce an APK that installs
        // and then cannot start.
        ndk { abiFilters += "arm64-v8a" }
    }

    // Shrooms Agents' own key (docs/agents.md). Never in the repository:
    // whoever holds it can ship an update to an app that drives your agents.
    // The build is handed it by scripts/build-agents-apk.sh, read-only, from
    // ~/apk-signing/shrooms-agents; without it the agents build is unsigned.
    val agentsKey = System.getenv("AGENTS_KEYSTORE")?.let { file(it) }?.takeIf { it.exists() }
    signingConfigs {
        if (agentsKey != null) {
            create("agents") {
                storeFile = agentsKey
                storePassword = file(System.getenv("AGENTS_KEYSTORE_PASSWORD_FILE")).readText().trim()
                keyAlias = "shrooms-agents"
                keyPassword = storePassword
                storeType = "pkcs12"
                // v1 (JAR) beside v2: no Android this app runs on needs it
                // (minSdk 26), but the publisher's check verifies against
                // minSdk 19 and refuses an APK without it (2026-10-08).
                enableV1Signing = true
                enableV2Signing = true
            }
        }
    }

    buildTypes {
        debug { isMinifyEnabled = false }
        // Deliberately unsigned. The release key lives on the F-Droid host and
        // does not leave it; scripts/publish-fdroid.sh signs there. A
        // debug-signed APK would also be skipped by `fdroid update`.
        release { isMinifyEnabled = false }
        // Shrooms Agents: a separate app built from the same code — its own
        // package, name, icon (src/agents/res) and key — that opens into the
        // Agents screens and never touches the VPN, which stays with the
        // shrooms app. Separate because the two change at different speeds
        // and carry different permissions (docs/agents.md).
        // Build: scripts/build-agents-apk.sh
        create("agents") {
            initWith(getByName("release"))
            applicationIdSuffix = ".agents"
            signingConfig = signingConfigs.findByName("agents")
            // -Pdebuggable=true: a copy to inspect in an emulator (run-as,
            // its prefs). Never published.
            isDebuggable = project.findProperty("debuggable") == "true"
            matchingFallbacks += "release"
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    // BuildConfig for the version, so a screenshot or a glance says which build
    // is installed — the question that comes up every time something is fixed.
    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        // The native libraries must stay uncompressed and page-aligned, or the
        // loader maps them slowly and, on some devices, not at all.
        jniLibs { useLegacyPackaging = false }
    }
}

dependencies {
    // Built by `make aar` from the Go core.
    implementation(files("../logosvpn.aar"))

    implementation(platform("androidx.compose:compose-bom:2024.12.01"))
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.lifecycle:lifecycle-runtime-compose:2.8.7")
    implementation("androidx.lifecycle:lifecycle-service:2.8.7")
    implementation("androidx.core:core-ktx:1.15.0")
    // Scanning a network key from a laptop screen. zxing-embedded rather than
    // ML Kit: it is a few hundred KB against several MB, and this APK already
    // carries a 27MB node.
    implementation("com.journeyapps:zxing-android-embedded:4.3.0")

    debugImplementation("androidx.compose.ui:ui-tooling")

    // JVM unit tests. org.json in a JVM test is Android's stub, which throws on
    // every call; the real one stands in for it.
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.json:json:20240303")
}
