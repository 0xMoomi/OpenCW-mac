import AppKit
import Combine
import SwiftUI

/// The menu-bar item: a maple leaf, lit with the live ping while connected
/// and dimmed otherwise, and a menu to open the window, connect, change
/// server or quit.
@MainActor
final class StatusMenu: NSObject, NSMenuDelegate {
    private let model: Model
    private let open: () -> Void
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let menu = NSMenu()
    private var watch: AnyCancellable?

    init(model: Model, open: @escaping () -> Void) {
        self.model = model
        self.open = open
        super.init()
        if let button = item.button {
            button.image = Self.leafImage()
            button.imagePosition = .imageLeading
            button.setAccessibilityLabel("OpenCW")
        }
        menu.autoenablesItems = false
        menu.delegate = self
        item.menu = menu
        // objectWillChange fires before the change lands; read it a turn later.
        watch = model.objectWillChange.sink { [weak self] _ in
            DispatchQueue.main.async {
                MainActor.assumeIsolated { self?.refresh() }
            }
        }
        refresh()
    }

    /// The leaf from the window's switch, as a template image so it follows
    /// the menu bar's colours.
    private static func leafImage() -> NSImage? {
        let renderer = ImageRenderer(content: Leaf(color: .black).frame(width: 16, height: 16))
        renderer.scale = 2
        guard let image = renderer.nsImage else { return nil }
        image.size = NSSize(width: 16, height: 16)
        image.isTemplate = true
        return image
    }

    private func refresh() {
        let s = model.snap
        guard let button = item.button else { return }
        let connected = s.state == .connected
        button.appearsDisabled = !connected
        if connected, let ms = s.pingMS {
            button.attributedTitle = NSAttributedString(string: " \(ms)ms", attributes: [
                .font: NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .medium),
            ])
        } else {
            button.attributedTitle = NSAttributedString(string: "")
        }
        button.toolTip = "OpenCW · \(statusLine)"
        rebuild()
    }

    /// "연결됨 · 미국 시애틀 · 145 ms", or the window's headline otherwise.
    private var statusLine: String {
        let s = model.snap
        guard s.state == .connected else { return s.title }
        var parts = ["연결됨"]
        if let server = s.server { parts.append(server.label) }
        if let ms = s.pingMS { parts.append("\(ms) ms") }
        return parts.joined(separator: " · ")
    }

    func menuNeedsUpdate(_ menu: NSMenu) { rebuild() }

    private func rebuild() {
        let s = model.snap
        menu.removeAllItems()
        add("OpenCW 열기", #selector(openWindow))
        menu.addItem(.separator())
        add(statusLine, nil)
        add(s.state == .off ? "연결" : "연결 해제", #selector(toggle), enabled: model.toggleEnabled)
        add("서버 변경", #selector(switchServer), enabled: model.switchEnabled)
        if let change = model.lastChange {
            menu.addItem(.separator())
            add("최근 서버 변경", nil)
            add(change.summary, nil)
        }
        menu.addItem(.separator())
        add("OpenCW 종료", #selector(quit), key: "q")
    }

    /// Adds an item; one without an action is a greyed-out line of text.
    private func add(_ title: String, _ action: Selector?, enabled: Bool = true, key: String = "") {
        let entry = NSMenuItem(title: title, action: action, keyEquivalent: key)
        entry.target = self
        entry.isEnabled = action != nil && enabled
        menu.addItem(entry)
    }

    @objc private func openWindow() { open() }
    @objc private func toggle() { model.toggle() }
    @objc private func switchServer() { model.switchServer() }
    @objc private func quit() { NSApp.terminate(nil) }
}
