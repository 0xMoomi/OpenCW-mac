import AppKit
import Combine
import SwiftUI

// The app is one fixed-size window plus a menu-bar item. Closing the window
// only hides it (and the Dock icon); the connection stays up and the
// menu-bar item remains. Quitting closes the socket, and the daemon tears the
// tunnel down when it does.
@main
@MainActor
enum OpenCWApp {
    static func main() {
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        app.setActivationPolicy(.regular)
        app.run()
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSWindowDelegate {
    private var window: NSWindow?
    private var statusMenu: StatusMenu?
    private let model = Model()

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.mainMenu = mainMenu()

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: Metrics.width, height: Metrics.height),
            styleMask: [.titled, .closable, .miniaturizable, .fullSizeContentView],
            backing: .buffered,
            defer: false
        )
        window.title = "OpenCW"
        window.titleVisibility = .hidden
        window.titlebarAppearsTransparent = true
        window.isMovableByWindowBackground = true
        window.collectionBehavior = [.fullScreenNone]
        window.standardWindowButton(.zoomButton)?.isEnabled = false
        // Upstream has a single light palette; keep it regardless of the system theme.
        window.appearance = NSAppearance(named: .aqua)
        window.backgroundColor = NSColor(Palette.canvasBg)
        window.isReleasedWhenClosed = false
        window.delegate = self
        // The title bar height differs between macOS releases; the content
        // runs under it, so the window grows by exactly that much.
        let titleBar = window.frame.height - window.contentLayoutRect.height
        let host = NSHostingView(rootView: ContentView(model: model, titleBar: titleBar))
        host.sizingOptions = []
        window.contentView = host
        window.setContentSize(NSSize(width: Metrics.width, height: Metrics.height + titleBar))
        window.center()
        self.window = window

        statusMenu = StatusMenu(model: model, open: { [weak self] in self?.showWindow() })
        showWindow()
        model.start()
    }

    /// Brings the window back, with the Dock icon.
    func showWindow() {
        NSApp.setActivationPolicy(.regular)
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    // Closing hides the window and the Dock icon; the menu-bar item stays.
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        sender.orderOut(nil)
        NSApp.setActivationPolicy(.accessory)
        return false
    }

    // Clicking the Dock icon or opening the app again shows the window.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showWindow()
        return false
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        false
    }

    private func mainMenu() -> NSMenu {
        let menu = NSMenu()
        let appItem = NSMenuItem()
        let appMenu = NSMenu()
        appMenu.addItem(withTitle: "OpenCW 가리기", action: #selector(NSApplication.hide(_:)), keyEquivalent: "h")
        appMenu.addItem(.separator())
        appMenu.addItem(withTitle: "OpenCW 종료", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        appItem.submenu = appMenu
        menu.addItem(appItem)

        let windowItem = NSMenuItem()
        let windowMenu = NSMenu(title: "윈도우")
        windowMenu.addItem(withTitle: "최소화", action: #selector(NSWindow.performMiniaturize(_:)), keyEquivalent: "m")
        windowMenu.addItem(withTitle: "닫기", action: #selector(NSWindow.performClose(_:)), keyEquivalent: "w")
        windowItem.submenu = windowMenu
        menu.addItem(windowItem)
        return menu
    }
}

/// A finished server change: where from and to, and the ping before and after.
struct ServerChange: Equatable {
    var from: Snapshot.Server
    var to: Snapshot.Server
    var fromPing: Int?
    var toPing: Int?

    /// "미국 로스앤젤레스 → 미국 댈러스"; the same place reads as another server there.
    var places: String {
        from.label == to.label ? "\(to.label)의 다른 서버" : "\(from.label) → \(to.label)"
    }

    /// "152 → 128 ms", when both pings are known.
    var pings: String? {
        guard let a = fromPing, let b = toPing else { return nil }
        return "\(a) → \(b) ms"
    }

    var summary: String {
        [places, pings].compactMap { $0 }.joined(separator: " · ")
    }
}

// Model holds the latest daemon snapshot for the views.
@MainActor
final class Model: ObservableObject {
    enum Link { case starting, reachable, unreachable }

    @Published private(set) var snap = Snapshot.initial
    @Published private(set) var link = Link.starting
    /// The most recent server change, kept for the menu-bar menu.
    @Published private(set) var lastChange: ServerChange?
    /// The change the window announces, for a few seconds.
    @Published private(set) var notice: ServerChange?
    private var client: DaemonClient?
    // The exit and ping of the current connection, to compare after a change.
    private var server: Snapshot.Server?
    private var ping: Int?
    private var noticeTimer: Timer?
    // Connect once per launch, on the helper's first answer; turning the
    // switch off afterwards is respected.
    private var autoConnected = false

    func start() {
        let path = ProcessInfo.processInfo.environment["OPENCW_SOCKET"] ?? "/var/run/opencw.sock"
        // The model lives as long as the app, so a strong capture is fine.
        let client = DaemonClient(path: path) { [self] event in
            DispatchQueue.main.async {
                MainActor.assumeIsolated { self.apply(event) }
            }
        }
        self.client = client
        client.start()
    }

    private func apply(_ event: DaemonClient.Event) {
        switch event {
        case .snapshot(let s):
            link = .reachable
            track(s)
            snap = s
            if !autoConnected {
                autoConnected = true
                // While the catalog loads the helper keeps this as the wanted
                // end state and connects when it is ready.
                if s.state == .off && s.canToggle { client?.send(cmd: "toggle") }
            }
        case .unreachable:
            link = .unreachable
            snap = .unreachable
            server = nil
            ping = nil
        }
    }

    /// Notices a new exit after a reconnect. Switching passes through
    /// "connecting", never "off", so turning OpenCW off forgets the exit and
    /// a later connect is not reported as a change.
    private func track(_ s: Snapshot) {
        switch s.state {
        case .connected:
            guard let now = s.server else { return }
            if let before = server, before.ip != now.ip {
                let change = ServerChange(from: before, to: now, fromPing: ping, toPing: s.pingMS)
                lastChange = change
                announce(change)
            }
            server = now
            ping = s.pingMS ?? ping
        case .connecting:
            break
        case .off:
            server = nil
            ping = nil
        }
    }

    private func announce(_ change: ServerChange) {
        notice = change
        noticeTimer?.invalidate()
        noticeTimer = Timer.scheduledTimer(withTimeInterval: 4, repeats: false) { [weak self] _ in
            MainActor.assumeIsolated { self?.notice = nil }
        }
    }

    /// The switch is inert until the helper answers and while the helper
    /// restores the network, exactly when it would ignore a toggle.
    var toggleEnabled: Bool { link == .reachable && snap.canToggle }

    var switchEnabled: Bool { link == .reachable && snap.canSwitch }

    /// Upstream paints the subtitle red only for a failed verdict; here also
    /// when the helper cannot be reached.
    var failed: Bool { link == .unreachable || !snap.error.isEmpty }

    func toggle() {
        guard toggleEnabled else { return }
        client?.send(cmd: "toggle")
    }

    func switchServer() {
        guard switchEnabled else { return }
        client?.send(cmd: "switch")
    }
}
