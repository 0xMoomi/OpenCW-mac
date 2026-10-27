import AppKit
import SwiftUI

// The app is one fixed-size window. Closing it quits the app; the daemon
// tears the tunnel down when the app's socket closes, so quitting is enough.
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
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var window: NSWindow?
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
        // The title bar height differs between macOS releases; the content
        // runs under it, so the window grows by exactly that much.
        let titleBar = window.frame.height - window.contentLayoutRect.height
        let host = NSHostingView(rootView: ContentView(model: model, titleBar: titleBar))
        host.sizingOptions = []
        window.contentView = host
        window.setContentSize(NSSize(width: Metrics.width, height: Metrics.height + titleBar))
        window.center()
        window.makeKeyAndOrderFront(nil)
        self.window = window

        NSApp.activate(ignoringOtherApps: true)
        model.start()
    }

    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool {
        true
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

// Model holds the latest daemon snapshot for the view.
@MainActor
final class Model: ObservableObject {
    enum Link { case starting, reachable, unreachable }

    @Published private(set) var snap = Snapshot.initial
    @Published private(set) var link = Link.starting
    private var client: DaemonClient?

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
            snap = s
        case .unreachable:
            link = .unreachable
            snap = .unreachable
        }
    }

    /// The switch is inert until the helper answers and while the helper
    /// restores the network, exactly when it would ignore a toggle.
    var toggleEnabled: Bool { link == .reachable && snap.canToggle }

    /// Upstream paints the subtitle red only for a failed verdict; here also
    /// when the helper cannot be reached.
    var failed: Bool { link == .unreachable || !snap.error.isEmpty }

    func toggle() {
        guard toggleEnabled else { return }
        client?.send(cmd: "toggle")
    }

    func switchServer() {
        guard link == .reachable, snap.canSwitch else { return }
        client?.send(cmd: "switch")
    }
}
