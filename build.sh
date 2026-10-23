#!/bin/sh
# 32비트 Windows 10(x86)용 배포 폴더(dist/)를 만듭니다. Go 1.26 이상이 필요합니다.
#   ./build.sh                 # 개발용(dev) 빌드: 자동 업데이트를 하지 않습니다
#   VERSION=v1.2.0 ./build.sh  # 배포용 빌드: release.key 로 서명한 dist/latest.json 도 만듭니다
set -e
cd "$(dirname "$0")"
VERSION=${VERSION:-dev}
go test ./internal/...
GOOS=windows GOARCH=386 go vet .
GOOS=windows GOARCH=386 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -H windowsgui -X main.version=$VERSION" -o dist/gcfldrive.exe .
cp install.cmd uninstall.cmd dist/
# 한 파일 설치본: gcfldrive.exe 를 품고 관리자 권한을 요청해 설치한다
cp dist/gcfldrive.exe cmd/setup/payload/gcfldrive.exe
cp rsrc_windows_386.syso cmd/setup/
GOOS=windows GOARCH=386 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/gcfldrive-setup.exe ./cmd/setup
rm -f cmd/setup/payload/gcfldrive.exe cmd/setup/rsrc_windows_386.syso
echo "dist/ 에 gcfldrive-setup.exe(설치 파일), gcfldrive.exe($VERSION), install.cmd, uninstall.cmd 를 만들었어요."
if [ "$VERSION" != dev ]; then
	go run ./cmd/release sign "$VERSION" dist/gcfldrive.exe
fi
echo "WebView2 런타임이 없는 PC용으로 MicrosoftEdgeWebView2RuntimeInstallerX86.exe 를 dist/ 에 함께 넣어 주세요."
