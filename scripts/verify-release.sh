#!/usr/bin/env bash
set -euo pipefail

export GOTOOLCHAIN=go1.26.8

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
readonly PROJECT_ROOT
cd "$PROJECT_ROOT"

# shellcheck source=release-manifest.sh
source "$PROJECT_ROOT/scripts/release-manifest.sh"

fail() {
    printf 'release verification failed: %s\n' "$*" >&2
    exit 1
}

if [[ -z "${VERSION:-}" ]]; then
    printf '%s\n' 'VERSION is required (for example, VERSION=v0.1.0)' >&2
    exit 2
fi
if [[ ! "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z][0-9A-Za-z.-]*)?$ ]]; then
    printf 'invalid VERSION: %s\n' "$VERSION" >&2
    exit 2
fi

readonly DIST_DIR="${DIST_DIR:-$PROJECT_ROOT/dist}"
readonly CHECKSUM_FILE="$DIST_DIR/SHA256SUMS"
[[ -f "$CHECKSUM_FILE" ]] || fail "missing $CHECKSUM_FILE"

expected_archives=()
declare -A expected_archive_set=()
for target in "${RELEASE_TARGETS[@]}"; do
    goos=${target%/*}
    goarch=${target#*/}
    archive="gong_${VERSION}_${goos}_${goarch}.tar.gz"
    expected_archives+=("$archive")
    expected_archive_set["$archive"]=1
    alias="gong_${goos}_${goarch}.tar.gz"
    expected_archives+=("$alias")
    expected_archive_set["$alias"]=1
done

mapfile -t checksum_lines < "$CHECKSUM_FILE"
(( ${#checksum_lines[@]} == ${#expected_archives[@]} )) || \
    fail "SHA256SUMS must contain exactly ${#expected_archives[@]} entries"

declare -A checksum_seen=()
for line in "${checksum_lines[@]}"; do
    if [[ ! "$line" =~ ^[[:xdigit:]]{64}[[:space:]][[:space:]]([^[:space:]]+)$ ]]; then
        fail "malformed SHA256SUMS entry: $line"
    fi
    archive=${BASH_REMATCH[1]}
    [[ -n "${expected_archive_set[$archive]+present}" ]] || \
        fail "unexpected SHA256SUMS entry: $archive"
    (( ${checksum_seen[$archive]:-0} == 0 )) || \
        fail "duplicate SHA256SUMS entry: $archive"
    checksum_seen["$archive"]=1
done
for archive in "${expected_archives[@]}"; do
    (( ${checksum_seen[$archive]:-0} == 1 )) || \
        fail "missing SHA256SUMS entry: $archive"
done

(
    cd "$DIST_DIR"
    sha256sum --check --strict SHA256SUMS
)

WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/gong-verify.XXXXXXXX")"
readonly WORK_DIR
trap 'rm -rf "$WORK_DIR"' EXIT

expected_members=(gong "${RELEASE_PAYLOAD_FILES[@]}")
declare -A expected_member_set=()
for member in "${expected_members[@]}"; do
    expected_member_set["$member"]=1
done

host_os=$(go env GOHOSTOS)
host_arch=$(go env GOHOSTARCH)
host_smoked=false

for target in "${RELEASE_TARGETS[@]}"; do
    goos=${target%/*}
    goarch=${target#*/}
    archive="gong_${VERSION}_${goos}_${goarch}.tar.gz"
    archive_path="$DIST_DIR/$archive"
    target_dir="$WORK_DIR/${goos}_${goarch}"
    member_file="$WORK_DIR/${goos}_${goarch}.members"
    verbose_file="$WORK_DIR/${goos}_${goarch}.verbose"
    metadata_file="$WORK_DIR/${goos}_${goarch}.metadata"

    [[ -f "$archive_path" ]] || fail "missing archive: $archive"
    cmp "$archive_path" "$DIST_DIR/gong_${goos}_${goarch}.tar.gz" >/dev/null || \
        fail "stable archive alias differs from $archive"
    tar -tzf "$archive_path" > "$member_file"
    mapfile -t members < "$member_file"
    (( ${#members[@]} == ${#expected_members[@]} )) || \
        fail "$archive has an incorrect member count"

    declare -A member_seen=()
    for member in "${members[@]}"; do
        if [[ "$member" == /* || "$member" == . || "$member" == .. || \
              "$member" == ./* || "$member" == ../* || "$member" == */./* || \
              "$member" == */. || "$member" == */../* || "$member" == */.. ]]; then
            fail "$archive contains unsafe member path: $member"
        fi
        [[ -n "${expected_member_set[$member]+present}" ]] || \
            fail "$archive contains unexpected member: $member"
        (( ${member_seen[$member]:-0} == 0 )) || \
            fail "$archive contains duplicate member: $member"
        member_seen["$member"]=1
    done
    for member in "${expected_members[@]}"; do
        (( ${member_seen[$member]:-0} == 1 )) || \
            fail "$archive is missing member: $member"
    done

    tar -tvzf "$archive_path" > "$verbose_file"
    while IFS= read -r verbose_line; do
        [[ "${verbose_line:0:1}" == - ]] || \
            fail "$archive contains a non-regular member: $verbose_line"
    done < "$verbose_file"

    mkdir -p "$target_dir"
    tar -xzf "$archive_path" -C "$target_dir"
    for payload_file in "${RELEASE_PAYLOAD_FILES[@]}"; do
        cmp "$PROJECT_ROOT/$payload_file" "$target_dir/$payload_file" >/dev/null || \
            fail "$archive payload differs from source: $payload_file"
    done

    go version -m "$target_dir/gong" > "$metadata_file"
    grep -Fqx $'\tbuild\tCGO_ENABLED=0' "$metadata_file" || \
        fail "$archive binary was not built with CGO_ENABLED=0"
    grep -Fqx $'\tbuild\tGOOS='"$goos" "$metadata_file" || \
        fail "$archive binary GOOS does not match $goos"
    grep -Fqx $'\tbuild\tGOARCH='"$goarch" "$metadata_file" || \
        fail "$archive binary GOARCH does not match $goarch"

    if [[ "$goos" == linux ]]; then
        file_output=$(file "$target_dir/gong")
        printf '%s\n' "$file_output"
        [[ "$file_output" == *'statically linked'* ]] || \
            fail "$archive is not statically linked"
        readelf -l "$target_dir/gong" > "$target_dir/readelf.txt"
        if grep -q INTERP "$target_dir/readelf.txt"; then
            fail "$archive contains a dynamically linked Linux binary"
        fi
    fi

    if [[ "$goos" == "$host_os" && "$goarch" == "$host_arch" ]]; then
        "$target_dir/gong" --help >/dev/null
        [[ "$("$target_dir/gong" version)" == "$VERSION" ]] || \
            fail "$archive binary reports the wrong version"
        host_smoked=true
    fi
done

[[ "$host_smoked" == true ]] || fail "no release target matches host $host_os/$host_arch"
printf 'Release artifacts for %s verified successfully\n' "$VERSION"
