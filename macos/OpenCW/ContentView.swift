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
                if let change = model.notice {
                    ChangeNotice(change: change)
                        .frame(maxWidth: .infinity)
                        .padding(.top, 14)
                        .transition(.move(edge: .top).combined(with: .opacity))
                }
                IconButton(glyph: showInfo ? .close : .info, label: showInfo ? "닫기" : "정보") {
                    showInfo.toggle()
                }
                .keyboardShortcut(showInfo ? .cancelAction : .init("i", modifiers: .command))
                .padding(.top, 34 - 18)
                .padding(.trailing, 34 - 18)
            }
            .frame(width: Metrics.width, height: Metrics.height)
            .animation(.easeOut(duration: 0.25), value: model.notice)
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
        // Upstream spins the knob while connecting and while restoring the
        // network, not while loading the list.
        let spinning = s.state == .connecting || (s.busy && !s.canToggle)

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
                .foregroundColor(model.failed ? Palette.bad : Palette.muted)
                .multilineTextAlignment(.center)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
                .frame(minHeight: 26)
            if !s.error.isEmpty {
                // Upstream lists the raw cause on its details page; this port has
                // none, so it sits small and quiet under the red verdict.
                Text(s.error)
                    .font(Fonts.small)
                    .foregroundColor(Palette.muted)
                    .multilineTextAlignment(.center)
                    .lineLimit(3)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 6)
                    .textSelection(.enabled)
            }
            // Determinate while connecting; a sweep while loading the list or
            // restoring the network (busy without a fraction), as upstream.
            if let progress = s.progress {
                ProgressBar(value: progress)
                    .padding(.top, 28)
            } else if s.busy {
                ProgressBar(value: -1)
                    .padding(.top, 28)
            }
            if s.state == .connected {
                PingRow(ms: s.pingMS, signal: s.signal)
                    .padding(.top, 26)
                Text(pingCaption(s))
                    .font(Fonts.caption)
                    .foregroundColor(Palette.muted)
                    .lineLimit(1)
                    .padding(.top, 2)
                Button("서버 변경") { model.switchServer() }
                    .buttonStyle(QuietButtonStyle(width: 120, height: 40))
                    .disabled(!s.canSwitch)
                    .padding(.top, 18)
            }
        }
        .padding(.horizontal, 20)
        .frame(width: Metrics.width, height: Metrics.height)
        .animation(.easeOut(duration: 0.3), value: s)
    }

    /// Where the ping goes: the exit, and the relay alone when the game
    /// servers' region does not answer.
    private func pingCaption(_ s: Snapshot) -> String {
        let place = s.server.map { "\($0.label) 서버 경유" } ?? "서버 경유"
        return s.pingSource == "relay" ? "\(place) · 중계 서버까지의 핑" : place
    }
}

/// A small card announcing a finished server change.
private struct ChangeNotice: View {
    var change: ServerChange

    var body: some View {
        VStack(spacing: 2) {
            Text("서버 변경됨")
                .font(Fonts.small.weight(.bold))
                .foregroundColor(Palette.ink)
            Text(change.summary)
                .font(Fonts.small)
                .foregroundColor(Palette.muted)
                .monospacedDigit()
                .lineLimit(1)
                .minimumScaleFactor(0.8)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 9)
        .background(RoundedRectangle(cornerRadius: 10).fill(Palette.cardBg))
        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(Palette.border, lineWidth: 1))
        .shadow(color: .black.opacity(0.08), radius: 6, y: 2)
        .padding(.horizontal, 56)
        .accessibilityElement(children: .combine)
    }
}

/// Signal bars for the verdict and the latency beside them, centred as one unit.
private struct PingRow: View {
    var ms: Int?
    var signal: Int

    var body: some View {
        // Upstream's qualityOf colours by bar count: 3–4 good, 2 fair, 1 bad.
        let color = signal >= 3 ? Palette.good : signal == 2 ? Palette.fair : signal == 1 ? Palette.bad : Palette.muted
        HStack(spacing: 0) {
            SignalBars(lit: max(0, min(4, signal)), color: color)
                .padding(.trailing, 14)
            Text(ms.map { "\($0)" } ?? "—")
                .font(Fonts.ping)
                .foregroundColor(Palette.ink)
                .monospacedDigit()
                // Live readings roll to the new number instead of jumping.
                .contentTransition(.numericText())
            Text("ms")
                .font(Fonts.body)
                .foregroundColor(Palette.muted)
                .padding(.leading, 5)
                .offset(y: 4)
        }
        .frame(height: 44)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(ms.map { "핑 \($0)ms" } ?? "핑 측정 전")
    }
}
