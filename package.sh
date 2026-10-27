#!/bin/sh
# Builds dist/OpenCW-<version>-macos.pkg: a universal (Apple silicon + Intel)
# OpenCW.app that installs into /Applications owned by root, so the account that
# runs it cannot change the program it runs with sudo.
#
#   ./package.sh 1.0.0
set -eu
cd "$(dirname "$0")"
version=${1:?사용법: ./package.sh <버전> (예: ./package.sh 1.0.0)}
command -v go >/dev/null || { echo "Go가 필요합니다: brew install go" >&2; exit 1; }
toolchain=$(sed -n 's/^toolchain \(go[^ ]*\)$/\1/p' go.mod)
id=io.github.0xmoomi.opencw-mac
umask 022

rm -rf dist
mkdir -p dist/build
for arch in arm64 amd64; do
	GOTOOLCHAIN="$toolchain" GOOS=darwin GOARCH=$arch CGO_ENABLED=0 \
		go build -trimpath -buildvcs=false -tags with_utls,with_quic \
		-ldflags "-s -w -X main.version=$version" \
		-o "dist/build/opencw-mac-$arch" ./cmd/opencw-mac
done

root=dist/build/root
app=$root/OpenCW.app
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/Resources/opencw-mac" \
	dist/build/opencw-mac-arm64 dist/build/opencw-mac-amd64

# The app only opens a Terminal window; the window runs the CLI with sudo.
# Both use the installed absolute path, which only root can write.
cat >"$app/Contents/MacOS/OpenCW" <<'EOF'
#!/bin/sh
exec /usr/bin/open -a Terminal /Applications/OpenCW.app/Contents/Resources/OpenCW.command
EOF
cat >"$app/Contents/Resources/OpenCW.command" <<'EOF'
#!/bin/sh
echo "OpenCW를 시작합니다. 끝내려면 Ctrl+C를 누르거나 이 창을 닫으세요."
echo "Mac 로그인 암호를 입력하세요. 입력해도 화면에 표시되지 않습니다."
exec /usr/bin/sudo /Applications/OpenCW.app/Contents/Resources/opencw-mac
EOF
chmod 755 "$app/Contents/MacOS/OpenCW" "$app/Contents/Resources/OpenCW.command" \
	"$app/Contents/Resources/opencw-mac"

cat >"$app/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>OpenCW</string>
	<key>CFBundleDisplayName</key><string>OpenCW</string>
	<key>CFBundleIdentifier</key><string>$id</string>
	<key>CFBundleExecutable</key><string>OpenCW</string>
	<key>CFBundleIconFile</key><string>OpenCW</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$version</string>
	<key>CFBundleVersion</key><string>$version</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>NSHumanReadableCopyright</key><string>GPL-3.0-or-later</string>
</dict>
</plist>
EOF

# Icon: assets/app.ico (256px) -> OpenCW.icns.
iconset=dist/build/OpenCW.iconset
mkdir -p "$iconset"
sips -s format png assets/app.ico --out dist/build/icon.png >/dev/null
for size in 16 32 128 256; do
	sips -z $size $size dist/build/icon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
done
for size in 16 32 128; do
	double=$((size * 2))
	sips -z $double $double dist/build/icon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns -o "$app/Contents/Resources/OpenCW.icns" "$iconset"

# Ad-hoc signatures (no Developer ID): the CLI first, then the bundle.
# Extended attributes would land in the payload as ._ files (macOS may keep
# com.apple.provenance on a local build; it is harmless).
xattr -cr "$root"
codesign --force -s - -i "$id.cli" "$app/Contents/Resources/opencw-mac"
codesign --force -s - "$app"
codesign -v --strict "$app"

# Not relocatable: the installer must not redirect into some user-writable copy.
pkgbuild --analyze --root "$root" dist/build/component.plist >/dev/null
plutil -replace 0.BundleIsRelocatable -bool NO dist/build/component.plist
pkg="dist/OpenCW-$version-macos.pkg"
pkgbuild --root "$root" --component-plist dist/build/component.plist \
	--install-location /Applications --identifier "$id" --version "$version" \
	--ownership recommended "$pkg"

(cd dist && shasum -a 256 "OpenCW-$version-macos.pkg" >SHA256SUMS.txt)
rm -rf dist/build
ls -l dist
