.PHONY: test run build deploy smoke

test:
	go test ./...

run:
	go run ./cmd/api

build:
	mkdir -p dist
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -ldflags="-s -w" -o dist/bootstrap ./cmd/api
	cd dist && rm -f bootstrap.zip && zip -q bootstrap.zip bootstrap

deploy: build
	terraform -chdir=deploy/terraform apply

smoke:
	./scripts/smoke.sh
