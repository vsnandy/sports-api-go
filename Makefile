.PHONY: test run build deploy smoke tf-init scoreaudit viewer viewer-env

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

viewer:
	@test -f .env || { echo "no .env: run 'make viewer-env' (or copy .env.example)"; exit 1; }
	@! grep -q replace-me .env || { echo ".env still has placeholder values: run 'make viewer-env' or edit it"; exit 1; }
	set -a && . ./.env && set +a && go run ./cmd/viewer

# Writes .env for `make viewer` from Terraform's api_url output and the SSM API key.
viewer-env:
	@test ! -f .env || { echo ".env already exists; delete it to regenerate"; exit 1; }
	@API_URL=$$(terraform -chdir=deploy/terraform output -raw api_url) && \
	 API_KEY=$$(aws ssm get-parameter --name /sports-api/api-key --with-decryption --query Parameter.Value --output text --region $${AWS_REGION:-us-east-1}) && \
	 test -n "$$API_URL" && test -n "$$API_KEY" && \
	 (umask 077 && printf 'API_URL=%s\nAPI_KEY=%s\n' "$$API_URL" "$$API_KEY" > .env) && \
	 echo "wrote .env (API key not shown)" || \
	 { echo "could not read api_url/API key: run 'make tf-init' with TF_STATE_BUCKET and AWS_REGION set, then retry"; exit 1; }
