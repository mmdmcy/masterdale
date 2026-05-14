.PHONY: check test vet build smoke clean hygiene

check: test vet build

test:
	go test ./...

vet:
	go vet ./...

build:
	mkdir -p dist
	go build -o dist/dale ./cmd/dale
	go build -o dist/autodale ./cmd/autodale
	go build -o dist/comdale ./cmd/comdale

smoke:
	DALE_DATA_DIR=/tmp/masterdale-smoke-learndale go run ./cmd/dale init
	DALE_DATA_DIR=/tmp/masterdale-smoke-learndale go run ./cmd/dale chat smoke-message-from-masterdale
	DALE_DATA_DIR=/tmp/masterdale-smoke-learndale go run ./cmd/dale context search smoke-message
	DALE_DATA_DIR=/tmp/masterdale-smoke-learndale go run ./cmd/dale npm scan --package react --root .
	AUTODALE_DATA_DIR=/tmp/masterdale-smoke-autodale go run ./cmd/autodale monitor sample
	AUTODALE_DATA_DIR=/tmp/masterdale-smoke-autodale go run ./cmd/autodale monitor daily --kwh-cost 0.40
	AUTODALE_DATA_DIR=/tmp/masterdale-smoke-autodale go run ./cmd/autodale check selfhost
	COMDALE_DATA_DIR=/tmp/masterdale-smoke-comdale go run ./cmd/comdale draft --type post --topic local-agent-mesh

hygiene:
	git status --short
	git status --ignored --short
	git ls-files -z | xargs -0 du -h | sort -h | tail -20
	git grep -n -I -E 'BEGIN (RSA|OPENSSH|EC|DSA)? ?PRIVATE KEY|DALE_TOKEN=[A-Za-z0-9_-]{20,}|ghp_[A-Za-z0-9_]{20,}|sk-[A-Za-z0-9_-]{20,}' -- . ':(exclude).env.example' || true

clean:
	rm -rf dist
