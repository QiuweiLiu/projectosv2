#!/bin/sh
set -eu

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
APP="$ROOT/desktop/build/ProjectOS.app"
GO="${GO:-go}"
CLANG="${CLANG:-clang}"

rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"

"$GO" build -o "$APP/Contents/Resources/kernel" "$ROOT/cmd/kernel"
"$CLANG" -fobjc-arc -O -framework Cocoa -framework WebKit "$ROOT/desktop/main.m" \
  -o "$APP/Contents/MacOS/ProjectOS"

cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleExecutable</key><string>ProjectOS</string>
  <key>CFBundleIdentifier</key><string>dev.projectos.desktop</string>
  <key>CFBundleName</key><string>ProjectOS</string>
  <key>CFBundleDisplayName</key><string>ProjectOS</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>0.1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>NSAppTransportSecurity</key>
  <dict><key>NSAllowsLocalNetworking</key><true/></dict>
</dict>
</plist>
PLIST

codesign --force --deep --sign - "$APP" >/dev/null 2>&1 || true
echo "built $APP"
echo "run: open \"$APP\""
