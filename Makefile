.PHONY: test run build deploy smoke tf-init

test:
	go test ./...

run:
	go run ./cmd/api

build:
	mkdir -p dist
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -tags lambda.norpc -trimpath -buildvcs=false -ldflags="-s -w" -o dist/bootstrap ./cmd/api
	touch -t 198001010000 dist/bootstrap
	cd dist && rm -f bootstrap.zip && zip -qX bootstrap.zip bootstrap

tf-init:
	terraform -chdir=deploy/terraform init -backend-config=bucket=$(TF_STATE_BUCKET) -backend-config=region=$(AWS_REGION)

deploy: build
	terraform -chdir=deploy/terraform apply

smoke:
	./scripts/smoke.sh
