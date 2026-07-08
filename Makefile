.PHONY: ui build-go build clean dev-ui

ui:
	cd ui && npm install && npm run build

build-go:
	go build -o forge.exe ./cmd/forge

build: ui build-go

dev-ui:
	cd ui && npm run dev

clean:
	rm -f forge.exe
	rm -rf internal/scheduler/web/dist
