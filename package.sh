#!/bin/sh
# Builds dist/OpenCW-<version>-macos.pkg: a universal (Apple silicon + Intel)
# OpenCW.app in /Applications plus the LaunchDaemon that runs its root helper,
# all owned by root, so the account that uses it cannot change what runs as
# root.
#
#   ./package.sh 1.0.0
set -eu
cd "$(dirname "$0")"
version=${1:?사용법: ./package.sh <버전> (예: ./package.sh 1.0.0)}
command -v go >/dev/null || { echo "Go가 필요합니다: brew install go" >&2; exit 1; }
command -v swiftc >/dev/null || { echo "Xcode 또는 Command Line Tools가 필요합니다: xcode-select --install" >&2; exit 1; }
toolchain=$(sed -n 's/^toolchain \(go[^ ]*\)$/\1/p' go.mod)
id=io.github.0xmoomi.opencw-mac
label=io.github.0xmoomi.opencw
umask 022

rm -rf dist
build=dist/build
mkdir -p "$build"

# The root helper (daemon).
for arch in arm64 amd64; do
	GOTOOLCHAIN="$toolchain" GOOS=darwin GOARCH=$arch CGO_ENABLED=0 \
		go build -trimpath -buildvcs=false -tags with_utls,with_quic \
		-ldflags "-s -w -X main.version=$version" \
		-o "$build/opencw-mac-$arch" ./cmd/opencw-mac
done

# The app.
for arch in arm64 x86_64; do
	swiftc -O -parse-as-library -target "$arch-apple-macos13" \
		-o "$build/OpenCW-$arch" macos/OpenCW/*.swift
done

root=$build/root
app=$root/Applications/OpenCW.app
daemons=$root/Library/LaunchDaemons
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources" "$daemons"
lipo -create -output "$app/Contents/MacOS/OpenCW" "$build/OpenCW-arm64" "$build/OpenCW-x86_64"
lipo -create -output "$app/Contents/Resources/opencw-mac" \
	"$build/opencw-mac-arm64" "$build/opencw-mac-amd64"
cp uninstall.sh "$app/Contents/Resources/uninstall.sh"
chmod 755 "$app/Contents/MacOS/OpenCW" "$app/Contents/Resources/opencw-mac" \
	"$app/Contents/Resources/uninstall.sh"

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
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSHumanReadableCopyright</key><string>GPL-3.0-or-later</string>
</dict>
</plist>
EOF

# launchd keeps the helper running; it idles until the app connects.
# AssociatedBundleIdentifiers names the app in the background-item notice.
cat >"$daemons/$label.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$label</string>
	<key>ProgramArguments</key>
	<array>
		<string>/Applications/OpenCW.app/Contents/Resources/opencw-mac</string>
	</array>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>AssociatedBundleIdentifiers</key><string>$id</string>
</dict>
</plist>
EOF
chmod 644 "$daemons/$label.plist"
plutil -lint "$app/Contents/Info.plist" "$daemons/$label.plist" >/dev/null

# Icon: assets/app.ico (256px) -> OpenCW.icns.
iconset=$build/OpenCW.iconset
mkdir -p "$iconset"
sips -s format png assets/app.ico --out "$build/icon.png" >/dev/null
for size in 16 32 128 256; do
	sips -z $size $size "$build/icon.png" --out "$iconset/icon_${size}x${size}.png" >/dev/null
done
for size in 16 32 128; do
	double=$((size * 2))
	sips -z $double $double "$build/icon.png" --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns -o "$app/Contents/Resources/OpenCW.icns" "$iconset"

# Ad-hoc signatures (no Developer ID): inner binaries first, then the bundle.
# The hardened runtime keeps other code from being injected into the app,
# the only client the helper accepts.
# Extended attributes would land in the payload as ._ files (macOS may keep
# com.apple.provenance on a local build; it is harmless).
xattr -cr "$root"
codesign --force -s - -i "$id.helper" "$app/Contents/Resources/opencw-mac"
codesign --force -s - -o runtime "$app"
codesign -v --strict "$app"

# Install scripts: stop the old helper (restoring the network) before the
# payload lands, start the new one after.
scripts=$build/scripts
mkdir -p "$scripts"
cat >"$scripts/preinstall" <<EOF
#!/bin/sh
/bin/launchctl bootout system/$label 2>/dev/null
exit 0
EOF
cat >"$scripts/postinstall" <<EOF
#!/bin/sh
# bootout can return before the old job is fully gone; retry briefly.
for _ in 1 2 3 4 5 6 7 8 9 10; do
	/bin/launchctl bootstrap system /Library/LaunchDaemons/$label.plist && exit 0
	/bin/sleep 1
done
exit 1
EOF
chmod 755 "$scripts/preinstall" "$scripts/postinstall"

# Not relocatable: the installer must not redirect into some user-writable copy.
pkgbuild --analyze --root "$root" "$build/component.plist" >/dev/null
plutil -replace 0.BundleIsRelocatable -bool NO "$build/component.plist"
pkg="dist/OpenCW-$version-macos.pkg"
pkgbuild --root "$root" --component-plist "$build/component.plist" --scripts "$scripts" \
	--install-location / --identifier "$id" --version "$version" \
	--ownership recommended "$pkg"

(cd dist && shasum -a 256 "OpenCW-$version-macos.pkg" >SHA256SUMS.txt)
rm -rf "$build"
ls -l dist
