# 🔭 WatchDog City - Bègles

**L'intelligence artificielle au service de la transparence citoyenne.**

WatchDog City, publié sous le nom *L'Observatoire de Bègles*, est une plateforme indépendante qui transforme les documents municipaux de Bègles en synthèses claires, neutres et accessibles à tous.

## 🚀 Mission
La politique locale produit des dizaines de pages de rapports techniques (PDF) souvent difficiles à suivre pour les citoyens. WatchDog City utilise les modèles Gemini configurés dans [`cdk/cities/begles.json`](cdk/cities/begles.json) pour extraire l'essentiel :
- **Décisions concrètes** : Ce qui a été réellement acté.
- **Impacts locaux** : Ce que cela change pour les habitants.
- **Résultats de votes** : La position de la majorité et de l'opposition.
- **Analyse structurée** : Contexte, enjeux et points de controverse.

## 🛠️ Architecture Technique
Le projet repose sur une infrastructure **100% Serverless** sur AWS, gérée via **AWS CDK** :

- **Backend (Go)** : 10 fonctions Lambda : Orchestrator, Worker, Aggregator, Validator, Publisher, Notifier, BrevoCampaignWebhook, Subscriber, Confirmer et Contact.
- **IA (Google Gemini)** : les modèles d’extraction PDF, de vérification PDF, de rédaction de la newsletter et de vérification de la newsletter sont configurables par étape. Les [valeurs par défaut dans le code](lambdas/shared/gemini_response.go) et la [configuration CDK](cdk/watchdog_stack.py) décrivent leur sélection.
- **Contrôle éditorial** : le Worker confronte chaque synthèse au PDF original avant stockage ; le Validator vérifie la complétude des PDF et les affirmations de la newsletter avant que le Publisher publie le site et que le Notifier prépare l'envoi. Le Publisher revérifie les nouveaux conseils approuvés ; les archives déjà approuvées nécessitent un audit et un backfill distincts.
- **Faits essentiels**. Les nouvelles analyses conservent des faits critiques avec l’empreinte du PDF et des références par page. Le Worker construit la note budgétaire à partir des montants et stades structurés et peut lancer une vérification ciblée avant stockage. La fiche et la newsletter appliquent des niveaux de couverture adaptés à leur format.
- **Données du site** : `data.json` en production est écrit uniquement par le Publisher. Le déploiement CDK met à jour les autres fichiers statiques et vérifie ensuite que `data.json` existe toujours dans S3.

Les essais réels du fact-checking sont volontaires et consomment des appels Gemini. Après connexion AWS au profil `watchdog-admin`, `python3 scripts/localtest/run_worker_factcheck.py` vérifie un PDF officiel, `--recent-council-test` relance localement l'extraction et le fact-check des trois derniers PDF de Bègles sans écrire dans AWS, `--newsletter-stage-test` vérifie le stade des décisions et `--newsletter-fixture chemin/data.json` génère une newsletter locale contrôlée sans envoi. La clé est lue depuis la configuration de la Lambda Worker et n'est pas affichée.
- **Frontend** : site statique dont les styles sont générés avec Tailwind CSS.
- **Stockage** : DynamoDB pour les données structurées, S3 pour le site statique et les PDF.
- **Sécurité** : Protection Cloudflare Turnstile et WAF pour minimiser les coûts et bloquer les spams.
- **Monitoring** : Dashboard CloudWatch personnalisé pour le suivi des erreurs et des coûts en temps réel.

Une fiche comptée déclenche l’agrégateur via DynamoDB Streams. Lorsque toute la séance est comptée, le Validator décide de son approbation. Une suspension documentaire conserve les fiches et bloque la publication de la séance. Une reprise technique après approbation utilise les paramètres de newsletter enregistrés.

## 📈 Suivi des Coûts
Le projet intègre un suivi de consommation des jetons (tokens) Gemini directement dans le dashboard AWS, permettant une maîtrise totale du budget de fonctionnement.

## 🔧 Installation & Maintenance
### Vérifications locales

| Périmètre | Commande |
|---|---|
| Une Lambda ou `lambdas/shared` | `go test -run TestNom ./...` pour cibler un test, puis `go test ./...` selon la portée, depuis le module concerné. Si `shared` change, repérer ses consommateurs depuis la racine avec `rg -l 'replace github.com/watchdog/shared' lambdas/*/go.mod scripts/*/go.mod`. |
| Outils locaux | `(cd scripts/localtest && go test ./...)` ou `(cd scripts/backfill && go test ./...)`. Pour le backfill, ce contrôle vérifie la compilation. |
| Tous les modules sous `lambdas/` | `make test` depuis la racine, avec le détecteur de courses. |
| Compilation et empaquetage des 10 Lambdas | `make build` depuis la racine. |
| Frontend | `npm run build` depuis `frontend/` après installation des dépendances avec `npm ci`. `npm run verify` vérifie les artefacts déjà construits. |
| Infrastructure CDK | Depuis `cdk/`, `CDK_OUTDIR=cdk.out .venv/bin/python app.py` produit le modèle CloudFormation localement. Préparer l'environnement Python avec `cdk/requirements.txt`, les ZIP Lambda (`make build` à la racine) et les artefacts frontend. Pour une synthèse locale, renseigner `ACM_CERTIFICATE_ARN` avec un ARN de test au format ACM en `us-east-1`. |

La CI vérifie chaque pull request vers `main`. Un push sur `main` lance le pipeline de test et de déploiement AWS défini dans [`.github/workflows/deploy.yml`](.github/workflows/deploy.yml). Le caractère bloquant de la vérification des pull requests dépend des règles de protection de branche sur GitHub. La commande `make deploy` déploie aussi vers AWS et nécessite une configuration AWS valide et `ACM_CERTIFICATE_ARN`.

Pour le détail des flux de données et des règles de validation, voir [`ARCHITECTURE.md`](ARCHITECTURE.md). Le [guide de transfert technique](docs/technical_handover.md) explique les choix et le fonctionnement du projet.

Les scripts ponctuels de maintenance se trouvent dans [`scripts/`](scripts/) ; le backfill 2026 possède son propre module Go.

### Pilote sur une séance en mode brouillon

Après autorisation de déployer et d’exécuter le pilote, utiliser une séance municipale encore inédite, visible sur la liste officielle. Attendre la fin des invocations de l’ancienne version et vérifier que les nouvelles versions du Worker, de l’Aggregator, du Validator, de l’Orchestrator et du Notifier sont actives. Choisir une fenêtre sans déclenchement planifié concurrent et sans messages en cours ou en attente d’une précédente séance.

Définir `ORCHESTRATOR` avec le nom physique de la Lambda déployée et `COUNCIL_URL` avec l’URL exacte de la séance dans la liste municipale. L’appel suivant lance le traitement réel à partir de SQS et peut entraîner la publication de la séance sur le site après approbation.

```sh
aws lambda invoke --profile watchdog-admin --region eu-west-3 \
  --function-name "${ORCHESTRATOR:?}" --cli-binary-format raw-in-base64-out \
  --payload "$(jq -cn --arg url "${COUNCIL_URL:?}" '{target_url:$url,draft_only:true}')" \
  pilote-reponse.json
```

Vérifier l’absence de `FunctionError` dans la réponse d’invocation. L’Orchestrator inscrit `newsletter_draft_only=true` avant la mise en file des PDF de cette seule séance. Le Notifier applique ce mode à chaque livraison, y compris si le Validator est rejoué.

Le pilote réussit si le manifeste correspond aux PDF comptés, si le conseil est `APPROVED`, si ses fiches figurent dans le `data.json` publié et si `newsletter_campaign_id` désigne un brouillon Brevo avec les paramètres stockés par le Validator. Vérifier dans Brevo que la campagne n’est ni programmée ni envoyée et qu’aucune campagne de test n’a été créée pour la séance. En cas de suspension, constater l’absence de publication et de campagne. Une tentative interrompue dont le verdict n’a pu être enregistré reste dans `pdf_reviews_pending`. Après épuisement des reprises SQS, conserver le message en DLQ et examiner le journal du Worker sans lever le blocage.

### Tester la newsletter reçue

| Mode | Objectif | Opérations | Services externes | Brouillon Brevo | Envoi d'e-mails | Préconditions et autorisations |
|---|---|---|---|---|---|---|
| [Aperçu hors ligne](#aperçu-newsletter-hors-ligne) | Relire les paramètres d'un instantané. | `newsletter-test --fixture` sans `--send` lit le JSON local et affiche le sujet, la source et les paramètres. | Aucun. | Aucun. | Aucun. | Fixture locale d'un conseil municipal marqué `APPROVED`, avec identifiant et sujet. Aucun accès AWS ou Brevo requis. |
| `refresh_newsletter: true` | Régénérer le contenu validé. | Le Validator relance les contrôles qualité sur les délibérations stockées et remplace `newsletter_params_json` si les contrôles passent et que la condition d'écriture tient. | DynamoDB en lecture et écriture, Gemini pour la génération et la vérification. | Aucun changement dans Brevo. | Aucun. | `council_id` d'un conseil municipal `APPROVED`. À l'écriture, le statut doit rester `APPROVED` et `newsletter_sent_at` doit être absent. Autorisation d'invoquer le Validator et de mettre à jour les données réelles. |
| `draft_only: true` | Préparer la campagne de production pour relecture. | Le Notifier crée ou actualise le brouillon et enregistre son identifiant en DynamoDB. Si Brevo indique un envoi déjà effectué pour la campagne enregistrée, il réconcilie sa date. | DynamoDB et Brevo. Le contenu provient des paramètres stockés par le Validator. | Brouillon de production créé ou mis à jour si la campagne est modifiable. Une campagne déjà envoyée, en cours d'envoi ou programmée reste telle quelle. | Aucun aperçu, envoi ou programmation. | `council_id` d'un conseil `APPROVED`, catégorie `Conseil municipal` ou absente, et `fact_check_version` ≥ 1. Incompatible avec `test_list_id`, `reconcile_only` et `scheduled_at`. Autorisation d'invoquer le Notifier et de modifier les ressources réelles. |
| `test_list_id: 3` | Recevoir le rendu Brevo sur la liste de test. | Le Notifier lit les paramètres stockés et crée une campagne distincte par invocation. Il ne modifie pas le suivi d'envoi en DynamoDB. | DynamoDB en lecture, Brevo pour la campagne et son envoi. | La campagne de test est créée avant son envoi. Aucun brouillon de production. | Envoi réel à la liste test #3 uniquement. | Événement avec `council_id` et `test_list_id: 3`. Conseil municipal `APPROVED` avec paramètres stockés valides et `fact_check_version` ≥ 1. Autorisation d'invoquer le Notifier et d'envoyer à la liste de test. |

Les trois modes Lambda requièrent le droit `lambda:InvokeFunction` sur la fonction concernée. Tout envoi ultérieur du brouillon de production reste soumis à la [confirmation distincte](#validation-manuelle-de-la-newsletter). Le détail des contrôles qualité et du pipeline figure dans [ARCHITECTURE.md](ARCHITECTURE.md).

Pour envoyer **depuis la Lambda AWS**, ouvrir la console AWS en région `eu-west-3`, puis **Lambda → WatchdogStack-NotifierDFD80165-4yUPPY2Vg3Gb → Test**. Créer un événement de test nommé `newsletter-liste-3` avec ce JSON, puis cliquer sur **Test** :

```json
{
  "council_id": "https://www.mairie-begles.fr/d%C3%A9lib%C3%A9rations/deliberation-du-conseil-municipal-du-22-juin-2026/",
  "test_list_id": 3
}
```

L'envoi utilise le template Brevo configuré pour le Notifier (actuellement `#7`). Le résultat d'invocation doit être **Succeeded** et les logs indiquent l'identifiant de campagne Brevo.

### Validation manuelle de la newsletter

Pour tout envoi ou toute programmation vers la liste Brevo de production #2, demander une confirmation explicite dans un message distinct et attendre la réponse affirmative, y compris pour un appel `sendNow`. La validation du contenu, un brouillon et un envoi de test vers la liste #3 sont des étapes indépendantes. En l'absence de confirmation, garder la campagne en brouillon.

Un événement Notifier contenant seulement `council_id` prépare le brouillon de production #2 et une campagne distincte pour la liste test #3. Si `BREVO_TEST_EMAIL` est configuré, le Notifier envoie aussi un aperçu individuel. Pour préparer le brouillon seul, utiliser `draft_only: true`. Le Notifier refuse les demandes de programmation.

La commande d’administration `release` prépare une demande d’autorisation liée à la campagne et à son contenu. Après confirmation explicite dans un message distinct, la même commande peut envoyer ou programmer cette campagne. L’opérateur utilise une identité AWS dédiée et une clé d’administration séparée. Voir le [mode opératoire et les tests de migration](docs/GEMINI_MIGRATION_BREVO_RELEASE.md).

DynamoDB conserve `newsletter_campaign_id` dès la préparation. Le webhook ou une invocation `{"council_id":"<ID_DU_CONSEIL>","reconcile_only":true}` renseigne `newsletter_sent_at` lorsque Brevo confirme le statut `sent` et fournit une date valide.

### Webhook Brevo pour l'envoi manuel

Définir un secret GitHub Actions `WEBHOOK_SECRET` aléatoire avant d'activer le webhook (par exemple, généré avec `openssl rand -hex 32`). Sans ce secret, le déploiement de la newsletter reste possible, mais CDK ne crée pas le webhook ni son URL ; `newsletter_sent_at` ne sera donc pas mis à jour automatiquement après un envoi manuel. Avec ce secret, CDK publie l'URL de base dans la sortie `BrevoWebhookUrl` ; le jeton n'y figure pas. Dans Brevo, créer un webhook de type **marketing**, canal **email**, événement **delivered**, sans envoi groupé, avec l'URL `BrevoWebhookUrl?token=<WEBHOOK_SECRET>`. Le handler accepte uniquement les requêtes POST portant ce jeton.

Brevo n'émet pas d'événement `campaign_sent` pour une campagne e-mail marketing : `delivered` est un événement par destinataire. Son champ `camp_id` désigne la campagne, tandis que `id` désigne la livraison du webhook. Le handler retrouve le conseil `APPROVED` grâce à l'index DynamoDB `newsletter_campaign_id-index`, vérifie dans l'API Brevo que cette campagne vise **la liste de production** et que son statut indique un envoi, puis renseigne `newsletter_sent_at` avec `sentDate` si le statut vaut `sent`. Les répétitions du webhook ne changent pas la date déjà enregistrée. En cas d'erreur transitoire ou si l'API Brevo indique encore `queued` ou `in_process`, le handler répond `429` pour solliciter une nouvelle tentative. Si aucune livraison n'a lieu ou si le webhook n'est pas configuré, utiliser l'invocation `reconcile_only` ci-dessus.

### Aperçu newsletter hors ligne

Depuis la racine, afficher le sujet, la source municipale et les paramètres validés d'un instantané du 22 juin 2026, sans accès AWS, Brevo ou Gemini.

```bash
(cd scripts/localtest && go run . newsletter-test --fixture fixtures/approved_council_2026-06-22.json)
```

`newsletter-test` accepte aussi `--council-id` pour lire les paramètres enregistrés par le Validator pour un conseil actuellement `APPROVED`. Avec `--send`, la commande crée et envoie une campagne Brevo **uniquement à la liste test #3**. `--template-file` utilise un fichier HTML local sans modifier le template Brevo de production ; sans cette option, la commande utilise le template actif (identifiant 7 par défaut). Elle ne modifie pas le statut d'envoi public dans DynamoDB. Avant un essai, vérifier la date et le contenu de la fixture et comparer le contenu de l'email aux PDF sources pour contrôler chiffres, votes, contexte et neutralité.

```bash
cd scripts/localtest
AWS_PROFILE=watchdog-admin AWS_REGION=eu-west-3 go run . newsletter-test --council-id '<ID_DU_CONSEIL>'
BREVO_API_KEY='<CLE_LOCALE>' go run . newsletter-test --fixture fixtures/approved_council_2026-06-22.json --template-file ../../docs/email-templates/brevo_template_v2.html --send --confirm-list-id 3
BREVO_API_KEY='<CLE_LOCALE>' go run . newsletter-test --fixture fixtures/approved_council_2026-06-22.json --template-file ../../docs/email-templates/brevo_template_v3.html --send --confirm-list-id 3
```

La lecture par `--council-id` exige les droits `dynamodb:GetItem` sur `watchdog-councils` ; `--fixture` fonctionne hors ligne jusqu'à l'envoi. `BREVO_NEWSLETTER_TEMPLATE_ID` et `SENDER_EMAIL` peuvent être définis si le déploiement utilise des valeurs différentes. L'essai sans `--send` n'appelle pas Brevo ; chaque commande avec `--send` crée et envoie une vraie campagne à la liste #3. Si Brevo retourne une erreur après la création, vérifier le statut de la campagne indiquée avant de relancer la commande.

## 👷 Auteur
**Béglais de naissance**, développeur et Cloud Architect passionné par l'émancipation citoyenne par la technologie.

---
*Ce projet est une initiative citoyenne bénévole, non-affiliée à la mairie de Bègles.*
