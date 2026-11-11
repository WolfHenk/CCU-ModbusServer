# -----------------------------------------------------------------------------
# Projekt:      CCU-ModbusServer
# Datei:        Makefile
# Ersteller:    Wolfram Henkel
# Datum:        11. Nov. 2026
# Version:      0.1.0
#
# Copyright (c) 2026 Wolfram Henkel.
#
# Lizenz:       CCU-ModbusServer-Lizenz für kostenfreie Nutzung und
#               Weitergabe, Version 1.1.
#
# Private und gewerbliche Nutzung sind erlaubt.
# Die Software und Änderungen dürfen nur kostenlos unter
# derselben Lizenz und mit vollständigem Quellcode weitergegeben werden.
#
# Vollständiger Lizenztext: LICENSE
# -----------------------------------------------------------------------------

GO ?= go
VERSION := $(shell cat VERSION)
BIN_FLAGS := -trimpath -ldflags "-X main.version=$(VERSION)"
SOURCE_FILES := go.mod Makefile VERSION LICENSE

.PHONY: test build source package

test:
	gofmt -l . | (! grep .)
	$(GO) vet ./...
	CGO_ENABLED=1 $(GO) test -race -count=1 ./...
	sh -n packaging/update_script packaging/rc.d/ccu-modbusserver

build:
	mkdir -p build/bin/linux-amd64 build/bin/linux-arm64 build/bin/linux-arm
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build $(BIN_FLAGS) -o build/bin/linux-amd64/ccu-modbusserverd ./cmd/ccu-modbusserverd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build $(BIN_FLAGS) -o build/bin/linux-arm64/ccu-modbusserverd ./cmd/ccu-modbusserverd
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=7 $(GO) build $(BIN_FLAGS) -o build/bin/linux-arm/ccu-modbusserverd ./cmd/ccu-modbusserverd

source:
	rm -rf build/source
	mkdir -p build/source
	cp -a cmd internal packaging tools build/source/
	cp -a $(SOURCE_FILES) build/source/
	cd build/source && tar -czf ../CCU-ModbusServer-$(VERSION)-SOURCE.tar.gz .

package: test build source
	rm -rf build/pkg
	mkdir -p build/pkg/bin build/pkg/rc.d build/pkg/www/img build/pkg/lib build/pkg/examples
	cp -a build/bin/linux-* build/pkg/bin/
	cp packaging/bin/update_addon packaging/bin/firewall build/pkg/bin/
	cp packaging/update_script build/pkg/
	cp packaging/rc.d/ccu-modbusserver build/pkg/rc.d/
	cp packaging/www/*.cgi packaging/www/style.css build/pkg/www/
	cp packaging/www/img/modbus-logo*.png build/pkg/www/img/
	cp packaging/lib/session.tcl build/pkg/lib/
	cp packaging/examples/config.json build/pkg/examples/
	cp VERSION LICENSE build/pkg/
	cp -a build/source build/pkg/source
	chmod 755 build/pkg/update_script build/pkg/bin/update_addon build/pkg/bin/firewall build/pkg/www/*.cgi build/pkg/rc.d/ccu-modbusserver
	cd build/pkg && tar --owner=0 --group=0 -czf ../CCU-ModbusServer-$(VERSION)-INSTALL.tar.gz .
	cd build && sha256sum CCU-ModbusServer-$(VERSION)-INSTALL.tar.gz > CCU-ModbusServer-$(VERSION)-INSTALL.tar.gz.sha256
