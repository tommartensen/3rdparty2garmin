#!/usr/bin/env bash
# Download a 3rdparty2garmin release binary for this platform.
#
# Usage: ./download.sh <tag> [output-path]
#   tag          release tag, e.g. v1.2.0
#   output-path  where to put the binary (default: ./3rdparty2garmin)
#
# Example:
#   ./download.sh v1.2.0 /usr/local/bin/3rdparty2garmin

set -euo pipefail

repo="tommartensen/3rdparty2garmin"

if [ $# -lt 1 ]; then
    echo "usage: $0 <tag> [output-path]" >&2
    exit 1
fi
tag="$1"
out="${2:-3rdparty2garmin}"

case "$(uname -s)" in
    Linux)  os=linux  ;;
    Darwin) os=darwin ;;
    *) echo "error: unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
    x86_64)          arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "error: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

file="3rdparty2garmin_${tag}_${os}_${arch}"
base_url="https://github.com/${repo}/releases/download/${tag}"

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

echo ">> downloading ${file}"
if ! curl -fsSL -o "${tmpdir}/${file}" "${base_url}/${file}"; then
    echo "error: download failed — check the tag at https://github.com/${repo}/releases" >&2
    exit 1
fi

# Verify against the release checksums when a checksum tool is available.
sum_cmd=""
if command -v sha256sum >/dev/null 2>&1; then
    sum_cmd="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    sum_cmd="shasum -a 256"
fi
if [ -n "$sum_cmd" ] && curl -fsSL -o "${tmpdir}/checksums.txt" "${base_url}/checksums.txt"; then
    # Checksum lines may carry a build-path prefix (e.g. "dist/"), so match
    # on the filename suffix and compare hashes directly.
    expected="$(grep "${file}\$" "${tmpdir}/checksums.txt" | awk '{print $1}')"
    actual="$($sum_cmd "${tmpdir}/${file}" | awk '{print $1}')"
    if [ -z "$expected" ] || [ "$expected" != "$actual" ]; then
        echo "error: checksum mismatch for ${file}" >&2
        exit 1
    fi
    echo ">> checksum verified"
fi

chmod +x "${tmpdir}/${file}"
mv "${tmpdir}/${file}" "$out"
echo ">> saved to ${out}"
