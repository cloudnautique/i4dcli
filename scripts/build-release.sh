#!/usr/bin/env bash
# Cross-compile on a Linux host; no target OS, C compiler, or QEMU is required.
set -euo pipefail

if [[ $# -lt 3 || $# -gt 4 ]]; then
  echo 'Usage: scripts/build-release.sh VERSION darwin|linux|windows amd64|arm64 [OUTPUT_DIR]' >&2
  exit 2
fi
version=$1
target_os=$2
target_arch=$3
output_dir=${4:-dist}

# Keep tag-derived paths predictable, including prerelease and build metadata.
if [[ ! "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$ ]]; then
  echo 'Expected a version tag such as v1.2.3 or v1.2.3-rc.1' >&2
  exit 2
fi
case "$target_os" in
  darwin|linux|windows) ;;
  *) echo "Unsupported target OS: $target_os" >&2; exit 2 ;;
esac
case "$target_arch" in
  amd64) archive_arch=x86_64 ;;
  arm64) archive_arch=arm64 ;;
  *) echo "Unsupported target architecture: $target_arch" >&2; exit 2 ;;
esac

mkdir -p "$output_dir"
output_dir=$(cd "$output_dir" && pwd)
build_dir=$(mktemp -d)
trap 'rm -r -- "$build_dir"' EXIT
binary=i4d
if [[ "$target_os" == windows ]]; then
  binary=i4d.exe
fi
CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" GOAMD64=v1 GOARM64=v8.0 \
  go build -mod=readonly -trimpath -ldflags='-s -w' -o "$build_dir/$binary" ./cmd/i4d
cp README.md "$build_dir/README.md"
archive="$output_dir/i4d_${version}_${target_os}_${archive_arch}"
if [[ "$target_os" == windows ]]; then
  # -1 prioritizes packaging speed; upload-artifact does not recompress it.
  (cd "$build_dir" && zip -1 -q package.zip "$binary" README.md)
  mv "$build_dir/package.zip" "$archive.zip"
else
  tar -C "$build_dir" -cf - "$binary" README.md | gzip -1 > "$build_dir/package.tar.gz"
  mv "$build_dir/package.tar.gz" "$archive.tar.gz"
fi
