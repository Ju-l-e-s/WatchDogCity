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
- **IA (Google Gemini)** : le Worker analyse les PDF avec Gemini 2.5 Flash ; l'Aggregator et le Validator utilisent Gemini 2.5 Pro selon la configuration actuelle.
- **Contrôle éditorial** : le Validator vérifie les données avant que le Publisher publie le site et que le Notifier envoie la newsletter.
- **Frontend** : site statique dont les styles sont générés avec Tailwind CSS.
- **Stockage** : DynamoDB pour les données structurées, S3 pour le site statique et les PDF.
- **Sécurité** : Protection Cloudflare Turnstile et WAF pour minimiser les coûts et bloquer les spams.
- **Monitoring** : Dashboard CloudWatch personnalisé pour le suivi des erreurs et des coûts en temps réel.

## 📈 Suivi des Coûts
Le projet intègre un suivi de consommation des jetons (tokens) Gemini directement dans le dashboard AWS, permettant une maîtrise totale du budget de fonctionnement.

## 🔧 Installation & Maintenance
Le projet utilise un `Makefile` pour les opérations courantes. Chaque Lambda et la bibliothèque partagée ont leur propre module Go ; `make test` lance tous leurs tests.

```bash
make test             # Tester les modules Go
make build            # Compiler et empaqueter les 10 Lambdas
cd frontend && npm ci && npm run build  # Construire et vérifier le frontend
```

La CI vérifie chaque pull request vers `main`. Un push sur `main` lance le pipeline de test et de déploiement AWS défini dans [`.github/workflows/deploy.yml`](.github/workflows/deploy.yml). Le caractère bloquant de la vérification des pull requests dépend des règles de protection de branche sur GitHub. La commande `make deploy` déploie aussi vers AWS et nécessite une configuration AWS valide et `ACM_CERTIFICATE_ARN`.

Pour le détail des flux de données et des règles de validation, voir [`ARCHITECTURE.md`](ARCHITECTURE.md). Le [guide de transfert technique](docs/technical_handover.md) explique les choix et le fonctionnement du projet.

Les scripts ponctuels de maintenance se trouvent dans [`scripts/`](scripts/) ; le backfill 2026 possède son propre module Go.

### Tester la newsletter reçue

Pour envoyer **depuis la Lambda AWS**, ouvrir la console AWS en région `eu-west-3`, puis **Lambda → WatchdogStack-NotifierDFD80165-4yUPPY2Vg3Gb → Test**. Créer un événement de test nommé `newsletter-liste-3` avec ce JSON, puis cliquer sur **Test** :

```json
{
  "council_id": "https://www.mairie-begles.fr/d%C3%A9lib%C3%A9rations/deliberation-du-conseil-municipal-du-22-juin-2026/",
  "test_list_id": 3
}
```

Chaque clic avec `test_list_id: 3` crée et envoie une campagne distincte avec le template Brevo configuré pour le Notifier (actuellement `#7`), destinée exclusivement à la liste test `#3`. Le Notifier relit les paramètres enregistrés par le Validator et refuse un conseil qui n'est plus `APPROVED`. Aucun appel Gemini ni modification de `newsletter_sent_at` n'a lieu. Le résultat d'invocation doit être **Succeeded** ; les logs indiquent l'identifiant de campagne Brevo. Cette invocation isolée ne crée pas de brouillon pour la liste de production. La fixture ci-dessous conserve l'instantané du 22 juin pour les essais locaux, mais l'invocation AWS lit les données DynamoDB actuelles.

### Validation manuelle de la newsletter

Pour obtenir **l'envoi à la liste test #3 et le brouillon identique pour la liste de production #2**, invoquer le Notifier avec un événement contenant seulement `council_id` (sans `test_list_id`). Il relit les paramètres sauvegardés par le Validator, crée le brouillon de production, envoie un aperçu via `sendTest` à `BREVO_TEST_EMAIL`, puis crée et envoie une campagne séparée vers la liste #3. Les deux campagnes utilisent le même sujet, les mêmes paramètres et le même template. La liste de production ne reçoit rien tant que son brouillon n'est pas envoyé manuellement.

Le Notifier utilise `BREVO_TEST_EMAIL` pour l'aperçu (ou `ADMIN_EMAIL` si `BREVO_TEST_EMAIL` est absent). Renseigner le secret GitHub Actions `BREVO_TEST_EMAIL` avec l'adresse du destinataire avant le prochain déploiement ; sans adresse valide, la Lambda refuse de créer le brouillon de production. `AUTO_SEND_ENABLED` vaut `false` par défaut dans le code et dans CDK. Pour rétablir l'envoi automatique, définir la variable GitHub Actions `AUTO_SEND_ENABLED` à `true` puis déployer : le Notifier appelle `sendNow` sur la campagne de production après l'aperçu et marque alors `newsletter_sent_at`. Un événement portant `scheduled_at` conserve la planification Brevo au lieu de l'envoi immédiat lorsque l'envoi automatique est activé.

En mode manuel, DynamoDB conserve `newsletter_campaign_id`, l'identifiant du **brouillon de production**, mais ne renseigne pas `newsletter_sent_at` avant confirmation de l'envoi. Le webhook Brevo ci-dessous effectue cette mise à jour automatiquement dès qu'il reçoit un événement de livraison de cette campagne. En secours, après avoir envoyé le brouillon dans Brevo, invoquer le Notifier avec `{"council_id":"<ID_DU_CONSEIL>","reconcile_only":true}` : il lit le statut de cette campagne dans Brevo et écrit sa date `sentDate` dans `newsletter_sent_at` seulement si le statut vaut `sent`. Une nouvelle invocation normale du Notifier effectue aussi cette vérification avant tout nouvel envoi.

### Webhook Brevo pour l'envoi manuel

Définir un secret GitHub Actions `WEBHOOK_SECRET` aléatoire avant d'activer le webhook (par exemple, généré avec `openssl rand -hex 32`). Sans ce secret, le déploiement de la newsletter reste possible, mais CDK ne crée pas le webhook ni son URL ; `newsletter_sent_at` ne sera donc pas mis à jour automatiquement après un envoi manuel. Avec ce secret, CDK publie l'URL de base dans la sortie `BrevoWebhookUrl` ; le jeton n'y figure pas. Dans Brevo, créer un webhook de type **marketing**, canal **email**, événement **delivered**, sans envoi groupé, avec l'URL `BrevoWebhookUrl?token=<WEBHOOK_SECRET>`. Le handler accepte uniquement les requêtes POST portant ce jeton.

Brevo n'émet pas d'événement `campaign_sent` pour une campagne e-mail marketing : `delivered` est un événement par destinataire. Son champ `camp_id` désigne la campagne, tandis que `id` désigne la livraison du webhook. Le handler retrouve le conseil `APPROVED` grâce à l'index DynamoDB `newsletter_campaign_id-index`, vérifie dans l'API Brevo que cette campagne vise **la liste de production** et que son statut indique un envoi, puis renseigne `newsletter_sent_at` avec `sentDate`, `ts_sent` ou l'heure courante. Les répétitions du webhook ne changent pas la date déjà enregistrée. En cas d'erreur transitoire ou si l'API Brevo indique encore `queued`, le handler répond `429` pour solliciter une nouvelle tentative. Si aucune livraison n'a lieu ou si le webhook n'est pas configuré, utiliser l'invocation `reconcile_only` ci-dessus.

Le mode test a été chargé directement sur la Lambda le 29 septembre 2026. Il faut intégrer les changements de `lambdas/notifier` au prochain déploiement CDK depuis `main` pour éviter qu'un ancien binaire ne remplace ce mode.

La commande `newsletter-test` lit en lecture seule les paramètres enregistrés par le Validator pour un conseil `APPROVED`, ou la fixture `scripts/localtest/fixtures/approved_council_2026-06-22.json` capturée depuis ce conseil pour répéter les essais. Sans `--send`, elle affiche le sujet, la source municipale et tous les paramètres à relire. Avec `--send`, elle crée une nouvelle campagne Brevo et l'envoie **uniquement à la liste test #3**. `--template-file` utilise un fichier HTML local sans modifier le template Brevo de production ; sans cette option, la commande utilise le template actif (identifiant 7 par défaut). Elle ne modifie pas le statut d'envoi public dans DynamoDB. La fixture est un instantané : vérifier son contenu et sa date avant un nouvel essai. Le contenu de l'email doit être comparé aux PDF sources pour contrôler chiffres, votes, contexte et neutralité.

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
