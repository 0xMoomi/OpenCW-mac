#!/bin/sh
# Builds dist/opencw-mac for Apple Silicon Macs. Like build.ps1 it pins the
# toolchain named in go.mod, the target and no cgo, so one commit always builds
# the same binary.
set -eu
cd "$(dirname "$0")"
command -v go >/dev/null || { echo "Go가 필요합니다: brew install go" >&2; exit 1; }
toolchain=$(sed -n 's/^toolchain \(go[^ ]*\)$/\1/p' go.mod)
GOTOOLCHAIN="$toolchain" GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 \
	go build -trimpath -buildvcs=false -tags with_utls,with_quic \
	-ldflags "-s -w" -o dist/opencw-mac ./cmd/opencw-mac
ls -l dist/opencw-mac
