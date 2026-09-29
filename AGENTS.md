# Consignes de travail — WatchDogCity

- Le projet publie des synthèses neutres de documents municipaux. Préserver le passage par le Validator et le statut `APPROVED` avant toute publication ou newsletter.
- Consulter `ARCHITECTURE.md` pour le pipeline de données et la QC Gateway. Pour l'infrastructure déployée, vérifier `cdk/watchdog_stack.py` et `cdk/cities/begles.json` ; les documents historiques de `docs/` ne décrivent pas forcément l'état actuel.
- Les Lambdas Go, `lambdas/shared`, `scripts/backfill` et `scripts/localtest` sont des modules Go distincts. Pour un changement ciblé, lancer `go test ./...` depuis les modules concernés ; `make test` vérifie uniquement les modules Lambda et partagé.
- Pour le frontend, modifier `frontend/input.css` plutôt que `frontend/style.css` généré, puis vérifier avec `npm run build` depuis `frontend/`.
- Avant de conclure une modification de code, vérifier le chemin touché et signaler les vérifications impossibles. Mettre à jour le README si l'architecture ou les commandes documentées changent.
- Les commandes `go run . article` et `go run . newsletter` de `scripts/localtest` appellent Gemini ; les réserver aux tâches qui demandent un essai réel du modèle.
- Un push sur `main` déclenche le déploiement via GitHub Actions. N'exécuter ce push, `make deploy`, `make update-data` ou des scripts modifiant des ressources AWS/Brevo réelles que lorsque la demande autorise cette action.
