import SwiftUI

/// The ⓘ page, laid out like upstream's details page: a heading, a card of
/// rows, then the notes and disclaimer from upstream's README.txt.
struct InfoView: View {
    private static let version =
        Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "—"

    private static let notes = """
    이 프로그램은 개인이 만든 비공식 도구입니다. 넥슨 및 메이플스토리와 관련이 없으며, 게임사의 승인이나 보증을 받지 않았습니다. 원본 OpenCW의 Mac 이식판이며 원본 개발자의 공식 배포판이 아닙니다.

    중계 서버는 인터넷에 공개된 제3자 서버입니다. 연결 속도나 안정성을 항상 보장할 수는 없으며, 화면의 핑은 참고용 측정값으로 실제 게임 서버의 핑과 다를 수 있습니다.

    게임 접속에 필요한 통신만 중계하며, 다른 프로그램의 통신은 평소처럼 직접 연결됩니다. 공개 서버의 운영 주체는 확인할 수 없다는 점을 고려해주세요. 네트워크 경로를 바꾸는 프로그램에 대한 게임사의 정책은 변경될 수 있습니다.

    이 프로그램은 별도의 보증 없이 있는 그대로 제공됩니다. 사용 여부와 방식은 사용자가 판단하며, 사용으로 생긴 결과에 대해 개발자는 법적 책임을 지지 않습니다.

    GPL 라이선스로 배포되는 구성 요소(sing-box 등)가 포함되어 있습니다.
    """

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("정보")
                .font(Fonts.pageHead)
                .foregroundColor(Palette.ink)
                .frame(height: 32)
                .padding(.leading, 4)
                .padding(.top, 18)
                .padding(.bottom, 18)
            VStack(spacing: 0) {
                InfoRow(key: "버전", value: Self.version)
                Divider().overlay(Palette.divider).padding(.horizontal, 18)
                InfoRow(key: "Mac 버전", value: "0xMoomi", link: "https://github.com/0xMoomi/OpenCW-mac")
                Divider().overlay(Palette.divider).padding(.horizontal, 18)
                InfoRow(key: "원본 OpenCW", value: "kimd0", link: "https://github.com/kimd0/OpenCW")
                Divider().overlay(Palette.divider).padding(.horizontal, 18)
                InfoRow(key: "라이선스", value: "GPL-3.0-or-later")
            }
            .background(RoundedRectangle(cornerRadius: 12).fill(Palette.cardBg))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(Palette.border, lineWidth: 1))
            ScrollView {
                Text(Self.notes)
                    .font(Fonts.small)
                    .foregroundColor(Palette.muted)
                    .lineSpacing(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .padding(.horizontal, 4)
                    .padding(.vertical, 14)
            }
        }
        .padding(.horizontal, 24)
        .frame(width: Metrics.width, height: Metrics.height, alignment: .top)
    }
}

private struct InfoRow: View {
    var key: String
    var value: String
    var link: String?

    var body: some View {
        HStack {
            Text(key)
                .font(Fonts.rowKey)
                .foregroundColor(Palette.muted)
            Spacer(minLength: 12)
            if let link, let url = URL(string: link) {
                Link(destination: url) {
                    HStack(spacing: 3) {
                        Text(value).font(Fonts.rowValue)
                        Image(systemName: "arrow.up.right").font(.system(size: 10, weight: .bold))
                    }
                    .foregroundColor(Palette.ink)
                }
                .help(link)
                .accessibilityHint(link)
            } else {
                Text(value)
                    .font(Fonts.rowValue)
                    .foregroundColor(Palette.ink)
                    .textSelection(.enabled)
            }
        }
        .padding(.horizontal, 18)
        .frame(height: 40)
    }
}
