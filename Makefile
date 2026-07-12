.PHONY: build linux clean vet desktop desktop-win icons mac-app mac-dmg

VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || echo 0.0.0)
ARCH    := $(shell uname -m)

# ---- headless(纯 Go,零 CGO,与旧版完全一致) ----

build:
	go build -o bin/tongtu ./cmd/tongtu

linux:
	GOOS=linux GOARCH=amd64 go build -o bin/tongtu-linux-amd64 ./cmd/tongtu
	GOOS=linux GOARCH=arm64 go build -o bin/tongtu-linux-arm64 ./cmd/tongtu

vet:
	go vet ./...
	go vet -tags desktop ./...

clean:
	rm -rf bin dist

# ---- 桌面版(系统托盘 + 原生 WebView 窗口) ----

# 本平台桌面构建:macOS/Linux 需 CGO(WKWebView / WebKitGTK)
desktop:
	CGO_ENABLED=1 go build -tags desktop -trimpath -o bin/tongtu ./cmd/tongtu

# Windows 桌面版可从任意平台交叉编译(go-webview2 纯 Go):
# -H windowsgui 免闪控制台,发行包应同时附 console 版 tongtu.exe 供 CLI 使用
desktop-win:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -tags desktop -trimpath \
		-ldflags "-s -w -H windowsgui" -o bin/tongtu-gui.exe ./cmd/tongtu
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w" -o bin/tongtu.exe ./cmd/tongtu

# 从 assets/icon/*.svg 重新生成全部图标产物(icns/ico/png/syso,产物提交进仓库)
icons:
	sh scripts/gen-icons.sh

# 组装 macOS .app(目录名用 ASCII,显示名「通途」走 CFBundleDisplayName)
mac-app: desktop
	rm -rf dist/TongTu.app
	mkdir -p dist/TongTu.app/Contents/MacOS dist/TongTu.app/Contents/Resources
	cp bin/tongtu dist/TongTu.app/Contents/MacOS/tongtu
	sed 's/__VERSION__/$(VERSION)/g' deploy/Info.plist > dist/TongTu.app/Contents/Info.plist
	cp assets/icon/tongtu.icns dist/TongTu.app/Contents/Resources/tongtu.icns
	codesign --force --deep --sign - --timestamp=none dist/TongTu.app
	@echo "dist/TongTu.app 已生成并 ad-hoc 签名(版本 $(VERSION))"

mac-dmg: mac-app
	rm -rf dist/dmgstage && mkdir -p dist/dmgstage
	cp -R dist/TongTu.app dist/dmgstage/
	ln -s /Applications dist/dmgstage/应用程序
	cp assets/icon/tongtu.icns dist/dmgstage/.VolumeIcon.icns
	hdiutil create -volname 通途 -srcfolder dist/dmgstage -ov -format UDRW dist/tongtu.rw.dmg
	mnt=$$(hdiutil attach -nobrowse -readwrite dist/tongtu.rw.dmg | tail -1 | awk '{print $$NF}'); \
		SetFile -a C "$$mnt" || true; hdiutil detach "$$mnt" >/dev/null
	hdiutil convert dist/tongtu.rw.dmg -format UDZO -ov \
		-o dist/tongtu_v$(VERSION)_darwin_$(ARCH).dmg
	rm -f dist/tongtu.rw.dmg && rm -rf dist/dmgstage
