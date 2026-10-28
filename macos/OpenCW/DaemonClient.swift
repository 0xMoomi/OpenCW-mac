import Darwin
import Foundation

/// One view snapshot from the daemon, sent as a line of JSON.
struct Snapshot: Decodable, Equatable, Sendable {
    enum State: String, Decodable, Sendable {
        case off, connecting, connected
    }

    /// The live exit: where it is, its address and relay protocol.
    struct Server: Decodable, Equatable, Sendable {
        var label: String
        var ip: String
        var `protocol`: String
    }

    var state: State
    var title: String
    var subtitle: String
    var progress: Double?
    var pingMS: Int?
    /// "game": round trip through the relay to the game servers' region;
    /// "relay": to the relay only, while that region does not answer.
    var pingSource: String
    /// Lit bars out of 4, as upstream draws them: 0 none, 1 bad, 2 fair, 3–4 good.
    var signal: Int
    /// False while the daemon restores the network and ignores toggle.
    var canToggle: Bool
    var canSwitch: Bool
    var busy: Bool
    var error: String
    /// Set while connected.
    var server: Server?

    enum CodingKeys: String, CodingKey {
        case state, title, subtitle, progress, signal, busy, error, server
        case pingMS = "ping_ms"
        case pingSource = "ping_source"
        case canToggle = "can_toggle"
        case canSwitch = "can_switch"
    }

    init(state: State, title: String, subtitle: String, progress: Double? = nil, pingMS: Int? = nil,
         pingSource: String = "", signal: Int = 0, canToggle: Bool = false, canSwitch: Bool = false,
         busy: Bool = false, error: String = "", server: Server? = nil) {
        self.state = state
        self.title = title
        self.subtitle = subtitle
        self.progress = progress
        self.pingMS = pingMS
        self.pingSource = pingSource
        self.signal = signal
        self.canToggle = canToggle
        self.canSwitch = canSwitch
        self.busy = busy
        self.error = error
        self.server = server
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        state = (try? c.decode(State.self, forKey: .state)) ?? .off
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        subtitle = try c.decodeIfPresent(String.self, forKey: .subtitle) ?? ""
        progress = try c.decodeIfPresent(Double.self, forKey: .progress)
        pingMS = try c.decodeIfPresent(Int.self, forKey: .pingMS)
        pingSource = try c.decodeIfPresent(String.self, forKey: .pingSource) ?? ""
        signal = try c.decodeIfPresent(Int.self, forKey: .signal) ?? 0
        canToggle = try c.decodeIfPresent(Bool.self, forKey: .canToggle) ?? false
        canSwitch = try c.decodeIfPresent(Bool.self, forKey: .canSwitch) ?? false
        busy = try c.decodeIfPresent(Bool.self, forKey: .busy) ?? false
        error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
        server = try? c.decodeIfPresent(Server.self, forKey: .server)
    }

    /// Shown, muted and inert, until the first connect attempt settles.
    static let initial = Snapshot(state: .off, title: "연결 안 됨", subtitle: "게임을 실행하기 전에 연결해주세요")

    static let unreachable = Snapshot(
        state: .off,
        title: "도우미 연결 실패",
        subtitle: "OpenCW 도우미에 연결할 수 없습니다. 앱을 다시 설치해주세요."
    )
}

/// DaemonClient keeps a newline-delimited JSON connection to the root daemon
/// over a unix socket. A background thread connects, reads snapshots and
/// reconnects every two seconds while the daemon is unreachable.
final class DaemonClient: @unchecked Sendable {
    enum Event: Sendable {
        case snapshot(Snapshot)
        case unreachable
    }

    private let path: String
    private let onEvent: @Sendable (Event) -> Void
    private let lock = NSLock()
    private var fd: Int32 = -1 // guarded by lock

    init(path: String, onEvent: @escaping @Sendable (Event) -> Void) {
        self.path = path
        self.onEvent = onEvent
    }

    func start() {
        let thread = Thread { [self] in run() }
        thread.name = "OpenCW daemon client"
        thread.start()
    }

    /// Sends one command line; dropped silently while disconnected.
    func send(cmd: String) {
        let line = Array("{\"cmd\":\"\(cmd)\"}\n".utf8)
        lock.lock()
        defer { lock.unlock() }
        guard fd >= 0 else { return }
        var sent = 0
        while sent < line.count {
            let n = line[sent...].withUnsafeBytes { Darwin.write(fd, $0.baseAddress, $0.count) }
            if n < 0 && errno == EINTR { continue }
            if n <= 0 { return } // the reader notices the broken socket
            sent += n
        }
    }

    private func run() {
        while true {
            let socket = connectSocket()
            if socket >= 0 {
                setFD(socket)
                readLines(socket)
                setFD(-1)
                close(socket)
            }
            onEvent(.unreachable)
            Thread.sleep(forTimeInterval: 2)
        }
    }

    private func setFD(_ value: Int32) {
        lock.lock()
        fd = value
        lock.unlock()
    }

    private func connectSocket() -> Int32 {
        var addr = sockaddr_un()
        let bytes = Array(path.utf8)
        guard bytes.count < MemoryLayout.size(ofValue: addr.sun_path) else { return -1 }
        addr.sun_family = sa_family_t(AF_UNIX)
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        let s = socket(AF_UNIX, SOCK_STREAM, 0)
        guard s >= 0 else { return -1 }
        var one: Int32 = 1
        setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        let result = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        guard result == 0 else {
            close(s)
            return -1
        }
        return s
    }

    /// Reads until the socket closes, publishing each decodable line.
    private func readLines(_ socket: Int32) {
        let decoder = JSONDecoder()
        var pending = [UInt8]()
        var chunk = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = chunk.withUnsafeMutableBytes { Darwin.read(socket, $0.baseAddress, $0.count) }
            if n < 0 && errno == EINTR { continue }
            if n <= 0 { return }
            pending.append(contentsOf: chunk[0..<n])
            while let newline = pending.firstIndex(of: 0x0A) {
                let line = Data(pending[0..<newline])
                pending.removeSubrange(0...newline)
                if let snap = try? decoder.decode(Snapshot.self, from: line) {
                    onEvent(.snapshot(snap))
                }
            }
            if pending.count > 1 << 20 { return } // runaway line; reconnect
        }
    }
}
