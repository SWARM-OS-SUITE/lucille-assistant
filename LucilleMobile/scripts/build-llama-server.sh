#!/bin/sh
# Builds llama.cpp's llama-server for the phones (arm64 Android, Cortex-A55: dotprod + fp16, no i8mm)
# and drops it into jniLibs as libllama_server.so, next to the Go server. Needs the NDK and a llama.cpp
# checkout; defaults match this PC's toolchain folder. Models are NOT bundled: see OnDeviceModel.kt.
set -eu
T=${TOOLCHAIN:-/mnt/lucilleassistant/toolchain}
NDK=${NDK:-$(ls -d "$T"/android-ndk-* | head -1)}
SRC=${LLAMA_SRC:-$T/llama.cpp}
ARCH=armv8.2-a+dotprod+fp16
cmake -S "$SRC" -B "$SRC/build-android" -G Ninja \
  -DCMAKE_TOOLCHAIN_FILE="$NDK/build/cmake/android.toolchain.cmake" -DANDROID_ABI=arm64-v8a -DANDROID_PLATFORM=android-28 \
  -DCMAKE_BUILD_TYPE=Release -DGGML_NATIVE=OFF -DGGML_CPU_ARM_ARCH=$ARCH \
  -DCMAKE_C_FLAGS="-march=$ARCH" -DCMAKE_CXX_FLAGS="-march=$ARCH" -DGGML_OPENMP=OFF -DLLAMA_CURL=OFF \
  -DBUILD_SHARED_LIBS=OFF -DLLAMA_BUILD_TESTS=OFF -DLLAMA_BUILD_EXAMPLES=OFF -DLLAMA_BUILD_SERVER=ON
cmake --build "$SRC/build-android" --target llama-server -j"$(nproc)"
OUT="$(dirname "$0")/../app/src/main/jniLibs/arm64-v8a"
mkdir -p "$OUT"
cp "$SRC/build-android/bin/llama-server" "$OUT/libllama_server.so"
"$NDK"/toolchains/llvm/prebuilt/linux-x86_64/bin/llvm-strip "$OUT/libllama_server.so"
