# Bègles Watchdog - Maintenance Makefile

.PHONY: build deploy test logs-orchestrator logs-worker logs-publisher clean update-data

# --- Déploiement ---
build:
	mkdir -p dist
	# Orchestrator
	cd lambdas/orchestrator && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go scraper.go
	cd lambdas/orchestrator && zip -j ../../dist/orchestrator.zip bootstrap && rm bootstrap
	# Worker
	cd lambdas/worker && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go gemini.go handler.go
	cd lambdas/worker && zip -j ../../dist/worker.zip bootstrap && rm bootstrap
	# Publisher
	cd lambdas/publisher && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/publisher && zip -j ../../dist/publisher.zip bootstrap && rm bootstrap
	# SubscribeFunction
	cd lambdas/subscriber && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/subscriber && zip -j ../../dist/subscriber.zip bootstrap && rm bootstrap
	# ConfirmFunction
	cd lambdas/confirm && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/confirm && zip -j ../../dist/confirm.zip bootstrap && rm bootstrap
	# ContactFunction
	cd lambdas/contact && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/contact && zip -j ../../dist/contact.zip bootstrap && rm bootstrap

deploy: build
	cd cdk && cdk deploy --require-approval never

# --- Tests & Diagnostics ---
test:
	go test ./lambdas/...

logs-orchestrator:
	aws logs tail /aws/lambda/WatchdogStack-Orchestrator --follow

logs-worker:
	aws logs tail /aws/lambda/WatchdogStack-Worker --follow

logs-publisher:
	aws logs tail /aws/lambda/WatchdogStack-Publisher --follow

update-data:
	aws lambda invoke --function-name WatchdogStack-Orchestrator response.json
	@cat response.json

clean:
	rm -rf dist
	rm -f response.json
