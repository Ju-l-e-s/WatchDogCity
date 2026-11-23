# Consignes de travail — WatchDogCity

- Le projet publie des synthèses neutres de documents municipaux. Préserver le passage par le Validator et le statut `APPROVED` avant toute publication ou newsletter.
- Avant de conclure une modification de code, vérifier le chemin touché et signaler les vérifications impossibles. Mettre à jour le README si l'architecture ou les commandes documentées changent.
- Un push sur `main` déclenche le déploiement via GitHub Actions. N'exécuter ce push, `make deploy`, `make update-data` ou des scripts modifiant des ressources AWS/Brevo réelles que lorsque la demande autorise cette action.
- **Consentement requis avant tout envoi en production :** avant tout envoi, programmation ou appel `sendNow` vers la liste Brevo de production, demander à l'utilisateur une confirmation explicite dans un message distinct et attendre sa réponse affirmative. Ne déclencher aucune action d'envoi avant cette réponse. Une validation du contenu, « c'est bon », « on peut envoyer la lettre », un brouillon ou un envoi test ne vaut pas ce consentement. Sans confirmation, laisser la campagne en brouillon. La liste test #3 est distincte.

## Task routing

Points de départ selon la tâche. Commencer par un contrôle ciblé et élargir selon les dépendances et la portée. Voir les [commandes de vérification](README.md#vérifications-locales).

- **Go** — Partir du module concerné sous `lambdas/` ou `scripts/`. Pour `lambdas/shared`, inclure les consommateurs concernés dans les vérifications.
- **Frontend** — Partir de `frontend/`. Modifier `input.css` pour les styles et régénérer `style.css` avec le build.
- **CDK** — Partir de `cdk/watchdog_stack.py` et `cdk/cities/begles.json`. Les documents historiques de `docs/` peuvent dater.
- **Newsletter** — `lambdas/validator/` pour la QC, `lambdas/shared/newsletter.go` pour le contenu, `lambdas/notifier/` pour Brevo. Voir [le pipeline](ARCHITECTURE.md), [les modes](README.md#tester-la-newsletter-reçue) et [l'aperçu hors ligne](README.md#aperçu-newsletter-hors-ligne). Réserver `go run . article` et `go run . newsletter` dans `scripts/localtest` aux essais réels de Gemini.
