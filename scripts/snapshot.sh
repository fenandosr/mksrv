#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

version="${VERSION:-dev}"
commit="${COMMIT:-unknown}"
date="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
dist="$repo_root/dist"
rm -rf "$dist"
mkdir -p "$dist"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

ldflags="-s -w -X main.version=$version -X main.commit=$commit -X main.date=$date"
targets=(
  "linux amd64"
  "linux arm64"
  "darwin amd64"
  "darwin arm64"
  "windows amd64"
)

for target in "${targets[@]}"; do
  read -r goos goarch <<<"$target"
  package="mksrv_${version}_${goos}_${goarch}"
  package_dir="$work/$package"
  mkdir -p "$package_dir"
  binary="mksrv"
  if [[ "$goos" == "windows" ]]; then
    binary="mksrv.exe"
  fi
  echo "==> building $goos/$goarch" >&2
  CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
    go build -trimpath -ldflags "$ldflags" -o "$package_dir/$binary" ./cmd/mksrv
  cp LICENSE README.md "$package_dir/"

  if [[ "$goos" == "windows" ]]; then
    python3 - "$package_dir" "$dist/$package.zip" <<'PY'
import pathlib
import sys
import zipfile
source = pathlib.Path(sys.argv[1])
destination = pathlib.Path(sys.argv[2])
with zipfile.ZipFile(destination, 'w', compression=zipfile.ZIP_DEFLATED, compresslevel=9) as archive:
    for path in sorted(source.rglob('*')):
        if path.is_file():
            archive.write(path, pathlib.Path(source.name) / path.relative_to(source))
PY
  else
    tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner \
      -C "$work" -czf "$dist/$package.tar.gz" "$package"
  fi
done

# Engine-only archive, mirroring the GoReleaser meta archive.
tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 --numeric-owner \
  -czf "$dist/engine.tar.gz" infra stacks schemas

(
  cd "$dist"
  sha256sum mksrv_* engine.tar.gz > checksums.txt
)

printf 'snapshot artifacts:\n' >&2
find "$dist" -maxdepth 1 -type f -printf '  %f\n' | sort >&2
