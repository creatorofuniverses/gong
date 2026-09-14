set shell := ["bash", "-euo", "pipefail", "-c"]
export GOTOOLCHAIN := "go1.26.8"

# Show available development commands.
default:
    @just --list

# Build the single static executable.
build:
    mkdir -p bin
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -o bin/gong ./cmd/gong

test:
    go test ./...

test-race:
    go test -race ./...

vet:
    go vet ./...

check-python:
    python3 -m unittest discover -s examples/python

check-shell:
    bash -n examples/shell/gong.bash
    fish -n examples/shell/gong.fish
    zsh -n examples/shell/gong.zsh

# Build four-platform archives, e.g. just release v0.1.0.
release version:
    VERSION={{quote(version)}} bash scripts/build-release.sh

# Check the complete release payload and static binaries.
verify-release version:
    VERSION={{quote(version)}} bash scripts/verify-release.sh
