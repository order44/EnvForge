APP_NAME = envforge
VERSION  ?= 0.3.5
PKG       = ./cmd/envforge

LDFLAGS   = -s -w -X main.Version=$(VERSION)

.PHONY: build build-linux build-windows build-all run list test fmt vet clean tidy

build:
	go build -ldflags="$(LDFLAGS)" -o $(APP_NAME) $(PKG)

build-linux:
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP_NAME)-linux-amd64        $(PKG)
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP_NAME)-linux-arm64        $(PKG)

build-windows:
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/$(APP_NAME)-windows-amd64.exe  $(PKG)

build-all: build-linux build-windows

run: build
	sudo ./$(APP_NAME)

list: build
	./$(APP_NAME) --list

dry-run: build
	sudo ./$(APP_NAME) --dry-run

test:
	go test ./...

fmt:
	gofmt -s -w .

vet:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf $(APP_NAME) dist/
