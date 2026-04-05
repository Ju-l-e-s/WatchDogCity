# Bègles Watchdog - Maintenance Makefile

.PHONY: build deploy test logs-orchestrator logs-worker logs-publisher clean update-data

# --- Déploiement ---
build:
	cd lambdas/orchestrator && GOOS=linux GOARCH=amd64 go build -o orchestrator main.go scraper.go
	cd lambdas/worker && GOOS=linux GOARCH=amd64 go build -o worker main.go gemini.go handler.go
	cd lambdas/publisher && GOOS=linux GOARCH=amd64 go build -o publisher main.go handler.go
	cd lambdas/subscriber && GOOS=linux GOARCH=amd64 go build -o subscriber main.go handler.go

deploy: build
	cd cdk && cdk deploy --require-approval never

# --- Tests & Diagnostics ---
test:
	go test ./lambdas/...

# Voir les logs en temps réel (nécessite AWS CLI)
logs-orchestrator:
	aws logs tail /aws/lambda/WatchdogStack-Orchestrator --follow

logs-worker:
	aws logs tail /aws/lambda/WatchdogStack-Worker --follow

logs-publisher:
	aws logs tail /aws/lambda/WatchdogStack-Publisher --follow

# Forcer une mise à jour manuelle
update-data:
	aws lambda invoke --function-name WatchdogStack-Orchestrator response.json
	@cat response.json

clean:
	rm -f lambdas/orchestrator/orchestrator
	rm -f lambdas/worker/worker
	rm -f lambdas/publisher/publisher
	rm -f lambdas/subscriber/subscriber
	rm -f response.json
