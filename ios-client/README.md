# ZeroDesk iOS 原生床头客户端 (Stage 3)

基于 SwiftUI + `stasel/WebRTC`（Apple VideoToolbox H.264 硬解 + `RTCMTLVideoView` Metal 60fps 渲染）与 XcodeGen 构建的原生 iOS 客户端（iOS 16.0+，支持竖屏单手触控与横屏全屏模式）。

## 目录结构

- `project.yml`：XcodeGen 工程定义（严禁手写 `.pbxproj`）
- `Sources/Info.plist`：配置 `NSLocalNetworkUsageDescription` 与 `NSAppTransportSecurity`（`NSAllowsLocalNetworking = true`）
- `Sources/DiscoveryAndWoL.swift`：UDP `9528` 局域网自动发现 + 双通道 Wake-on-LAN（iPhone 直发 UDP 魔术包 / 可选 N1 `:9529/api/wol`）
- `Sources/WebRTCManager.swift`：纯内网 WebRTC（空 `iceServers`）、VideoToolbox 硬解、单次 `POST /api/connect` 握手、DataChannel 遥测与回前台自动重连
- `Sources/TouchpadGestureView.swift`：多点触控相对位移触控板（带速度增益曲线、`UIImpactFeedbackGenerator` 震动反馈、双击按住拖拽、双指右键与滚轮）
- `Sources/BedsideControlView.swift`：竖屏床头布局 / 横屏全屏切换、4x 双指捏合缩放、中文直通上屏、物理黑幕开关
- `build_unsigned_ipa.sh` & `.github/workflows/build-ios-ipa.yml`：自动生成未签名 `ZeroDesk-unsigned.ipa` 流水线（可用于 TrollStore / AltStore / Sideloadly 安装）

## 打包未签名 `.ipa`

- **Mac 本地**：进入 `ios-client/` 执行 `./build_unsigned_ipa.sh`
- **GitHub Actions 云端无 Mac 打包**：将 `ios-client/` 推送至 GitHub 仓库，触发 `Build ZeroDesk Unsigned iOS IPA` 工作流即可下载 `ZeroDesk-unsigned.ipa`。
