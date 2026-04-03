.PHONY: build test test-race bench fuzz-exif fuzz-iptc fuzz-xmp lint tidy coverage ci testdata

build:
	go build ./...

test:
	go test -count=1 ./...

test-race:
	go test -race -count=1 ./...

bench:
	go test -bench=. -benchmem -count=5 ./...

fuzz-exif:
	go test -fuzz=FuzzParseEXIF -fuzztime=60s ./exif/...

fuzz-iptc:
	go test -fuzz=FuzzParseIPTC -fuzztime=60s ./iptc/...

fuzz-xmp:
	go test -fuzz=FuzzParseXMP -fuzztime=60s ./xmp/...

lint:
	golangci-lint run ./...

tidy:
	go mod tidy

coverage:
	go test -coverprofile=cover.out ./...
	go tool cover -html=cover.out

ci: lint test-race bench

testdata:
	bash testdata/download.sh
