import Foundation
import Combine
import CoreGraphics
import WebRTC

private struct ConnectPayload: Encodable {
    let pin: String
    let sdp: String
    let type: String
}

private struct ConnectReply: Decodable {
    let sdp: String
    let type: String?
    let width: Int?
    let height: Int?
    let targetFPS: Int?
    let bitrateKbps: Int?

    enum CodingKeys: String, CodingKey {
        case sdp, type, width, height
        case targetFPS = "target_fps"
        case bitrateKbps = "bitrate_kbps"
    }
}

@MainActor
final class WebRTCManager: NSObject, ObservableObject {
    @Published var isConnected: Bool = false
    @Published var isConnecting: Bool = false
    @Published var statusText: String = "未连接"
    @Published var errorMessage: String = ""
    @Published var remoteVideoTrack: RTCVideoTrack?
    @Published var blackoutEnabled: Bool = false
    @Published var rttMs: Int = 0
    @Published var fps: Int = 0
    @Published var bitrateMbps: Double = 0.0
    @Published var desktopSize: CGSize = CGSize(width: 2560, height: 1440)

    private let factory: RTCPeerConnectionFactory
    private var peerConnection: RTCPeerConnection?
    private var controlChannel: RTCDataChannel?
    private var pingTimer: Timer?
    private var statsTimer: Timer?
    private var lastBytesReceived: Int64 = 0
    private var lastStatsTime: TimeInterval = 0

    private var activeHost: String = ""
    private var activePort: Int = 9527
    private var activePin: String = ""
    private var gatheringWaiter: CheckedContinuation<Void, Never>?
    private var connectAttemptToken: UUID = UUID()

    override init() {
        RTCInitializeSSL()
        let encoderFactory = RTCDefaultVideoEncoderFactory()
        let decoderFactory = RTCDefaultVideoDecoderFactory() // Hardware VideoToolbox H.264 decoder
        self.factory = RTCPeerConnectionFactory(encoderFactory: encoderFactory, decoderFactory: decoderFactory)
        super.init()
    }

    func connect(host: String, port: Int, pin: String) {
        let cleanHost = host.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanPin = pin.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanHost.isEmpty, cleanPin.count == 6 else {
            statusText = "参数未完整"
            errorMessage = "请填写有效的电脑局域网 IP 和 6 位 PIN 码"
            return
        }

        disconnect(resetTarget: false)
        let token = UUID()
        connectAttemptToken = token
        activeHost = cleanHost
        activePort = port > 0 ? port : 9527
        activePin = cleanPin
        isConnecting = true
        errorMessage = ""
        statusText = "握手中..."

        let config = RTCConfiguration()
        config.iceServers = [] // Pure LAN: zero STUN/TURN external requests
        config.sdpSemantics = .unifiedPlan
        config.continualGatheringPolicy = .gatherOnce

        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let pc = factory.peerConnection(with: config, constraints: constraints, delegate: self) else {
            isConnecting = false
            statusText = "初始化失败"
            errorMessage = "无法创建 RTCPeerConnection"
            return
        }
        peerConnection = pc

        let transInit = RTCRtpTransceiverInit()
        transInit.direction = .recvOnly
        pc.addTransceiver(of: .video, init: transInit)

        let dcConfig = RTCDataChannelConfiguration()
        dcConfig.isOrdered = true
        if let dc = pc.dataChannel(forLabel: "control", configuration: dcConfig) {
            dc.delegate = self
            controlChannel = dc
        }

        let offerConstraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        pc.offer(for: offerConstraints) { [weak self] sdp, err in
            guard let self else { return }
            if let err {
                Task { @MainActor in
                    guard self.connectAttemptToken == token else { return }
                    self.isConnecting = false
                    self.statusText = "Offer 失败"
                    self.errorMessage = err.localizedDescription
                }
                return
            }
            guard let sdp else { return }
            pc.setLocalDescription(sdp) { setErr in
                if let setErr {
                    Task { @MainActor in
                        guard self.connectAttemptToken == token else { return }
                        self.isConnecting = false
                        self.statusText = "信令错误"
                        self.errorMessage = setErr.localizedDescription
                    }
                    return
                }
                Task { @MainActor in
                    guard self.connectAttemptToken == token, self.peerConnection === pc else { return }
                    await self.waitForLocalCandidates(pc: pc, timeoutMs: 250)
                    guard self.connectAttemptToken == token, self.peerConnection === pc else { return }
                    let finalOffer = pc.localDescription ?? sdp
                    await self.sendOfferToHost(
                        sdp: finalOffer,
                        host: cleanHost,
                        port: self.activePort,
                        pin: cleanPin,
                        pc: pc,
                        token: token
                    )
                }
            }
        }
    }

    func reconnectNow() {
        guard !activeHost.isEmpty, activePin.count == 6 else { return }
        connect(host: activeHost, port: activePort, pin: activePin)
    }

    func reconnectIfNeeded() {
        guard !isConnected, !isConnecting, !activeHost.isEmpty, activePin.count == 6 else { return }
        connect(host: activeHost, port: activePort, pin: activePin)
    }

    private func waitForLocalCandidates(pc: RTCPeerConnection, timeoutMs: UInt64) async {
        if pc.iceGatheringState == .complete {
            return
        }
        await withCheckedContinuation { (cont: CheckedContinuation<Void, Never>) in
            self.gatheringWaiter = cont
            Task { @MainActor in
                try? await Task.sleep(nanoseconds: timeoutMs * 1_000_000)
                if let pending = self.gatheringWaiter {
                    self.gatheringWaiter = nil
                    pending.resume()
                }
            }
        }
    }

    private func sendOfferToHost(
        sdp: RTCSessionDescription,
        host: String,
        port: Int,
        pin: String,
        pc: RTCPeerConnection,
        token: UUID
    ) async {
        guard let url = URL(string: "http://\(host):\(port)/api/connect") else {
            isConnecting = false
            statusText = "地址无效"
            errorMessage = "地址格式错误: \(host):\(port)"
            return
        }

        let payloadData = try? JSONEncoder().encode(ConnectPayload(pin: pin, sdp: sdp.sdp, type: "offer"))
        var lastErr: Error?

        // Retry transient Local Network permission / Wi-Fi wakeup errors up to 2 times
        for attempt in 0..<3 {
            guard connectAttemptToken == token, peerConnection === pc else { return }
            if attempt > 0 {
                statusText = "等待本地网络授权/重试(\(attempt))..."
                try? await Task.sleep(nanoseconds: 850_000_000)
                guard connectAttemptToken == token, peerConnection === pc else { return }
            }

            var req = URLRequest(url: url, timeoutInterval: 4.0)
            req.httpMethod = "POST"
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = payloadData

            do {
                let (data, response) = try await URLSession.shared.data(for: req)
                guard connectAttemptToken == token, peerConnection === pc else { return }
                guard let http = response as? HTTPURLResponse else {
                    isConnecting = false
                    statusText = "网络异常"
                    return
                }
                if http.statusCode == 401 {
                    isConnecting = false
                    statusText = "PIN 错误"
                    errorMessage = "PIN 码校验失败，请核对电脑端托盘或 data\\config.json 中的 6 位数字"
                    disconnect(resetTarget: false, preserveError: true)
                    return
                }
                guard http.statusCode == 200 else {
                    isConnecting = false
                    statusText = "握手失败(\(http.statusCode))"
                    errorMessage = String(data: data, encoding: .utf8) ?? "HTTP \(http.statusCode)"
                    return
                }

                let reply = try JSONDecoder().decode(ConnectReply.self, from: data)
                if let w = reply.width, let h = reply.height, w > 0, h > 0 {
                    desktopSize = CGSize(width: w, height: h)
                }
                statusText = "ICE 连通中..."
                let answer = RTCSessionDescription(type: .answer, sdp: reply.sdp)
                pc.setRemoteDescription(answer) { [weak self] err in
                    if let err {
                        Task { @MainActor in
                            guard let self, self.connectAttemptToken == token else { return }
                            self.isConnecting = false
                            self.statusText = "SDP 异常"
                            self.errorMessage = err.localizedDescription
                        }
                    }
                }
                return
            } catch {
                lastErr = error
            }
        }

        guard connectAttemptToken == token, peerConnection === pc else { return }
        isConnecting = false
        statusText = "连接超时"
        let desc = lastErr?.localizedDescription ?? "超时"
        errorMessage = "无法连接到 \(host):\(port)（\(desc)）。请确认电脑已启动 ZeroDesk 且手机「设置 -> 隐私 -> 本地网络」已允许 ZeroDesk。"
    }

    func disconnect(resetTarget: Bool = true, preserveError: Bool = false) {
        connectAttemptToken = UUID()
        if let pending = gatheringWaiter {
            gatheringWaiter = nil
            pending.resume()
        }
        pingTimer?.invalidate()
        pingTimer = nil
        statsTimer?.invalidate()
        statsTimer = nil

        controlChannel?.delegate = nil
        controlChannel?.close()
        controlChannel = nil

        peerConnection?.delegate = nil
        peerConnection?.close()
        peerConnection = nil

        remoteVideoTrack = nil
        isConnected = false
        isConnecting = false
        blackoutEnabled = false
        if !preserveError {
            statusText = "未连接"
            errorMessage = ""
        }
        if resetTarget {
            activeHost = ""
            activePin = ""
        }
    }

    func sendControl(_ payload: [String: Any]) {
        guard let dc = controlChannel, dc.readyState == .open else { return }
        guard let data = try? JSONSerialization.data(withJSONObject: payload) else { return }
        dc.sendData(RTCDataBuffer(data: data, isBinary: false))
    }

    func sendMouseMove(dx: Int, dy: Int) {
        guard dx != 0 || dy != 0 else { return }
        sendControl(["type": "mouse_move", "dx": dx, "dy": dy])
    }

    func sendMouseButton(_ button: String, action: String) {
        sendControl(["type": "mouse_btn", "button": button, "action": action])
    }

    func sendScroll(dy: Int) {
        guard dy != 0 else { return }
        sendControl(["type": "scroll", "dy": dy])
    }

    func sendUnicodeText(_ text: String) {
        guard !text.isEmpty else { return }
        sendControl(["type": "text", "text": text])
    }

    func sendShortcut(_ key: String) {
        sendControl(["type": "shortcut", "key": key])
    }

    func toggleBlackout() {
        sendControl(["type": "blackout", "action": "toggle"])
    }

    func sendSystemCommand(_ cmd: String) {
        sendControl(["type": cmd])
    }

    private func startTelemetryTimers() {
        pingTimer?.invalidate()
        statsTimer?.invalidate()
        lastBytesReceived = 0
        lastStatsTime = ProcessInfo.processInfo.systemUptime

        pingTimer = Timer.scheduledTimer(withTimeInterval: 1.5, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.sendControl(["type": "ping", "ts": Int64(ProcessInfo.processInfo.systemUptime * 1000)])
            }
        }

        statsTimer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
            Task { @MainActor in
                guard let self, let pc = self.peerConnection else { return }
                pc.statistics { report in
                    var totalBytes: Int64 = 0
                    var currentFPS: Int = 0
                    for (_, stat) in report.statistics where stat.type == "inbound-rtp" {
                        if let kind = stat.values["kind"] as? String, kind == "video" {
                            if let b = stat.values["bytesReceived"] as? NSNumber {
                                totalBytes = b.int64Value
                            }
                            if let f = stat.values["framesPerSecond"] as? NSNumber {
                                currentFPS = f.intValue
                            }
                        }
                    }
                    let now = ProcessInfo.processInfo.systemUptime
                    Task { @MainActor in
                        let dt = now - self.lastStatsTime
                        if self.lastBytesReceived > 0 && dt > 0.2 {
                            let deltaBits = Double(totalBytes - self.lastBytesReceived) * 8.0
                            self.bitrateMbps = max(0, deltaBits / dt / 1_000_000.0)
                        }
                        self.lastBytesReceived = totalBytes
                        self.lastStatsTime = now
                        if currentFPS > 0 {
                            self.fps = currentFPS
                        }
                    }
                }
            }
        }
    }
}

extension WebRTCManager: RTCPeerConnectionDelegate {
    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didChange stateChanged: RTCSignalingState) {}

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didAdd stream: RTCMediaStream) {
        if let track = stream.videoTracks.first {
            Task { @MainActor in
                guard self.peerConnection === peerConnection else { return }
                self.remoteVideoTrack = track
            }
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnectionShouldNegotiate(_ peerConnection: RTCPeerConnection) {}

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        Task { @MainActor in
            guard self.peerConnection === peerConnection else { return }
            switch newState {
            case .checking:
                self.statusText = "ICE 连通中..."
            case .connected, .completed:
                self.isConnected = true
                self.isConnecting = false
                self.statusText = "已直连"
                self.errorMessage = ""
                self.sendControl(["type": "request_idr"])
                self.startTelemetryTimers()
            case .disconnected:
                self.isConnected = false
                self.statusText = "重连中..."
            case .failed:
                self.isConnected = false
                self.isConnecting = false
                self.blackoutEnabled = false
                self.statusText = "ICE 通道失败"
                if self.errorMessage.isEmpty {
                    self.errorMessage = "UDP 媒体通道建立失败，请检查同 Wi-Fi 连接或关闭 VPN 后重连"
                }
            case .closed:
                self.isConnected = false
                self.isConnecting = false
                self.blackoutEnabled = false
                self.statusText = "已断开"
            default:
                break
            }
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didChange newState: RTCIceGatheringState) {
        if newState == .complete {
            Task { @MainActor in
                guard self.peerConnection === peerConnection else { return }
                if let pending = self.gatheringWaiter {
                    self.gatheringWaiter = nil
                    pending.resume()
                }
            }
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didGenerate candidate: RTCIceCandidate) {}
    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didRemove candidates: [RTCIceCandidate]) {}

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didOpen dataChannel: RTCDataChannel) {
        dataChannel.delegate = self
        Task { @MainActor in
            guard self.peerConnection === peerConnection else { return }
            self.controlChannel = dataChannel
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didAdd rtpReceiver: RTCRtpReceiver, streams mediaStreams: [RTCMediaStream]) {
        if let track = rtpReceiver.track as? RTCVideoTrack {
            Task { @MainActor in
                guard self.peerConnection === peerConnection else { return }
                self.remoteVideoTrack = track
            }
        }
    }
}

extension WebRTCManager: RTCDataChannelDelegate {
    nonisolated func dataChannelDidChangeState(_ dataChannel: RTCDataChannel) {
        if dataChannel.readyState == .open {
            Task { @MainActor in
                guard self.controlChannel === dataChannel else { return }
                self.sendControl(["type": "request_idr"])
            }
        }
    }

    nonisolated func dataChannel(_ dataChannel: RTCDataChannel, didReceiveMessageWith buffer: RTCDataBuffer) {
        guard !buffer.isBinary,
              let json = try? JSONSerialization.jsonObject(with: buffer.data) as? [String: Any],
              let type = json["type"] as? String else { return }

        Task { @MainActor in
            guard self.controlChannel === dataChannel else { return }
            switch type {
            case "pong":
                if let ts = (json["ts"] as? NSNumber)?.int64Value {
                    let nowMs = Int64(ProcessInfo.processInfo.systemUptime * 1000)
                    self.rttMs = max(1, Int(nowMs - ts))
                }
            case "blackout_state":
                if let enabled = json["enabled"] as? Bool {
                    self.blackoutEnabled = enabled
                }
            default:
                break
            }
        }
    }
}
