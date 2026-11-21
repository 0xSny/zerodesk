import Foundation
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
            errorMessage = "请填写有效的电脑局域网 IP 和 6 位 PIN 码"
            return
        }

        disconnect(resetTarget: false)
        activeHost = cleanHost
        activePort = port
        activePin = cleanPin
        errorMessage = ""
        statusText = "握手中..."

        let config = RTCConfiguration()
        config.iceServers = [] // Pure LAN: zero STUN/TURN external requests
        config.sdpSemantics = .unifiedPlan
        config.continualGatheringPolicy = .gatherOnce

        let constraints = RTCMediaConstraints(mandatoryConstraints: nil, optionalConstraints: nil)
        guard let pc = factory.peerConnection(with: config, constraints: constraints, delegate: self) else {
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

        let offerConstraints = RTCMediaConstraints(
            mandatoryConstraints: ["OfferToReceiveVideo": "true"],
            optionalConstraints: nil
        )
        pc.offer(for: offerConstraints) { [weak self] sdp, err in
            guard let self else { return }
            if let err {
                Task { @MainActor in
                    self.statusText = "Offer 失败"
                    self.errorMessage = err.localizedDescription
                }
                return
            }
            guard let sdp else { return }
            pc.setLocalDescription(sdp) { setErr in
                if let setErr {
                    Task { @MainActor in
                        self.statusText = "信令错误"
                        self.errorMessage = setErr.localizedDescription
                    }
                    return
                }
                Task { @MainActor in
                    await self.waitForLocalCandidates(pc: pc, timeoutMs: 250)
                    let finalOffer = pc.localDescription ?? sdp
                    await self.sendOfferToHost(sdp: finalOffer, host: cleanHost, port: port, pin: cleanPin, pc: pc)
                }
            }
        }
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

    private func sendOfferToHost(sdp: RTCSessionDescription, host: String, port: Int, pin: String, pc: RTCPeerConnection) async {
        guard let url = URL(string: "http://\(host):\(port)/api/connect") else {
            statusText = "地址无效"
            return
        }
        var req = URLRequest(url: url, timeoutInterval: 4.0)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONEncoder().encode(ConnectPayload(pin: pin, sdp: sdp.sdp, type: "offer"))

        do {
            let (data, response) = try await URLSession.shared.data(for: req)
            guard let http = response as? HTTPURLResponse else {
                statusText = "网络异常"
                return
            }
            if http.statusCode == 401 {
                statusText = "PIN 错误"
                errorMessage = "PIN 码校验失败，请核对电脑端托盘或控制台显示的 6 位数字"
                disconnect(resetTarget: false)
                return
            }
            guard http.statusCode == 200 else {
                statusText = "握手失败(\(http.statusCode))"
                errorMessage = String(data: data, encoding: .utf8) ?? "HTTP \(http.statusCode)"
                return
            }

            let reply = try JSONDecoder().decode(ConnectReply.self, from: data)
            if let w = reply.width, let h = reply.height, w > 0, h > 0 {
                desktopSize = CGSize(width: w, height: h)
            }
            let answer = RTCSessionDescription(type: .answer, sdp: reply.sdp)
            pc.setRemoteDescription(answer) { [weak self] err in
                if let err {
                    Task { @MainActor in
                        self?.statusText = "SDP 异常"
                        self?.errorMessage = err.localizedDescription
                    }
                }
            }
        } catch {
            statusText = "连接超时"
            errorMessage = "无法连接到 \(host):\(port)（\(error.localizedDescription)）"
        }
    }

    func reconnectIfNeeded() {
        guard !isConnected, !activeHost.isEmpty, activePin.count == 6 else { return }
        connect(host: activeHost, port: activePort, pin: activePin)
    }

    func disconnect(resetTarget: Bool = true) {
        pingTimer?.invalidate()
        pingTimer = nil
        statsTimer?.invalidate()
        statsTimer = nil
        controlChannel?.close()
        controlChannel = nil
        peerConnection?.close()
        peerConnection = nil
        remoteVideoTrack = nil
        isConnected = false
        blackoutEnabled = false
        statusText = "未连接"
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
                self.remoteVideoTrack = track
            }
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didRemove stream: RTCMediaStream) {}
    nonisolated func peerConnectionShouldNegotiate(_ peerConnection: RTCPeerConnection) {}

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didChange newState: RTCIceConnectionState) {
        Task { @MainActor in
            switch newState {
            case .connected, .completed:
                self.isConnected = true
                self.statusText = "已直连"
                self.errorMessage = ""
                self.sendControl(["type": "request_idr"])
                self.startTelemetryTimers()
            case .disconnected:
                self.isConnected = false
                self.statusText = "重连中..."
            case .failed, .closed:
                self.isConnected = false
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
            self.controlChannel = dataChannel
        }
    }

    nonisolated func peerConnection(_ peerConnection: RTCPeerConnection, didAdd rtpReceiver: RTCRtpReceiver, streams mediaStreams: [RTCMediaStream]) {
        if let track = rtpReceiver.track as? RTCVideoTrack {
            Task { @MainActor in
                self.remoteVideoTrack = track
            }
        }
    }
}

extension WebRTCManager: RTCDataChannelDelegate {
    nonisolated func dataChannelDidChangeState(_ dataChannel: RTCDataChannel) {
        if dataChannel.readyState == .open {
            Task { @MainActor in
                self.sendControl(["type": "request_idr"])
            }
        }
    }

    nonisolated func dataChannel(_ dataChannel: RTCDataChannel, didReceiveMessageWith buffer: RTCDataBuffer) {
        guard !buffer.isBinary,
              let json = try? JSONSerialization.jsonObject(with: buffer.data) as? [String: Any],
              let type = json["type"] as? String else { return }

        Task { @MainActor in
            switch type {
            case "pong":
                if let ts = json["ts"] as? Int64 {
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
