#!/bin/bash
# 构建 ssh-tunnel.app（macOS 通用二进制：amd64 + arm64）
set -euo pipefail

cd "$(dirname "$0")"

APP_NAME="ssh-tunnel"
APP="build/${APP_NAME}.app"
VERSION="${VERSION:-1.0.0}"

# 构建机（如 macOS 26 的 CI runner）会按自身 SDK 把二进制最低系统
# 版本打成 26.0，导致在旧系统上无法启动。这里固定为与 Info.plist
# 一致的 11.0，让 CGO 链接带上 -mmacosx-version-min。
export MACOSX_DEPLOYMENT_TARGET="${MACOSX_DEPLOYMENT_TARGET:-11.0}"

rm -rf build
mkdir -p "${APP}/Contents/MacOS" "${APP}/Contents/Resources"

echo "==> 编译 amd64"
CGO_ENABLED=1 GOOS=darwin GOARCH=amd64 \
  go build -trimpath -ldflags "-s -w" -o "build/${APP_NAME}-amd64" .

echo "==> 编译 arm64"
CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
  go build -trimpath -ldflags "-s -w" -o "build/${APP_NAME}-arm64" .

echo "==> 合并为通用二进制"
lipo -create -output "${APP}/Contents/MacOS/${APP_NAME}" \
  "build/${APP_NAME}-amd64" "build/${APP_NAME}-arm64"
rm -f "build/${APP_NAME}-amd64" "build/${APP_NAME}-arm64"

echo "==> 拷贝图标"
# icns 由 assets/icon.svg 生成，见 assets/README 或 make-icon 说明
cp assets/icon.icns "${APP}/Contents/Resources/icon.icns"

echo "==> 生成 Info.plist"
cat > "${APP}/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key>
	<string>${APP_NAME}</string>
	<key>CFBundleDisplayName</key>
	<string>SSH Tunnel</string>
	<key>CFBundleIdentifier</key>
	<string>cn.yjiang.ssh-tunnel</string>
	<key>CFBundleExecutable</key>
	<string>${APP_NAME}</string>
	<key>CFBundlePackageType</key>
	<string>APPL</string>
	<key>CFBundleIconFile</key>
	<string>icon</string>
	<key>CFBundleVersion</key>
	<string>${VERSION}</string>
	<key>CFBundleShortVersionString</key>
	<string>${VERSION}</string>
	<key>LSMinimumSystemVersion</key>
	<string>11.0</string>
	<key>NSHighResolutionCapable</key>
	<true/>
	<!-- Fyne 需要 Cocoa 主线程，普通窗口应用不能设 LSUIElement，
	     否则应用不会出现在 Dock 里，窗口也无法获得焦点 -->
</dict>
</plist>
PLIST

echo "==> 自签名（ad-hoc）"
# 未签名的 .app 在较新系统上会被 Gatekeeper 直接拦下，ad-hoc 签名可避免
codesign --force --deep --sign - "${APP}"

echo
echo "构建完成: ${APP}"
lipo -archs "${APP}/Contents/MacOS/${APP_NAME}"
echo
echo "安装:  cp -R ${APP} /Applications/"
echo "配置:  首次打开后在 App 的「设置」页填写并保存，写入 ~/.config/ssh-tunnel/config.yaml"
