# OpenCW

메이플스토리 클래식월드의 트래픽을 해외 공개 중계 서버로 우회하여 네트워크 지연을 줄여주는 프로그램입니다.
게임 외 다른 프로그램의 통신에는 영향을 주지 않습니다.

## 설치 및 실행

[Releases](../../releases/latest)에서 최신 버전의 zip 파일을 다운로드하여 압축을 풀고, `OpenCW.exe`를 실행하세요.

자세한 사용 방법과 면책 조항은 [README.txt](assets/README.txt)에서 확인할 수 있습니다.
> 현재 Windows 64비트만을 지원하며, 실행 시 관리자 권한이 요구됩니다.

## 무결성 검증

배포 파일은 태그된 소스 코드를 기반으로 GitHub Actions에서 직접 빌드되며, 빌드 출처 증명(attestation)이 함께 제공됩니다.

~~~powershell
gh attestation verify OpenCW.exe --repo kimd0/OpenCW
~~~

동일한 태그(예: `v1.0.0`)의 소스를 내려받아 `./build.ps1 -Version 1.0.0` 명령어로 직접 빌드 시 `SHA256SUMS.txt`에 명시된 해시와 완전히 일치하는 `OpenCW.exe`를 얻을 수 있습니다.

## 직접 빌드하기

OpenCW 빌드에는 [Go](https://go.dev/dl/)가 요구됩니다.
컴파일러 버전은 `go.mod`에 명시되어 있으며, 빌드 시 자동으로 적용됩니다.

~~~powershell
./build.ps1 -Version 1.0.0
~~~

빌드된 결과물은 `dist/` 디렉터리에 생성됩니다.

## 라이선스

이 프로젝트는 [GPL-3.0-or-later](LICENSE) 라이선스를 따릅니다.
사용된 오픈소스 목록 및 고지 사항은 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)를 참고하세요.