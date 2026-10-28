# OpenCW Mac 버전

메이플스토리 클래식월드 **Mac 런처**의 게임 통신만 해외 중계 서버로 우회해 지연을 줄여주는 앱입니다. 다른 앱의 통신에는 영향이 없습니다.

> 원작: **kimd0**님의 [OpenCW](https://github.com/kimd0/OpenCW) (Windows) · [원 게시글](https://gall.dcinside.com/mgallery/board/view/?id=mapleclassic&no=40380)
> 이 저장소는 제가 Mac에서 쓰려고 옮긴 비공식 포트이며, 원작자와는 관계가 없습니다.

## 설치

1. [Releases](../../releases/latest)에서 `.pkg`를 내려받아 더블클릭합니다.
2. 차단되면 **시스템 설정 → 개인정보 보호 및 보안 → 그래도 열기**를 누릅니다. (Apple 유료 서명이 없어서 뜨는 경고입니다.)
3. 설치할 때 Mac 암호를 한 번 입력합니다. 필요 조건: macOS 13 이상, `/Applications`에 공식 `MapleStory Launcher`

## 사용

1. **OpenCW**를 열면 자동으로 연결됩니다.
2. `연결됨`이 뜬 **뒤에** MapleStory Launcher를 실행합니다.
3. 창을 닫아도 메뉴 막대(🍁)에서 연결이 유지됩니다. 끄려면 🍁 → **OpenCW 종료**.

핑이 높으면 **서버 변경**을 누르세요. 핑은 중계 서버를 거쳐 게임 서버가 있는 미국 서부까지 4초마다 잰 값입니다.

## 주의

- 넥슨과 무관한 비공식 도구입니다. 게임사 정책에 따라 계정 제재 위험이 있을 수 있습니다.
- 중계 서버는 운영자를 알 수 없는 공개 서버입니다. 넥슨 **2단계 인증**을 켜고, 암호를 다른 곳과 같이 쓰지 마세요.
- 별도 보증 없이 있는 그대로 제공됩니다.

## 문제 해결

- **지역 오류로 접속 안 됨** → 런처를 Cmd+Q로 완전히 끄고, OpenCW가 `연결됨`인 상태에서 다시 실행하세요.
- **도우미에 연결할 수 없음** → `.pkg`를 다시 설치하세요.
- 인터넷이 이상하면 Mac을 재시작하면 원래대로 돌아옵니다.

## 삭제

```sh
sudo /Applications/OpenCW.app/Contents/Resources/uninstall.sh
```

<details>
<summary>파일 검증 · 직접 빌드 · 동작 방식</summary>

**파일 검증** — 배포 파일은 GitHub Actions가 태그된 소스로 직접 빌드하며 출처 증명이 붙습니다.

```sh
gh attestation verify OpenCW-<버전>-macos.pkg --repo 0xMoomi/OpenCW-mac
```

**직접 빌드** — Go와 Xcode Command Line Tools가 필요합니다. 결과물은 `dist/`에 생깁니다.

```sh
./package.sh 1.1.0
```

**동작 방식**

- Mac 런처는 CrossOver(Wine) 위에서 돌아가므로, `/Applications/MapleStory Launcher.app`이 쓰는 통신을 중계합니다(80·853번 포트 제외).
- 화면 앱(SwiftUI)과 관리자 권한 도우미(LaunchDaemon)로 나뉘며, 도우미는 설치된 OpenCW 앱의 요청만 받습니다. 앱을 종료하면 연결이 해제됩니다.
- 모든 파일은 관리자 소유로 설치되어 일반 계정으로는 수정할 수 없습니다.

</details>

## 라이선스

[GPL-3.0-or-later](LICENSE) · 원작 [kimd0/OpenCW](https://github.com/kimd0/OpenCW) · [오픈소스 고지](THIRD_PARTY_NOTICES.md)
