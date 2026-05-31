#!/usr/bin/env bash
# Build reasonix for every target, bundle the matching CodeGraph runtime beside
# the binary on the platforms where its layout is verified (darwin/linux), and
# package per-platform archives + SHA256SUMS into dist/. Driven by the Release
# workflow on a v* tag; runnable locally with VERSION set (needs `gh` auth).
set -euo pipefail

VERSION="${VERSION:?set VERSION, e.g. v1.0.0}"
CODEGRAPH_VERSION="${CODEGRAPH_VERSION:-v0.9.7}"
CG_REPO="colbymchenry/codegraph"
LDFLAGS="-s -w -X main.version=${VERSION}"

# All reasonix targets. CodeGraph is bundled only on the platforms listed in
# bundle_cg below; the rest ship a bare binary (codegraph then resolves from PATH,
# or the feature reports unavailable).
TARGETS="darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64 windows/arm64"

# bundle_cg reports whether to ship the CodeGraph bundle for a GOOS. darwin/linux
# use a relocatable POSIX-sh launcher (verified). windows ships a zip with a
# .cmd/.exe shim whose relocated layout is not yet verified — bundle it only after
# confirming, so we don't ship a binary that can't find its runtime.
bundle_cg() { case "$1" in darwin | linux) return 0 ;; *) return 1 ;; esac; }

# cg_asset prints CodeGraph's release asset name for a GOOS/GOARCH (it uses
# win32 + x64 naming; darwin/linux ship .tar.gz, windows ships .zip).
cg_asset() {
  local os=$1 arch=$2 cgos cgarch ext
  case "$os" in darwin) cgos=darwin ;; linux) cgos=linux ;; windows) cgos=win32 ;; esac
  case "$arch" in amd64) cgarch=x64 ;; arm64) cgarch=arm64 ;; esac
  ext=tar.gz
  [ "$os" = windows ] && ext=zip
  echo "codegraph-${cgos}-${cgarch}.${ext}"
}

rm -rf dist stage
mkdir -p dist stage

# CodeGraph's published checksums, used to verify each bundle (the upstream
# installer skips this, so we own the integrity gate).
gh release download "$CODEGRAPH_VERSION" -R "$CG_REPO" -p SHA256SUMS -O stage/CG_SHA256SUMS

for t in $TARGETS; do
  os=${t%/*}
  arch=${t#*/}
  name="reasonix-${os}-${arch}"
  stagedir="stage/${name}"
  ext=""
  [ "$os" = windows ] && ext=".exe"

  echo "==> build ${name}"
  mkdir -p "$stagedir"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
    go build -ldflags "$LDFLAGS" -o "${stagedir}/reasonix${ext}" ./cmd/reasonix

  if bundle_cg "$os"; then
    asset=$(cg_asset "$os" "$arch")
    echo "==> bundle CodeGraph: ${asset}"
    gh release download "$CODEGRAPH_VERSION" -R "$CG_REPO" -p "$asset" -O "stage/${asset}"
    (cd stage && grep " ${asset}\$" CG_SHA256SUMS | sha256sum -c -)
    mkdir -p "${stagedir}/codegraph"
    tar -xzf "stage/${asset}" -C "${stagedir}/codegraph" --strip-components=1
  else
    echo "==> skip CodeGraph bundle for ${os} (layout not yet verified)"
  fi

  echo "==> package ${name}"
  if [ "$os" = windows ]; then
    (cd stage && zip -qr "../dist/${name}.zip" "$name")
  else
    tar -czf "dist/${name}.tar.gz" -C stage "$name"
  fi
done

echo "==> checksums"
(cd dist && sha256sum reasonix-*.* >SHA256SUMS)
ls -la dist
