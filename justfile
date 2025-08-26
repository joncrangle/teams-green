default:
    @just --list

build:
    go build -ldflags="-s -w" -o dist/teams-green.exe .

run: build
    ./dist/teams-green.exe start

run-ws: build
    ./dist/teams-green.exe start --websocket

debug: build
    ./dist/teams-green.exe start --websocket --debug

clean:
    rm -rf dist/
    go clean

fmt:
    go fmt ./...

lint:
    golangci-lint run

tidy:
    go mod tidy

update-deps:
    go get -u ./...
    go mod tidy

tag version:
    git tag -a v{{version}} -m "Release v{{version}}"
    git push origin v{{version}}
