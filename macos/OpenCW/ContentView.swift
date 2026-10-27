import SwiftUI

/// The whole window: a title row in the transparent title bar, then either
/// the home page or the info page, as upstream swaps its details page in place.
struct ContentView: View {
    @ObservedObject var model: Model
    var titleBar: CGFloat
    @State private var showInfo = false

    var body: some View {
        VStack(spacing: 0) {
            TitleRow(height: titleBar)
            ZStack(alignment: .topTrailing) {
                if showInfo {
                    InfoView()
                } else {
                    HomeView(model: model)
                }
                IconButton(glyph: showInfo ? .close : .info, label: showInfo ? "닫기" : "정보") {
                    showInfo.toggle()
                }
                .keyboardShortcut(showInfo ? .cancelAction : .init("i", modifiers: .command))
                .padding(.top, 34 - 18)
                .padding(.trailing, 34 - 18)
            }
            .frame(width: Metrics.width, height: Metrics.height)
        }
        .frame(width: Metrics.width, height: Metrics.height + titleBar)
        .background(Palette.canvasBg)
        .ignoresSafeArea()
    }
}

/// "OpenCW" centred like a native title, with a quiet "Mac.ver" tag.
private struct TitleRow: View {
    var height: CGFloat

    var body: some View {
        HStack(spacing: 6) {
            Text("OpenCW")
                .font(.system(size: 13, weight: .semibold))
                .foregroundColor(Palette.ink)
            Text("Mac.ver")
                .font(.system(size: 10, weight: .semibold))
                .foregroundColor(Palette.muted)
                .padding(.horizontal, 6)
                .padding(.vertical, 1.5)
                .overlay(Capsule().strokeBorder(Palette.border, lineWidth: 1))
        }
        .frame(width: Metrics.width, height: height)
        .accessibilityElement(children: .combine)
    }
}

/// What a player needs: whether OpenCW is on, what it is doing, and the ping.
struct HomeView: View {
    @ObservedObject var model: Model

    var body: some View {
        let s = model.snap
        let on = s.state != .off
        let spinning = s.state == .connecting || s.busy
        let failed = !model.reachable || !s.error.isEmpty

        VStack(spacing: 0) {
            MapleToggle(on: on, busy: spinning, enabled: model.toggleEnabled) { model.toggle() }
                .accessibilityLabel("OpenCW. \(s.title). \(s.subtitle)")
                .padding(.bottom, 28)
            Text(s.title)
                .font(Fonts.headline)
                .foregroundColor(Palette.ink)
                .lineLimit(1)
                .frame(height: 40)
                .padding(.bottom, 4)
            Text(s.subtitle)
                .font(Fonts.body)
                .foregroundColor(failed ? Palette.bad : Palette.muted)
                .multilineTextAlignment(.center)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
                .frame(minHeight: 26)
            if !s.error.isEmpty {
                // Upstream keeps the raw cause small and quiet under the red verdict.
                Text(s.error)
                    .font(Fonts.small)
                    .foregroundColor(Palette.muted)
                    .multilineTextAlignment(.center)
                    .lineLimit(3)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 6)
                    .textSelection(.enabled)
            }
            if let progress = s.progress {
                ProgressBar(value: progress)
                    .padding(.top, 28)
            }
            if s.state == .connected {
                if let ping = s.pingMS {
                    PingRow(ms: ping, signal: s.signal)
                        .padding(.top, 26)
                }
                Button("서버 변경") { model.switchServer() }
                    .buttonStyle(QuietButtonStyle(width: 120, height: 40))
                    .disabled(!s.canSwitch)
                    .padding(.top, s.pingMS == nil ? 26 : 22)
            }
        }
        .padding(.horizontal, 20)
        .frame(width: Metrics.width, height: Metrics.height)
        .animation(.easeOut(duration: 0.3), value: s)
    }
}

/// Signal bars for the verdict and the latency beside them, centred as one unit.
private struct PingRow: View {
    var ms: Int
    var signal: Int

    var body: some View {
        // The daemon grades 0–3; upstream lights 3 of 4 green for this tier.
        let color = signal >= 3 ? Palette.good : signal == 2 ? Palette.fair : Palette.bad
        HStack(spacing: 0) {
            SignalBars(lit: max(0, min(4, signal)), color: color)
                .padding(.trailing, 14)
            Text("\(ms)")
                .font(Fonts.ping)
                .foregroundColor(Palette.ink)
                .monospacedDigit()
            Text("ms")
                .font(Fonts.body)
                .foregroundColor(Palette.muted)
                .padding(.leading, 5)
                .offset(y: 4)
        }
        .frame(height: 44)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("핑 \(ms)ms")
    }
}
