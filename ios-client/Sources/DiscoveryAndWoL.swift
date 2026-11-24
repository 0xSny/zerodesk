import Foundation
import Combine
import Darwin

struct DiscoveredHost: Identifiable, Codable, Equatable {
    var id: String { "\(primaryIP):\(httpPort)" }
    let service: String
    let version: String?
    let hostname: String
    let ips: [String]?
    let primaryIP: String
    let macAddress: String
    let httpPort: Int
    let discoveryPort: Int

    enum CodingKeys: String, CodingKey {
        case service, version, hostname, ips
        case primaryIP = "primary_ip"
        case macAddress = "mac_address"
        case httpPort = "http_port"
        case discoveryPort = "discovery_port"
    }
}

private struct HealthProbeReply: Decodable {
    let status: String?
    let service: String?
    let version: String?
    let hostname: String?
    let primaryIP: String?
    let ips: [String]?
    let macAddress: String?
    let httpPort: Int?
    let discoveryPort: Int?

    enum CodingKeys: String, CodingKey {
        case status, service, version, hostname, ips
        case primaryIP = "primary_ip"
        case macAddress = "mac_address"
        case httpPort = "http_port"
        case discoveryPort = "discovery_port"
    }
}

@MainActor
final class DiscoveryAndWoL: ObservableObject {
    @Published var hosts: [DiscoveredHost] = []
    @Published var isScanning: Bool = false
    @Published var scanStatusText: String = ""
    @Published var wakeStatusMessage: String = ""

    private let discoveryMagic = "ZERODESK_DISCOVER"
    private let discoveryPort: UInt16 = 9528

    func discoverHosts(cachedIP: String = "", httpPort: Int = 9527) {
        guard !isScanning else { return }
        isScanning = true
        scanStatusText = "正在扫描同 Wi-Fi 电脑..."

        let cleanCached = cachedIP.trimmingCharacters(in: .whitespacesAndNewlines)

        Task { [weak self] in
            guard let self else { return }
            let subnetInfo = self.getLocalWiFiSubnet()
            let bcastIP = subnetInfo?.broadcast ?? "255.255.255.255"

            // Run UDP discovery + HTTP /24 subnet scan in parallel (HTTP scan works even without multicast entitlement)
            async let udpTask = self.performUDPDiscovery(
                subnetBroadcast: bcastIP,
                cachedIP: cleanCached,
                timeout: 1.0
            )
            async let httpTask = self.performHTTPSubnetScan(
                prefix24: subnetInfo?.prefix24,
                cachedIP: cleanCached,
                port: httpPort > 0 ? httpPort : 9527
            )

            let udpFound = await udpTask
            let httpFound = await httpTask

            var merged: [DiscoveredHost] = []
            var seen = Set<String>()
            for h in udpFound + httpFound {
                if !seen.contains(h.id) {
                    seen.insert(h.id)
                    merged.append(h)
                }
            }

            self.hosts = merged
            self.isScanning = false
            if merged.isEmpty {
                self.scanStatusText = "未自动发现主机，请确认处于同一 Wi-Fi 并检查本地网络权限或手动输入 IP"
            } else {
                self.scanStatusText = "已发现 \(merged.count) 台电脑"
            }
        }
    }

    func probeHost(ip: String, port: Int) async -> (ok: Bool, message: String, host: DiscoveredHost?) {
        let cleanIP = ip.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleanIP.isEmpty, let url = URL(string: "http://\(cleanIP):\(port)/api/health") else {
            return (false, "IP 或端口格式无效", nil)
        }
        var req = URLRequest(url: url, timeoutInterval: 2.0)
        req.httpMethod = "GET"

        do {
            let (data, resp) = try await URLSession.shared.data(for: req)
            guard let http = resp as? HTTPURLResponse, http.statusCode == 200 else {
                return (false, "HTTP 状态异常", nil)
            }
            if let decoded = try? JSONDecoder().decode(HealthProbeReply.self, from: data),
               decoded.service == "zerodesk-host" {
                let h = DiscoveredHost(
                    service: "ZeroDesk",
                    version: decoded.version,
                    hostname: decoded.hostname ?? cleanIP,
                    ips: decoded.ips ?? [cleanIP],
                    primaryIP: decoded.primaryIP ?? cleanIP,
                    macAddress: decoded.macAddress ?? "",
                    httpPort: decoded.httpPort ?? port,
                    discoveryPort: decoded.discoveryPort ?? 9528
                )
                return (true, "连通正常：\(h.hostname) (\(cleanIP):\(port))", h)
            }
            return (true, "端口可达：\(cleanIP):\(port)", nil)
        } catch {
            return (false, "无法访问 \(cleanIP):\(port)（\(error.localizedDescription)）", nil)
        }
    }

    private nonisolated func getLocalWiFiSubnet() -> (ip: String, prefix24: String, broadcast: String)? {
        var ifaddr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddr) == 0, let firstAddr = ifaddr else { return nil }
        defer { freeifaddrs(ifaddr) }

        var fallback: (String, String, String)?
        var ptr: UnsafeMutablePointer<ifaddrs>? = firstAddr
        while let current = ptr {
            let flags = Int32(current.pointee.ifa_flags)
            let isUp = (flags & IFF_UP) != 0
            let isLoopback = (flags & IFF_LOOPBACK) != 0
            if isUp && !isLoopback, let sa = current.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) {
                let name = String(cString: current.pointee.ifa_name)
                var addrCopy = sa.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee }
                var buf = [CChar](repeating: 0, count: Int(INET_ADDRSTRLEN))
                if inet_ntop(AF_INET, &addrCopy.sin_addr, &buf, socklen_t(INET_ADDRSTRLEN)) != nil {
                    let ipStr = String(cString: buf)
                    let parts = ipStr.split(separator: ".")
                    if parts.count == 4 && !ipStr.hasPrefix("169.254.") {
                        let prefix24 = "\(parts[0]).\(parts[1]).\(parts[2])"
                        let bcast = "\(prefix24).255"
                        if name == "en0" {
                            return (ipStr, prefix24, bcast)
                        }
                        if fallback == nil {
                            fallback = (ipStr, prefix24, bcast)
                        }
                    }
                }
            }
            ptr = current.pointee.ifa_next
        }
        return fallback
    }

    private nonisolated func performUDPDiscovery(subnetBroadcast: String, cachedIP: String, timeout: Double) async -> [DiscoveredHost] {
        await withCheckedContinuation { cont in
            DispatchQueue.global(qos: .userInitiated).async {
                let fd = Darwin.socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP)
                guard fd >= 0 else {
                    cont.resume(returning: [])
                    return
                }
                defer { Darwin.close(fd) }

                var yes: Int32 = 1
                setsockopt(fd, SOL_SOCKET, SO_BROADCAST, &yes, socklen_t(MemoryLayout<Int32>.size))
                setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))

                var tv = timeval(tv_sec: 0, tv_usec: 250_000)
                setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

                let payload = Array(self.discoveryMagic.utf8)
                let payloadLen = payload.count

                var targets = [subnetBroadcast, "255.255.255.255"]
                if !cachedIP.isEmpty {
                    targets.insert(cachedIP, at: 0)
                }

                for target in Set(targets) {
                    var addr = sockaddr_in()
                    addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
                    addr.sin_family = sa_family_t(AF_INET)
                    addr.sin_port = self.discoveryPort.bigEndian
                    if inet_pton(AF_INET, target, &addr.sin_addr) == 1 {
                        _ = payload.withUnsafeBytes { ptr in
                            withUnsafePointer(to: &addr) { addrPtr in
                                addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                                    sendto(fd, ptr.baseAddress, payloadLen, 0, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
                                }
                            }
                        }
                    }
                }

                var results: [DiscoveredHost] = []
                var seen = Set<String>()
                let deadline = Date().addingTimeInterval(timeout)
                var recvBuf = [UInt8](repeating: 0, count: 2048)
                let recvCapacity = recvBuf.count

                while Date() < deadline {
                    var srcAddr = sockaddr_in()
                    var srcLen = socklen_t(MemoryLayout<sockaddr_in>.size)
                    let n = recvBuf.withUnsafeMutableBytes { bufPtr in
                        withUnsafeMutablePointer(to: &srcAddr) { addrPtr in
                            addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                                recvfrom(fd, bufPtr.baseAddress, recvCapacity, 0, sa, &srcLen)
                            }
                        }
                    }
                    if n <= 0 { continue }
                    let data = Data(recvBuf[0..<n])
                    if let host = try? JSONDecoder().decode(DiscoveredHost.self, from: data) {
                        if !seen.contains(host.id) {
                            seen.insert(host.id)
                            results.append(host)
                        }
                    }
                }
                cont.resume(returning: results)
            }
        }
    }

    private nonisolated func performHTTPSubnetScan(prefix24: String?, cachedIP: String, port: Int) async -> [DiscoveredHost] {
        var candidates: [String] = []
        if !cachedIP.isEmpty {
            candidates.append(cachedIP)
        }
        if let prefix24 {
            for lastOctet in 1...254 {
                let ip = "\(prefix24).\(lastOctet)"
                if ip != cachedIP {
                    candidates.append(ip)
                }
            }
        }
        guard !candidates.isEmpty else { return [] }

        let cfg = URLSessionConfiguration.ephemeral
        cfg.timeoutIntervalForRequest = 0.50
        cfg.timeoutIntervalForResource = 0.60
        cfg.waitsForConnectivity = false
        let session = URLSession(configuration: cfg)
        defer { session.invalidateAndCancel() }

        var found: [DiscoveredHost] = []
        let batchSize = 42
        var idx = 0

        while idx < candidates.count {
            let end = min(idx + batchSize, candidates.count)
            let slice = Array(candidates[idx..<end])
            idx = end

            let batchResults = await withTaskGroup(of: DiscoveredHost?.self) { group in
                for ip in slice {
                    group.addTask {
                        guard let url = URL(string: "http://\(ip):\(port)/api/health") else { return nil }
                        var req = URLRequest(url: url, timeoutInterval: 0.50)
                        req.httpMethod = "GET"
                        guard let (data, resp) = try? await session.data(for: req),
                              let http = resp as? HTTPURLResponse,
                              http.statusCode == 200,
                              let decoded = try? JSONDecoder().decode(HealthProbeReply.self, from: data),
                              decoded.service == "zerodesk-host" else {
                            return nil
                        }
                        return DiscoveredHost(
                            service: "ZeroDesk",
                            version: decoded.version,
                            hostname: decoded.hostname ?? ip,
                            ips: decoded.ips ?? [ip],
                            primaryIP: decoded.primaryIP ?? ip,
                            macAddress: decoded.macAddress ?? "",
                            httpPort: decoded.httpPort ?? port,
                            discoveryPort: decoded.discoveryPort ?? 9528
                        )
                    }
                }
                var out: [DiscoveredHost] = []
                for await item in group {
                    if let item { out.append(item) }
                }
                return out
            }
            found.append(contentsOf: batchResults)
            if !found.isEmpty && cachedIP.isEmpty == false && found.contains(where: { $0.primaryIP == cachedIP }) {
                break
            }
        }
        return found
    }

    /// Dual-channel Wake-on-LAN:
    /// 1) Direct UDP magic packet from iPhone over local Wi-Fi (255.255.255.255:9 & subnet broadcast)
    /// 2) Optional N1 box HTTP WoL relay (:9529/api/wol) when configured
    func wakeComputer(mac: String, pcIP: String, n1IP: String, pin: String) {
        let trimmedMAC = mac.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedMAC.isEmpty else {
            wakeStatusMessage = "缺少电脑网卡 MAC 地址，请先连接一次自动学习或手动填写"
            return
        }

        wakeStatusMessage = "正在发送唤醒指令..."
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let udpOK = self?.sendMagicPacket(macString: trimmedMAC, pcIP: pcIP) ?? false
            var n1OK = false
            let trimmedN1 = n1IP.trimmingCharacters(in: .whitespacesAndNewlines)
            if !trimmedN1.isEmpty {
                n1OK = self?.triggerN1WoL(n1IP: trimmedN1, pin: pin) ?? false
            }

            DispatchQueue.main.async {
                if udpOK || n1OK {
                    self?.wakeStatusMessage = n1OK
                        ? "已通过本机与 N1 双通道发送唤醒魔术包"
                        : "已发送局域网 Wake-on-LAN 魔术包，等待电脑唤醒..."
                } else {
                    self?.wakeStatusMessage = "魔术包发送失败，请检查 MAC 格式（如 E8:9C:25:9A:80:A0）"
                }
            }
        }
    }

    private nonisolated func parseMAC(_ mac: String) -> [UInt8]? {
        let cleaned = mac.replacingOccurrences(of: "-", with: ":")
        let parts = cleaned.split(separator: ":")
        guard parts.count == 6 else { return nil }
        var bytes: [UInt8] = []
        bytes.reserveCapacity(6)
        for p in parts {
            guard let b = UInt8(p, radix: 16) else { return nil }
            bytes.append(b)
        }
        return bytes
    }

    private nonisolated func sendMagicPacket(macString: String, pcIP: String) -> Bool {
        guard let macBytes = parseMAC(macString) else { return false }

        var packet = [UInt8](repeating: 0xFF, count: 6)
        for _ in 0..<16 {
            packet.append(contentsOf: macBytes)
        }

        let fd = Darwin.socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP)
        guard fd >= 0 else { return false }
        defer { Darwin.close(fd) }

        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_BROADCAST, &yes, socklen_t(MemoryLayout<Int32>.size))

        var targets = ["255.255.255.255"]
        let octets = pcIP.split(separator: ".")
        if octets.count == 4 {
            targets.append("\(octets[0]).\(octets[1]).\(octets[2]).255")
        }

        let packetLen = packet.count
        var sentAny = false
        for targetIP in targets {
            for port: UInt16 in [9, 7] {
                var addr = sockaddr_in()
                addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
                addr.sin_family = sa_family_t(AF_INET)
                addr.sin_port = port.bigEndian
                inet_pton(AF_INET, targetIP, &addr.sin_addr)

                let sent = packet.withUnsafeBytes { ptr in
                    withUnsafePointer(to: &addr) { addrPtr in
                        addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                            sendto(fd, ptr.baseAddress, packetLen, 0, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
                        }
                    }
                }
                if sent == packetLen {
                    sentAny = true
                }
            }
        }
        return sentAny
    }

    private nonisolated func triggerN1WoL(n1IP: String, pin: String) -> Bool {
        guard let url = URL(string: "http://\(n1IP):9529/api/wol") else { return false }
        var req = URLRequest(url: url, timeoutInterval: 2.0)
        req.httpMethod = "POST"
        req.setValue("application/json", forHTTPHeaderField: "Content-Type")
        req.httpBody = try? JSONSerialization.data(withJSONObject: ["pin": pin])

        let sem = DispatchSemaphore(value: 0)
        var ok = false
        URLSession.shared.dataTask(with: req) { _, resp, _ in
            if let http = resp as? HTTPURLResponse, (200...299).contains(http.statusCode) {
                ok = true
            }
            sem.signal()
        }.resume()
        _ = sem.wait(timeout: .now() + 2.2)
        return ok
    }
}
