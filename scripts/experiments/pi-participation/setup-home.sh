#!/bin/sh
# Make an isolated pi home that holds only the local-model config (placeholder keys, no real secrets).
set -eu
H=$1
mkdir -p "$H/sessions"
cp "$HOME/.pi/agent/settings.json" "$HOME/.pi/agent/models.json" "$H/"
