plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "dev.logos.vpn"
    compileSdk = 35

    defaultConfig {
        applicationId = "dev.logos.vpn"
        // The prebuilt liblogosdelivery is compiled against API 24, so nothing
        // older can load it.
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1-prototype"

        // arm64 only. There is no x86_64 liblogosdelivery, so an emulator has
        // no node — building the other ABIs would produce an APK that installs
        // and then cannot start.
        ndk { abiFilters += "arm64-v8a" }
    }

    buildTypes {
        debug { isMinifyEnabled = false }
        release {
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("debug")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    buildFeatures { compose = true }

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
    debugImplementation("androidx.compose.ui:ui-tooling")
}
