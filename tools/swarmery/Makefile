.PHONY: build build-web build-go copy-docs dev test clean

# Repo docs snapshotted into the go:embed content dir for /api/docs.
DOCS_SRC := ../../docs
DOCS_DST := internal/docsfs/content

# build: docs snapshot + vite bundle → go:embed → single binary ./swarmery
build: copy-docs build-web build-go

build-web:
	cd web && npm install && npm run build

build-go:
	go build -o swarmery ./cmd/swarmery

# copy-docs: snapshot the repo docs into internal/docsfs/content for go:embed.
# The copies are gitignored (.gitkeep keeps the embed pattern valid without
# them, e.g. in CI — /api/docs then serves an empty list).
copy-docs:
	cp $(DOCS_SRC)/ONBOARDING.md $(DOCS_SRC)/EXTENDING.md $(DOCS_SRC)/NEUTRALITY.md $(DOCS_DST)/

# dev: run the API daemon and the vite dev server (proxying /api to :7777)
dev: copy-docs
	go run ./cmd/swarmery serve & \
	cd web && npm run dev; \
	kill %1 2>/dev/null

test:
	go vet ./...
	go test ./...

clean:
	rm -f swarmery
	find web/dist -mindepth 1 ! -name .gitkeep -delete
	find $(DOCS_DST) -mindepth 1 ! -name .gitkeep -delete
