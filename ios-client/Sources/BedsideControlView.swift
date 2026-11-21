import SwiftUI
import UIKit
import WebRTC

struct MetalVideoView: UIViewRepresentable {
    let track: RTCVideoTrack?

    func makeCoordinator() -> Coordinator {
        Coordinator()
    }

    func makeUIView(context: Context) -> RTCMTLVideoView {
        let view = RTCMTLVideoView(frame: .zero)
        view.videoContentMode = .scaleAspectFit
        view.backgroundColor = .black
        return view
    }

    func updateUIView(_ uiView: RTCMTLVideoView, context: Context) {
        if context.coordinator.boundTrack !== track {
            if let old = context.coordinator.boundTrack {
                old.remove(uiView)
            }
            if let newTrack = track {
                newTrack.add(uiView)
            }
            context.coordinator.boundTrack = track
        }
    }

    static func dismantleUIView(_ uiView: RTCMTLVideoView, coordinator: Coordinator) {
        coordinator.boundTrack?.remove(uiView)
        coordinator.boundTrack = nil
    }

    final class Coordinator {
        var boundTrack: RTCVideoTrack?
    }
}

struct BedsideControlView: View {
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var rtc = WebRTCManager()
    @StateObject private var discovery = DiscoveryAndWoL()

    @AppStorage("zd_host_ip") private var hostIP: String = ""
    @AppStorage("zd_host_port") private var hostPort: Int = 9527
    @AppStorage("zd_host_mac") private var hostMAC: String = ""
    @AppStorage("zd_pin_code") private var pinCode: String = ""
    @AppStorage("zd_n1_ip") private var n1IP: String = ""

    @State private var inputText: String = ""
    @State private var isDragging: Bool = false
    @State private var isLandscape: Bool = false
    @State private var showLandscapePad: Bool = false
    @State private var showPairingSheet: Bool = false
    @State private var showSleepConfirm: Bool = false

    // Pinch-to-zoom & pan state for Metal video viewport
    @State private var zoomScale: CGFloat = 1.0
    @State private var lastZoomScale: CGFloat = 1.0
    @State private var panOffset: CGSize = .zero
    @State private var lastPanOffset: CGSize = .zero

    private let shortcuts: [(label: String, key: String)] = [
        ("Esc", "Esc"), ("Enter", "Enter"), ("退格", "Backspace"),
        ("Tab", "Tab"), ("桌面", "Win+D"), ("切窗", "Alt+Tab"),
        ("复制", "Ctrl+C"), ("粘贴", "Ctrl+V"), ("撤销", "Ctrl+Z"),
        ("全选", "Ctrl+A"), ("关标签", "Ctrl+W"),
        ("↑", "Up"), ("↓", "Down"), ("←", "Left"), ("→", "Right")
    ]

    var body: some View {
        ZStack {
            Color(red: 0.04, green: 0.05, blue: 0.07).ignoresSafeArea()

            if isLandscape {
                landscapeLayout
            } else {
                portraitLayout
            }
        }
        .onAppear {
            discovery.discoverHosts()
            if hostIP.isEmpty || pinCode.count != 6 {
                showPairingSheet = true
            } else {
                rtc.connect(host: hostIP, port: hostPort, pin: pinCode)
            }
        }
        .onChange(of: discovery.hosts) { hosts in
            if let first = hosts.first {
                if hostIP.isEmpty {
                    hostIP = first.primaryIP
                    hostPort = first.httpPort
                }
                if !first.macAddress.isEmpty {
                    hostMAC = first.macAddress
                }
            }
        }
        .onChange(of: scenePhase) { phase in
            if phase == .active {
                rtc.reconnectIfNeeded()
            }
        }
        .sheet(isPresented: $showPairingSheet) {
            pairingSheet
        }
        .alert("确认让电脑立即进入睡眠状态？", isPresented: $showSleepConfirm) {
            Button("立即睡眠", role: .destructive) {
                rtc.sendSystemCommand("sleep")
            }
            Button("取消", role: .cancel) {}
        }
    }

    private var portraitLayout: some View {
        VStack(spacing: 0) {
            videoViewport
                .frame(maxHeight: UIScreen.main.bounds.height * 0.38)

            thumbToolbar

            VStack(spacing: 8) {
                ZStack {
                    RoundedRectangle(cornerRadius: 18)
                        .fill(
                            RadialGradient(
                                colors: [Color(white: 0.11), Color(white: 0.06)],
                                center: .center,
                                startRadius: 10,
                                endRadius: 240
                            )
                        )
                        .overlay(
                            RoundedRectangle(cornerRadius: 18)
                                .stroke(isDragging ? Color.blue : Color.white.opacity(0.12), lineWidth: 1.2)
                        )

                    VStack(spacing: 4) {
                        Text("单指滑光标 · 轻点左键 · 双击按住拖拽")
                        Text("双指轻点右键 · 双指上下滑滚轮")
                    }
                    .font(.caption2)
                    .foregroundColor(.gray)
                    .allowsHitTesting(false)

                    TouchpadGestureView(
                        onMove: { dx, dy in rtc.sendMouseMove(dx: dx, dy: dy) },
                        onButton: { btn, act in rtc.sendMouseButton(btn, action: act) },
                        onScroll: { dy in rtc.sendScroll(dy: dy) },
                        isDraggingActive: $isDragging
                    )
                }

                HStack(spacing: 8) {
                    mouseButton("左键") { rtc.sendMouseButton("left", action: "click") }
                    mouseButton("中键") { rtc.sendMouseButton("middle", action: "click") }
                        .frame(maxWidth: 80)
                    mouseButton("右键") { rtc.sendMouseButton("right", action: "click") }
                }
                .frame(height: 42)
            }
            .padding(10)
        }
    }

    private var landscapeLayout: some View {
        ZStack(alignment: .bottomTrailing) {
            videoViewport
                .ignoresSafeArea()

            if showLandscapePad {
                TouchpadGestureView(
                    onMove: { dx, dy in rtc.sendMouseMove(dx: dx, dy: dy) },
                    onButton: { btn, act in rtc.sendMouseButton(btn, action: act) },
                    onScroll: { dy in rtc.sendScroll(dy: dy) },
                    isDraggingActive: $isDragging
                )
                .background(Color.black.opacity(0.08))
            }

            HStack(spacing: 8) {
                Button(showLandscapePad ? "隐藏触控叠层" : "触控叠层") {
                    showLandscapePad.toggle()
                }
                .buttonStyle(.borderedProminent)
                .tint(showLandscapePad ? .blue : .gray.opacity(0.5))

                Button(rtc.blackoutEnabled ? "🌙 黑幕已开" : "🌙 黑幕") {
                    rtc.toggleBlackout()
                }
                .buttonStyle(.borderedProminent)
                .tint(rtc.blackoutEnabled ? .indigo : .gray.opacity(0.5))
            }
            .font(.caption)
            .padding(14)
        }
    }

    private var videoViewport: some View {
        ZStack {
            Color.black

            MetalVideoView(track: rtc.remoteVideoTrack)
                .scaleEffect(zoomScale)
                .offset(panOffset)
                .gesture(
                    MagnificationGesture()
                        .onChanged { val in
                            zoomScale = min(4.0, max(1.0, lastZoomScale * val))
                        }
                        .onEnded { _ in
                            if zoomScale <= 1.03 {
                                zoomScale = 1.0
                                panOffset = .zero
                                lastPanOffset = .zero
                            }
                            lastZoomScale = zoomScale
                        }
                )
                .simultaneousGesture(
                    DragGesture()
                        .onChanged { val in
                            guard zoomScale > 1.02 else { return }
                            panOffset = CGSize(
                                width: lastPanOffset.width + val.translation.width,
                                height: lastPanOffset.height + val.translation.height
                            )
                        }
                        .onEnded { _ in
                            lastPanOffset = panOffset
                        }
                )
                .onTapGesture(count: 2) {
                    withAnimation(.easeOut(duration: 0.2)) {
                        zoomScale = 1.0
                        lastZoomScale = 1.0
                        panOffset = .zero
                        lastPanOffset = .zero
                    }
                }

            VStack {
                HStack(spacing: 6) {
                    HStack(spacing: 5) {
                        Circle()
                            .fill(rtc.isConnected ? Color.green : Color.red)
                            .frame(width: 7, height: 7)
                        Text(rtc.statusText)
                    }
                    .padding(.horizontal, 9)
                    .padding(.vertical, 4)
                    .background(.ultraThinMaterial)
                    .clipShape(Capsule())

                    Spacer()

                    if rtc.isConnected {
                        Text("\(rtc.rttMs)ms · \(rtc.fps)fps · \(String(format: "%.1f", rtc.bitrateMbps))M")
                            .padding(.horizontal, 9)
                            .padding(.vertical, 4)
                            .background(.ultraThinMaterial)
                            .clipShape(Capsule())
                    }

                    if zoomScale > 1.03 {
                        Button("\(String(format: "%.1fx", zoomScale))") {
                            zoomScale = 1.0
                            lastZoomScale = 1.0
                            panOffset = .zero
                            lastPanOffset = .zero
                        }
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .background(Color.black.opacity(0.65))
                        .clipShape(Capsule())
                    }

                    Button {
                        toggleOrientation()
                    } label: {
                        Image(systemName: isLandscape ? "rectangle.portrait" : "rectangle.landscape")
                    }
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(Color.black.opacity(0.65))
                    .clipShape(Capsule())

                    Button {
                        showPairingSheet = true
                    } label: {
                        Image(systemName: "gearshape.fill")
                    }
                    .padding(.horizontal, 8)
                    .padding(.vertical, 4)
                    .background(Color.black.opacity(0.65))
                    .clipShape(Capsule())
                }
                .font(.caption2)
                .foregroundColor(.white)
                .padding(.horizontal, 8)
                .padding(.top, 8)

                Spacer()
            }
        }
    }

    private var thumbToolbar: some View {
        VStack(spacing: 7) {
            HStack(spacing: 6) {
                TextField("输入中文或英文直接上屏...", text: $inputText)
                    .textFieldStyle(.plain)
                    .padding(.horizontal, 10)
                    .padding(.vertical, 7)
                    .background(Color(white: 0.13))
                    .cornerRadius(9)
                    .submitLabel(.send)
                    .onSubmit(sendInputText)

                Button("上屏", action: sendInputText)
                    .padding(.horizontal, 11)
                    .padding(.vertical, 7)
                    .background(Color.blue)
                    .foregroundColor(.white)
                    .cornerRadius(9)

                Button(rtc.blackoutEnabled ? "🌙 黑幕开" : "🌙 黑幕") {
                    rtc.toggleBlackout()
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 7)
                .background(rtc.blackoutEnabled ? Color.indigo : Color(white: 0.15))
                .foregroundColor(.white)
                .cornerRadius(9)
            }
            .font(.caption)

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 6) {
                    quickChip("⚡ 唤醒", tint: .orange) {
                        discovery.wakeComputer(mac: hostMAC, pcIP: hostIP, n1IP: n1IP, pin: pinCode)
                    }
                    quickChip("静音") { rtc.sendSystemCommand("vol_mute") }
                    quickChip("音量-") { rtc.sendSystemCommand("vol_down") }
                    quickChip("音量+") { rtc.sendSystemCommand("vol_up") }
                    quickChip("睡眠", tint: .red) { showSleepConfirm = true }

                    ForEach(shortcuts, id: \.key) { item in
                        quickChip(item.label) {
                            rtc.sendShortcut(item.key)
                        }
                    }
                }
            }
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 8)
        .background(Color(white: 0.08))
    }

    private func quickChip(_ title: String, tint: Color? = nil, action: @escaping () -> Void) -> some View {
        Button(title, action: action)
            .font(.caption)
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(Color(white: 0.14))
            .foregroundColor(tint ?? .white)
            .cornerRadius(8)
    }

    private func mouseButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Text(title)
                .font(.caption)
                .foregroundColor(.gray)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .background(Color(white: 0.09))
                .cornerRadius(12)
                .overlay(
                    RoundedRectangle(cornerRadius: 12)
                        .stroke(Color.white.opacity(0.1), lineWidth: 1)
                )
        }
    }

    private func sendInputText() {
        guard !inputText.isEmpty else { return }
        rtc.sendUnicodeText(inputText)
        inputText = ""
    }

    private func toggleOrientation() {
        isLandscape.toggle()
        let mask: UIInterfaceOrientationMask = isLandscape ? .landscapeRight : .portrait
        AppOrientationDelegate.orientationLock = mask
        guard let windowScene = UIApplication.shared.connectedScenes.first as? UIWindowScene else { return }
        windowScene.windows.first?.rootViewController?.setNeedsUpdateOfSupportedInterfaceOrientations()
        windowScene.requestGeometryUpdate(.iOS(interfaceOrientations: mask)) { _ in }
    }

    private var pairingSheet: some View {
        NavigationView {
            Form {
                Section("局域网主机自动发现 (UDP 9528)") {
                    Button {
                        discovery.discoverHosts()
                    } label: {
                        HStack {
                            Text(discovery.isScanning ? "正在扫描局域网..." : "重新扫描同 Wi-Fi 电脑")
                            Spacer()
                            if discovery.isScanning { ProgressView() }
                        }
                    }
                    ForEach(discovery.hosts) { h in
                        Button {
                            hostIP = h.primaryIP
                            hostPort = h.httpPort
                            if !h.macAddress.isEmpty { hostMAC = h.macAddress }
                        } label: {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("\(h.hostname) (\(h.primaryIP):\(h.httpPort))")
                                    .foregroundColor(.primary)
                                if !h.macAddress.isEmpty {
                                    Text("MAC: \(h.macAddress)")
                                        .font(.caption)
                                        .foregroundColor(.secondary)
                                }
                            }
                        }
                    }
                }

                Section("配对连接参数") {
                    TextField("电脑局域网 IP (如 192.168.1.7)", text: $hostIP)
                        .keyboardType(.decimalPad)
                    TextField("6 位配对 PIN 码", text: $pinCode)
                        .keyboardType(.numberPad)
                    TextField("电脑物理网卡 MAC (自动学习)", text: $hostMAC)
                    TextField("可选: N1 盒子 IP (留空则纯本机 WoL)", text: $n1IP)
                        .keyboardType(.decimalPad)
                }

                Section {
                    Button("立即连接桌面") {
                        showPairingSheet = false
                        rtc.connect(host: hostIP, port: hostPort, pin: pinCode)
                    }
                    .frame(maxWidth: .infinity)
                    .foregroundColor(.blue)

                    Button("⚡ 发送 Wake-on-LAN 唤醒电脑") {
                        discovery.wakeComputer(mac: hostMAC, pcIP: hostIP, n1IP: n1IP, pin: pinCode)
                    }
                    .frame(maxWidth: .infinity)
                    .foregroundColor(.orange)

                    if !discovery.wakeStatusMessage.isEmpty {
                        Text(discovery.wakeStatusMessage)
                            .font(.caption)
                            .foregroundColor(.secondary)
                    }
                    if !rtc.errorMessage.isEmpty {
                        Text(rtc.errorMessage)
                            .font(.caption)
                            .foregroundColor(.red)
                    }
                }
            }
            .navigationTitle("ZeroDesk 配对设置")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("关闭") { showPairingSheet = false }
                }
            }
        }
    }
}
