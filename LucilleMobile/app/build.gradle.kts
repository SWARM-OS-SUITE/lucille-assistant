import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
}

// Release signing — keystore.properties is gitignored (it holds the keystore
// password); generated once via keytool, not committed. Falls back to no
// signing config if the file is missing, so a clean checkout still builds a
// debug APK without it.
val keystoreProps = Properties().apply {
    val f = rootProject.file("keystore.properties")
    if (f.exists()) f.inputStream().use { load(it) }
}

// Cross-compiles Lucille Assistant's own Go server (../LucilleAssistant, the
// SAME source as the SWARM OS / Debian service) for Android arm64 and drops
// it straight into jniLibs. Naming it lib*.so (not a bare executable copied
// into a data dir) is deliberate: Android only lets an app execute files
// from its own installed native-library directory, not from app-private
// storage copied at runtime (W^X enforcement since Android 10). The binary
// is a build artifact, not committed — gitignored, rebuilt every time.
val goServerSrc = File(rootDir, "../LucilleAssistant")
val jniLibsDir = File(projectDir, "src/main/jniLibs/arm64-v8a")

val buildGoServer by tasks.registering(Exec::class) {
    workingDir = goServerSrc
    inputs.dir(goServerSrc)
    outputs.file(File(jniLibsDir, "liblucille_mobile.so"))
    doFirst { jniLibsDir.mkdirs() }
    environment("GOOS", "android")
    environment("GOARCH", "arm64")
    environment("CGO_ENABLED", "0")
    commandLine("go", "build", "-o", File(jniLibsDir, "liblucille_mobile.so").absolutePath, ".")
}

// The shared-memory CLI (swarm-memory, from LucyServer-go) bundled the same
// way, so thread comments the bundled server mirrors into swarm-memory (and
// the periodic `swarm-memory sync` against the user's PC) work on the phone.
// Lives outside this repo on the SWARM build partition, so the task is
// skipped (with the CLI absent at runtime) when that source isn't mounted.
val swarmMemorySrc = File("/mnt/Lucy/30-source/LucyServer-go")

val buildSwarmMemory by tasks.registering(Exec::class) {
    onlyIf { swarmMemorySrc.exists() }
    workingDir = swarmMemorySrc
    outputs.file(File(jniLibsDir, "libswarm_memory.so"))
    doFirst { jniLibsDir.mkdirs() }
    environment("GOOS", "android")
    environment("GOARCH", "arm64")
    environment("CGO_ENABLED", "0")
    commandLine("go", "build", "-o", File(jniLibsDir, "libswarm_memory.so").absolutePath, "./cmd/swarm-memory")
}

tasks.named("preBuild") { dependsOn(buildGoServer, buildSwarmMemory) }

android {
    namespace = "com.lucille.mobile"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.lucille.mobile"
        minSdk = 26
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        ndk {
            abiFilters += "arm64-v8a"
        }
    }

    signingConfigs {
        if (keystoreProps.isNotEmpty()) {
            create("release") {
                storeFile = rootProject.file(keystoreProps["storeFile"] as String)
                storePassword = keystoreProps["storePassword"] as String
                keyAlias = keystoreProps["keyAlias"] as String
                keyPassword = keystoreProps["keyPassword"] as String
            }
        }
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            if (keystoreProps.isNotEmpty()) {
                signingConfig = signingConfigs.getByName("release")
            }
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    // The Go binary is a plain ELF executable named lib*.so to live in the
    // native-library dir, not a real JNI library — stop AGP compressing or
    // stripping it like one.
    packaging {
        jniLibs.useLegacyPackaging = true
    }
}

dependencies {
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.appcompat)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    // Phone-approved sudo: strong biometric gate on the hardware-backed key
    implementation("androidx.biometric:biometric:1.1.0")
    implementation("androidx.fragment:fragment-ktx:1.6.2")
    testImplementation("junit:junit:4.13.2")
}
