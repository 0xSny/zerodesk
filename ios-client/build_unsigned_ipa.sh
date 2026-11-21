#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")"

if ! command -v xcodegen >/dev/null 2>&1; then
  echo "[Error] xcodegen not found. Install via: brew install xcodegen"
  exit 1
fi

echo "[1/4] Generating ZeroDesk.xcodeproj via XcodeGen..."
xcodegen generate --spec project.yml

echo "[2/4] Resolving SPM dependencies & building unsigned Release app..."
BUILD_DIR="$(pwd)/.xcode_build"
rm -rf "$BUILD_DIR"

xcodebuild \
  -project ZeroDesk.xcodeproj \
  -scheme ZeroDesk \
  -configuration Release \
  -sdk iphoneos \
  -destination "generic/platform=iOS" \
  -derivedDataPath "$BUILD_DIR" \
  CODE_SIGNING_ALLOWED=NO \
  CODE_SIGNING_REQUIRED=NO \
  CODE_SIGN_IDENTITY="" \
  EXPANDED_CODE_SIGN_IDENTITY="" \
  AD_HOC_CODE_SIGNING_ALLOWED=YES \
  build

APP_PATH="$BUILD_DIR/Build/Products/Release-iphoneos/ZeroDesk.app"
if [ ! -d "$APP_PATH" ]; then
  echo "[Error] ZeroDesk.app not found at $APP_PATH"
  exit 1
fi

echo "[3/4] Packaging Payload/ZeroDesk.app -> ZeroDesk-unsigned.ipa..."
PKG_DIR="$BUILD_DIR/ipa_stage"
rm -rf "$PKG_DIR"
mkdir -p "$PKG_DIR/Payload"
cp -R "$APP_PATH" "$PKG_DIR/Payload/"
(cd "$PKG_DIR" && zip -qry "../ZeroDesk-unsigned.ipa" Payload)
mv "$BUILD_DIR/ZeroDesk-unsigned.ipa" "./ZeroDesk-unsigned.ipa"

echo "[4/4] Done: $(pwd)/ZeroDesk-unsigned.ipa ($(du -h ./ZeroDesk-unsigned.ipa | cut -f1))"
