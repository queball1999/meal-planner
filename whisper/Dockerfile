# syntax=docker/dockerfile:1
#
# whisper.cpp server for Go Eat's video recipe import (phase 16) - the
# `whisper` service in docker-compose.yml. Go Eat posts each video's audio to
# http://whisper:8081/inference.
#
# Built from source rather than using ghcr.io/ggml-org/whisper.cpp: that image
# bakes in the CPU features of the machine that built it and can exit with
# SIGILL (132) on a CPU without AVX-512 (seen in QSS/audio-transcription-
# server). This image is built once in CI and run anywhere, so instead of
# targeting the build host (GGML_NATIVE) it ships every CPU variant ggml knows
# (GGML_CPU_ALL_VARIANTS, loaded at runtime via GGML_BACKEND_DL) and picks the
# best one for whatever CPU it lands on - how upstream's own release zips are
# built.
#
# Built per architecture on a native runner (whisper_image.yaml); compiling
# C++ under QEMU takes far too long.

ARG UBUNTU=ubuntu:24.04

FROM ${UBUNTU} AS build
RUN apt-get update && apt-get install -y --no-install-recommends \
        build-essential cmake git ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Same whisper.cpp release the desktop/server installer downloads
# (video/install.go), so every install transcribes the same way.
ARG WHISPER_CPP_VERSION=v1.9.4
WORKDIR /src
RUN git clone --depth 1 --branch "${WHISPER_CPP_VERSION}" https://github.com/ggml-org/whisper.cpp.git .

RUN cmake -B build \
        -DCMAKE_BUILD_TYPE=Release \
        -DBUILD_SHARED_LIBS=ON \
        -DGGML_NATIVE=OFF \
        -DGGML_BACKEND_DL=ON \
        -DGGML_CPU_ALL_VARIANTS=ON \
        -DWHISPER_BUILD_TESTS=OFF \
        -DWHISPER_SDL2=OFF \
    && cmake --build build --config Release -j"$(nproc)" \
    && mkdir -p /out \
    && cp build/bin/whisper-server /out/ \
    && find build -name '*.so*' -exec cp -P {} /out/ \;

FROM ${UBUNTU}
# libgomp: ggml's CPU backend uses OpenMP threads.
RUN apt-get update && apt-get install -y --no-install-recommends libgomp1 \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd -r -g 10002 whisper \
    && useradd -r -u 10002 -g whisper -d /opt/whisper -s /usr/sbin/nologin whisper

# Binary and every library side by side: ggml looks for its CPU-variant
# backends next to the executable.
COPY --from=build /out/ /opt/whisper/
COPY entrypoint.sh /opt/whisper/entrypoint.sh
RUN chmod 0755 /opt/whisper/entrypoint.sh /opt/whisper/whisper-server
ENV LD_LIBRARY_PATH=/opt/whisper \
    WHISPER_MODELS_DIR=/models \
    WHISPER_PORT=8081
WORKDIR /opt/whisper

# Models come from Go Eat (Settings → AI Setup) on a shared volume; this
# container only reads them.
VOLUME ["/models"]
EXPOSE 8081
USER whisper:whisper
ENTRYPOINT ["/opt/whisper/entrypoint.sh"]
