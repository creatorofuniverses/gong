#!/usr/bin/env bash

# These arrays are consumed by scripts that source this manifest.
# shellcheck disable=SC2034
RELEASE_TARGETS=(
    linux/amd64
    linux/arm64
    darwin/amd64
    darwin/arm64
)

# shellcheck disable=SC2034
RELEASE_PAYLOAD_FILES=(
    LICENSE
    gong.example.yaml
    README.md
    assets/gong-light.png
    assets/gong-dark.png
    docs/recipes.md
    docs/bot.md
    docs/chat-id.md
    docs/installation.md
    docs/configuration.md
    docs/background.md
    docs/usage.md
    docs/python.md
    docs/topics.md
    docs/architecture.md
    deploy/gong.service
    examples/python/gong.py
    examples/python/recipes.py
    examples/python/test_gong.py
    examples/shell/gong.bash
    examples/shell/gong.zsh
    examples/shell/gong.fish
)

readonly RELEASE_TARGETS RELEASE_PAYLOAD_FILES
