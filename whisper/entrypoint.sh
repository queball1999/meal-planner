#!/bin/sh
# Starts whisper-server once Go Eat has downloaded a speech model.
#
# whisper-server exits straight away (code 3) when its model file is missing,
# and on a fresh install nobody has downloaded one yet - so a plain
# `whisper-server -m ...` would restart-loop. This waits for the model
# instead, saying where to get it.
#
# Which model: the one named in $WHISPER_MODELS_DIR/active (written by Go Eat
# when you pick one), else the first ggml-*.bin found. Switching models later
# doesn't need a restart: Go Eat calls the server's /load.
set -eu

dir="${WHISPER_MODELS_DIR:-/models}"
port="${WHISPER_PORT:-8081}"
threads="${WHISPER_THREADS:-4}"

pick_model() {
    if [ -f "$dir/active" ]; then
        name=$(tr -d ' \r\n' < "$dir/active")
        if [ -n "$name" ] && [ -f "$dir/ggml-$name.bin" ]; then
            echo "$dir/ggml-$name.bin"
            return
        fi
    fi
    for f in "$dir"/ggml-*.bin; do
        if [ -f "$f" ]; then
            echo "$f"
            return
        fi
    done
}

waited=0
model=$(pick_model)
while [ -z "$model" ]; do
    if [ $((waited % 60)) -eq 0 ]; then
        echo "whisper: no speech model in $dir yet - download one in Go Eat: Settings > AI Setup > Video recipe import. Waiting..."
    fi
    sleep 5
    waited=$((waited + 5))
    model=$(pick_model)
done

echo "whisper: starting with $model"
exec /opt/whisper/whisper-server \
    --no-gpu \
    -m "$model" \
    -t "$threads" \
    --host 0.0.0.0 \
    --port "$port" \
    "$@"
