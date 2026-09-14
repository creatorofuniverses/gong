#!/usr/bin/env bash
set -euo pipefail

export GOTOOLCHAIN=go1.26.8

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PROJECT_ROOT
cd "$PROJECT_ROOT"

# shellcheck source=release-manifest.sh
source "$PROJECT_ROOT/scripts/release-manifest.sh"

if [[ -z "${VERSION:-}" ]]; then
    printf '%s\n' 'VERSION is required (for example, VERSION=v0.1.0)' >&2
    exit 2
fi
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    printf 'invalid VERSION: %s\n' "$VERSION" >&2
    exit 2
fi

for required in "${RELEASE_PAYLOAD_FILES[@]}"; do
    if [[ ! -f "$required" ]]; then
        printf 'required release file is missing: %s\n' "$required" >&2
        exit 1
    fi
done

readonly VERSION_SYMBOL='github.com/creatorofuniverses/gong/internal/cli.Version'
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gong-release.XXXXXXXX")"
readonly WORK_DIR
trap 'rm -rf "$WORK_DIR"' EXIT

mkdir -p dist "$WORK_DIR/output"
archives=()

for target in "${RELEASE_TARGETS[@]}"; do
    goos=${target%/*}
    goarch=${target#*/}
    archive="gong_${VERSION}_${goos}_${goarch}.tar.gz"
    package_dir="$WORK_DIR/package-${goos}-${goarch}"
    archives+=("$archive")

    mkdir -p "$package_dir"

    GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 \
        go build -buildvcs=false -trimpath \
        -ldflags "-s -w -X ${VERSION_SYMBOL}=${VERSION}" \
        -o "$package_dir/gong" ./cmd/gong

    for payload_file in "${RELEASE_PAYLOAD_FILES[@]}"; do
        install -D -m 0644 "$payload_file" "$package_dir/$payload_file"
    done

    tar -czf "$WORK_DIR/output/$archive" -C "$package_dir" \
        gong "${RELEASE_PAYLOAD_FILES[@]}"

    # Stable asset names make /releases/latest/download usable without first
    # querying GitHub for a version. They are byte-identical to versioned files.
    alias="gong_${goos}_${goarch}.tar.gz"
    cp "$WORK_DIR/output/$archive" "$WORK_DIR/output/$alias"
    archives+=("$alias")
done

(
    cd "$WORK_DIR/output"
    sha256sum "${archives[@]}" > SHA256SUMS
)

for archive in "${archives[@]}"; do
    mv -f "$WORK_DIR/output/$archive" dist/
done
mv -f "$WORK_DIR/output/SHA256SUMS" dist/

printf 'Release artifacts for %s written to %s/dist\n' "$VERSION" "$PROJECT_ROOT"
