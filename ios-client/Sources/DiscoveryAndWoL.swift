import Foundation
import Darwin

struct DiscoveredHost: Identifiable, Codable, Equatable {
    var id: String { "\(primaryIP):\(httpPort)" }
    let service: String
    let version: String
    let hostname: String
    let ips: [String]
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

@MainActor
final class DiscoveryAndWoL: ObservableObject {
    @Published var hosts: [DiscoveredHost] = []
    @Published var isScanning: Bool = false
    @Published var wakeStatusMessage: String = ""

    private let discoveryMagic = "ZERODESK_DISCOVER"
    private let discoveryPort: UInt16 = 9528

    func discoverHosts(timeoutSeconds: Double = 1.2) {
        guard !isScanning else { return }
        isScanning = true

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let found = self.performUDPBroadcastDiscovery(timeout: timeoutSeconds)
            DispatchQueue.main.async {
                self.hosts = found
                self.isScanning = false
            }
        }
    }

    private nonisolated func performUDPBroadcastDiscovery(timeout: Double) -> [DiscoveredHost] {
        let fd = Darwin.socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP)
        guard fd >= 0 else { return [] }
        defer { Darwin.close(fd) }

        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_BROADCAST, &yes, socklen_t(MemoryLayout<Int32>.size))
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))

        var tv = timeval(tv_sec: 0, tv_usec: 350_000)
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, socklen_t(MemoryLayout<timeval>.size))

        var bcastAddr = sockaddr_in()
        bcastAddr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        bcastAddr.sin_family = sa_family_t(AF_INET)
        bcastAddr.sin_port = discoveryPort.bigEndian
        bcastAddr.sin_addr = in_addr(s_addr: INADDR_BROADCAST)

        let payload = Array(discoveryMagic.utf8)
        _ = payload.withUnsafeBytes { ptr in
            withUnsafePointer(to: &bcastAddr) { addrPtr in
                addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                    sendto(fd, ptr.baseAddress, payload.count, 0, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
                }
            }
        }

        var results: [DiscoveredHost] = []
        var seen = Set<String>()
        let deadline = Date().addingTimeInterval(timeout)
        var recvBuf = [UInt8](repeating: 0, count: 2048)

        while Date() < deadline {
            var srcAddr = sockaddr_in()
            var srcLen = socklen_t(MemoryLayout<sockaddr_in>.size)
            let n = recvBuf.withUnsafeMutableBytes { bufPtr in
                withUnsafeMutablePointer(to: &srcAddr) { addrPtr in
                    addrPtr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                        recvfrom(fd, bufPtr.baseAddress, recvBuf.count, 0, sa, &srcLen)
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
        return results
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
                            sendto(fd, ptr.baseAddress, packet.count, 0, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
                        }
                    }
                }
                if sent == packet.count {
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
