.PHONY: build build-web build-go dev test clean

# build: vite bundle → go:embed → single binary ./swarmery
build: build-web build-go

build-web:
	cd web && npm install && npm run build

build-go:
	go build -o swarmery ./cmd/swarmery

# dev: run the API daemon and the vite dev server (proxying /api to :7777)
dev:
	go run ./cmd/swarmery serve & \
	cd web && npm run dev; \
	kill %1 2>/dev/null

test:
	go vet ./...
	go test ./...

clean:
	rm -f swarmery
	find web/dist -mindepth 1 ! -name .gitkeep -delete
