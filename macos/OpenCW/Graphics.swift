import SwiftUI

// Design tokens from upstream theme_windows.go. Warm neutrals carry the
// layout; maple orange is the one brand colour.
enum Palette {
    static let canvasBg = Color(hex: 0xF6F4F0)
    static let cardBg = Color(hex: 0xFFFFFF)
    static let border = Color(hex: 0xE0DBD2)
    static let divider = Color(hex: 0xECE8E1)
    static let ink = Color(hex: 0x1C1915)
    static let muted = Color(hex: 0x5F584F)
    static let brand = Color(hex: 0xE2621B)
    static let brandHover = Color(hex: 0xD15715)
    static let trackOff = Color(hex: 0xDBD5CB)
    static let trackHover = Color(hex: 0xCFC8BC)
    static let barTrack = Color(hex: 0xE7E2DA)
    static let good = Color(hex: 0x13804A)
    static let fair = Color(hex: 0x9A6200)
    static let bad = Color(hex: 0xB3261E)
    static let iconHover = Color(hex: 0xECE8E1)
    static let iconDown = Color(hex: 0xE2DDD4)
}

// Upstream lays out in 96-DPI pixels; one pixel there is one point here.
enum Metrics {
    static let width: CGFloat = 420
    static let height: CGFloat = 520
}

enum Fonts {
    static let headline = Font.system(size: 28, weight: .bold)
    static let body = Font.system(size: 15)
    static let small = Font.system(size: 13)
    static let caption = Font.system(size: 12)
    static let ping = Font.system(size: 30, weight: .bold)
    static let button = Font.system(size: 15, weight: .bold)
    static let rowKey = Font.system(size: 14)
    static let rowValue = Font.system(size: 14, weight: .bold)
    static let pageHead = Font.system(size: 20, weight: .bold)
}

extension Color {
    init(hex: UInt32) {
        self.init(.sRGB,
                  red: Double((hex >> 16) & 0xFF) / 255,
                  green: Double((hex >> 8) & 0xFF) / 255,
                  blue: Double(hex & 0xFF) / 255)
    }
}

// MARK: - Maple leaf

/// The comic-style leaf from gfx_windows.go: five fat lobes around a round
/// core in a 100×100 box, each a closed cardinal spline, plus a stubby stem.
struct Leaf: View {
    var color: Color
    var skew: CGFloat = 0.1

    private struct Lobe { let angle, length, width: CGFloat }
    private static let lobes = [
        Lobe(angle: 0, length: 50, width: 30), Lobe(angle: -70, length: 46, width: 28),
        Lobe(angle: 70, length: 46, width: 28), Lobe(angle: -135, length: 32, width: 22),
        Lobe(angle: 135, length: 32, width: 22),
    ]
    private static let center = CGPoint(x: 50, y: 52)
    private static let profile: [CGPoint] = [
        .init(x: -0.36, y: 0.1), .init(x: -0.5, y: -0.45), .init(x: -0.2, y: -0.84), .init(x: 0, y: -1),
        .init(x: 0.2, y: -0.84), .init(x: 0.5, y: -0.45), .init(x: 0.36, y: 0.1),
    ]
    private static let core: CGFloat = 12
    private static let tension: CGFloat = 0.45

    var body: some View {
        Canvas { ctx, size in
            let s = min(size.width, size.height)
            let ox = size.width / 2, oy = size.height / 2
            func at(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
                let px = (x - 50) / 100 * s, py = (y - 50) / 100 * s
                return CGPoint(x: ox + px - py * skew, y: oy + py)
            }
            let c = Self.center
            for lobe in Self.lobes {
                let a = lobe.angle * .pi / 180
                let points = Self.profile.map { q -> CGPoint in
                    let x = q.x * lobe.width, y = q.y * lobe.length
                    return at(c.x + x * cos(a) - y * sin(a), c.y + x * sin(a) + y * cos(a))
                }
                ctx.fill(closedCurve(points, tension: Self.tension), with: .color(color))
            }
            let corePoints = (0..<12).map { i -> CGPoint in
                let a = CGFloat(i) / 12 * 2 * .pi
                return at(c.x + Self.core * cos(a), c.y + Self.core * sin(a))
            }
            ctx.fill(closedCurve(corePoints, tension: 0.5), with: .color(color))
            var stem = Path()
            stem.move(to: at(c.x, c.y + Self.core * 0.6))
            stem.addLine(to: at(c.x + 1, 78))
            stem.addLine(to: at(c.x + 3, 90))
            ctx.stroke(stem, with: .color(color),
                       style: StrokeStyle(lineWidth: s * 0.07, lineCap: .round, lineJoin: .round))
        }
    }
}

/// A closed cardinal spline through points, matching GDI+ AddClosedCurve2:
/// each Bézier control point sits 0.3 × tension of the neighbour chord away.
func closedCurve(_ p: [CGPoint], tension: CGFloat) -> Path {
    var path = Path()
    let n = p.count
    guard n >= 3 else { return path }
    let k = 0.3 * tension
    path.move(to: p[0])
    for i in 0..<n {
        let p0 = p[(i - 1 + n) % n], p1 = p[i], p2 = p[(i + 1) % n], p3 = p[(i + 2) % n]
        let c1 = CGPoint(x: p1.x + k * (p2.x - p0.x), y: p1.y + k * (p2.y - p0.y))
        let c2 = CGPoint(x: p2.x - k * (p3.x - p1.x), y: p2.y - k * (p3.y - p1.y))
        path.addCurve(to: p2, control1: c1, control2: c2)
    }
    path.closeSubpath()
    return path
}

// MARK: - Toggle

/// The main control: the switch is both the action and the state. The knob
/// glides right when on; busy shows a spinner inside the knob.
struct MapleToggle: View {
    var on: Bool
    var busy: Bool
    var enabled: Bool
    var action: () -> Void

    @State private var hover = false

    var body: some View {
        Button(action: action) { EmptyView() }
            .buttonStyle(ToggleStyle(on: on, busy: busy, enabled: enabled, hover: hover))
            .frame(width: 144, height: 76)
            .onHover { hover = $0 }
            .disabled(!enabled)
    }

    private struct ToggleStyle: ButtonStyle {
        let on, busy, enabled, hover: Bool

        func makeBody(configuration: Configuration) -> some View {
            let w: CGFloat = 144, h: CGFloat = 76
            var d = h - 12
            if configuration.isPressed && enabled { d -= 2 }
            var track = on ? Palette.brand : Palette.trackOff
            if hover && enabled { track = on ? Palette.brandHover : Palette.trackHover }
            return ZStack(alignment: .leading) {
                Capsule().fill(track)
                if !enabled { Capsule().fill(Palette.canvasBg.opacity(0.45)) }
                ZStack {
                    Circle().fill(Color.black.opacity(0.10))
                        .frame(width: d + 1, height: d + 1)
                        .offset(y: 1.5)
                    Circle().fill(Color.white).frame(width: d, height: d)
                    if busy {
                        Spinner(diameter: d * 0.44)
                    } else {
                        Leaf(color: on ? Palette.brand : Palette.trackOff)
                            .frame(width: d * 0.5, height: d * 0.5)
                            .offset(y: 1)
                    }
                }
                .frame(width: d + 1, height: d + 1)
                .offset(x: 6 + (on ? w - 12 - d : 0) - 0.5)
            }
            .frame(width: w, height: h)
            .contentShape(Capsule())
            .animation(.easeOut(duration: 0.28), value: on)
            .animation(.easeOut(duration: 0.12), value: track)
        }
    }
}

/// A 260° arc turning once a second.
struct Spinner: View {
    var diameter: CGFloat

    var body: some View {
        TimelineView(.animation) { timeline in
            let t = timeline.date.timeIntervalSinceReferenceDate
            Circle()
                .trim(from: 0, to: 260.0 / 360)
                .stroke(Palette.brand, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                .frame(width: diameter, height: diameter)
                .rotationEffect(.degrees(t.truncatingRemainder(dividingBy: 1) * 360))
        }
    }
}

// MARK: - Small pieces

/// Four rising bars with the first `lit` ones in color.
struct SignalBars: View {
    var lit: Int
    var color: Color

    var body: some View {
        Canvas { ctx, size in
            let count = 4, gap = size.width * 0.12
            let w = (size.width - gap * CGFloat(count - 1)) / CGFloat(count)
            for i in 0..<count {
                let h = size.height * (0.34 + 0.22 * CGFloat(i))
                let r = CGRect(x: CGFloat(i) * (w + gap), y: size.height - h, width: w, height: h)
                ctx.fill(Path(roundedRect: r, cornerRadius: 1.5), with: .color(i < lit ? color : Palette.barTrack))
            }
        }
        .frame(width: 30, height: 26)
    }
}

/// A thin brand bar: determinate for 0...1, an indeterminate sweep below 0.
struct ProgressBar: View {
    var value: Double

    var body: some View {
        let w: CGFloat = 240, h: CGFloat = 6
        ZStack(alignment: .leading) {
            Capsule().fill(Palette.barTrack)
            if value >= 0 {
                Capsule().fill(Palette.brand)
                    .frame(width: max(h, w * min(1, CGFloat(value))))
                    .animation(.easeOut(duration: 0.6), value: value)
            } else {
                TimelineView(.animation) { timeline in
                    let t = timeline.date.timeIntervalSinceReferenceDate
                    let span = w * 0.3
                    let x = -span + CGFloat((t * 0.7).truncatingRemainder(dividingBy: 1)) * (w + span)
                    let left = max(0, x), right = min(w, x + span)
                    Capsule().fill(Palette.brand)
                        .frame(width: max(0, right - left))
                        .offset(x: left)
                }
            }
        }
        .frame(width: w, height: h)
        .clipShape(Capsule())
    }
}

/// The quiet secondary action: white face, hairline border.
struct QuietButtonStyle: ButtonStyle {
    var width: CGFloat
    var height: CGFloat

    func makeBody(configuration: Configuration) -> some View {
        Face(configuration: configuration, width: width, height: height)
    }

    private struct Face: View {
        let configuration: Configuration
        let width, height: CGFloat
        @Environment(\.isEnabled) private var enabled
        @State private var hover = false

        var body: some View {
            var face = Palette.cardBg, edge = Palette.border, fg = Palette.ink
            if !enabled {
                face = Palette.canvasBg
                fg = Palette.muted
            } else if configuration.isPressed {
                face = Color(hex: 0xEFECE6)
            } else if hover {
                face = Color(hex: 0xF8F6F2)
                edge = Color(hex: 0xC9C2B6)
            }
            return configuration.label
                .font(Fonts.button)
                .foregroundColor(fg)
                .frame(width: width, height: height)
                .background(RoundedRectangle(cornerRadius: 8).fill(face))
                .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(edge, lineWidth: 1))
                .contentShape(RoundedRectangle(cornerRadius: 8))
                .onHover { hover = $0 }
        }
    }
}

/// A round, label-free control for secondary navigation (ⓘ and ×).
struct IconButton: View {
    enum Glyph { case info, close }
    var glyph: Glyph
    var label: String
    var action: () -> Void

    var body: some View {
        Button(action: action) { EmptyView() }
            .buttonStyle(Style(glyph: glyph))
            .frame(width: 36, height: 36)
            .accessibilityLabel(label)
    }

    private struct Style: ButtonStyle {
        let glyph: Glyph

        func makeBody(configuration: Configuration) -> some View {
            Face(glyph: glyph, pressed: configuration.isPressed)
        }
    }

    private struct Face: View {
        let glyph: Glyph
        let pressed: Bool
        @State private var hover = false

        var body: some View {
            let c = pressed || hover ? Palette.ink : Palette.muted
            ZStack {
                if pressed {
                    Circle().fill(Palette.iconDown)
                } else if hover {
                    Circle().fill(Palette.iconHover)
                }
                Canvas { ctx, size in
                    let cx = size.width / 2, cy = size.height / 2
                    let round = StrokeStyle(lineWidth: 1.8, lineCap: .round)
                    switch glyph {
                    case .info:
                        ctx.stroke(Path(ellipseIn: CGRect(x: cx - 9.5, y: cy - 9.5, width: 19, height: 19)),
                                   with: .color(c), lineWidth: 1.6)
                        ctx.fill(Path(ellipseIn: CGRect(x: cx - 1.35, y: cy - 4.2 - 1.35, width: 2.7, height: 2.7)),
                                 with: .color(c))
                        var bar = Path()
                        bar.move(to: CGPoint(x: cx, y: cy - 1))
                        bar.addLine(to: CGPoint(x: cx, y: cy + 4.6))
                        ctx.stroke(bar, with: .color(c), style: StrokeStyle(lineWidth: 1.9, lineCap: .round))
                    case .close:
                        var x = Path()
                        x.move(to: CGPoint(x: cx - 5.5, y: cy - 5.5))
                        x.addLine(to: CGPoint(x: cx + 5.5, y: cy + 5.5))
                        x.move(to: CGPoint(x: cx + 5.5, y: cy - 5.5))
                        x.addLine(to: CGPoint(x: cx - 5.5, y: cy + 5.5))
                        ctx.stroke(x, with: .color(c), style: round)
                    }
                }
            }
            .frame(width: 36, height: 36)
            .contentShape(Circle())
            .onHover { hover = $0 }
        }
    }
}
