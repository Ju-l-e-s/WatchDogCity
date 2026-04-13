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
	# Aggregator
	cd lambdas/aggregator && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go
	cd lambdas/aggregator && zip -j ../../dist/aggregator.zip bootstrap && rm bootstrap
	# Publisher
	cd lambdas/publisher && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/publisher && zip -j ../../dist/publisher.zip bootstrap && rm bootstrap
	# SubscribeFunction
	cd lambdas/subscriber && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/subscriber && zip -j ../../dist/subscriber.zip bootstrap && rm bootstrap
	# ContactFunction
	cd lambdas/contact && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/contact && zip -j ../../dist/contact.zip bootstrap && rm bootstrap
	# Confirmer
	cd lambdas/confirmer && go mod tidy && GOOS=linux GOARCH=arm64 go build -o bootstrap main.go handler.go
	cd lambdas/confirmer && zip -j ../../dist/confirmer.zip bootstrap && rm bootstrap

deploy: build
	cd cdk && cdk deploy --require-approval never

# --- Tests & Diagnostics ---
test:
	@for dir in lambdas/*/ ; do \
		if [ -f "$${dir}go.mod" ]; then \
			echo "Testing $$dir..." ; \
			(cd $$dir && go test ./... || exit 1) ; \
		fi \
	done

logs-orchestrator:
	aws logs tail /aws/lambda/WatchdogStack-Orchestrator --follow

logs-worker:
	aws logs tail /aws/lambda/WatchdogStack-Worker --follow

logs-publisher:
	aws logs tail /aws/lambda/WatchdogStack-Publisher --follow

update-data:
	aws lambda invoke --function-name $$(aws lambda list-functions --query "Functions[?contains(FunctionName, 'Orchestrator')].FunctionName" --output text | head -n 1) response.json
	@cat response.json

clean:
	rm -rf dist
	rm -f response.json
