# Backfill 2026

Ce dossier est un module Go autonome pour le script ponctuel `backfill_2026.go`. Il ne fait pas partie du build des Lambdas.

Depuis ce dossier, `go test ./...` vérifie la compilation sans contacter AWS. `go run .` exécute le backfill et modifie DynamoDB et SQS ; il requiert des identifiants AWS ainsi que `COUNCILS_TABLE`, `DELIBERATIONS_TABLE` et `PDF_QUEUE_URL`.
