.PHONY: test run build deploy smoke tf-init scoreaudit

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
	$(if $(TF_STATE_BUCKET),,$(error TF_STATE_BUCKET is required))
	$(if $(AWS_REGION),,$(error AWS_REGION is required))
	terraform -chdir=deploy/terraform init -backend-config=bucket=$(TF_STATE_BUCKET) -backend-config=region=$(AWS_REGION)

deploy: build
	terraform -chdir=deploy/terraform apply

smoke:
	./scripts/smoke.sh

scoreaudit:
	go run ./cmd/scoreaudit -season 2026 -weeks 1-3 $(ARGS)
