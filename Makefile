BIN := bin/q0
PKG := ./...

.PHONY: build test vet tidy check guard-cohort clean

build:
	go build -o $(BIN) ./cmd/q0

test:
	go test $(PKG)

vet:
	go vet $(PKG)

tidy:
	go mod tidy

# guard-cohort enforces the import discipline: internal/cohort must never
# import a cloud SDK or a scheduler. This is what keeps the eventual
# extraction a `git mv`. CI runs this.
guard-cohort:
	@bash scripts/guard-cohort.sh

check: vet guard-cohort test

clean:
	rm -rf bin/
