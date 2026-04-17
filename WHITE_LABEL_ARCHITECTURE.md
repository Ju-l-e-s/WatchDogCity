# White-Label Architecture — Watchdog Municipal Newsletter

---

## 1. Cartographie de l'Existant (As-Is)

### Vue d'ensemble

Pipeline entièrement serverless sur AWS, géré par AWS CDK (Python). Neuf fonctions Lambda Go (ARM64) orchestrent le cycle de vie complet : scraping → analyse IA → publication web → newsletter.

```
EventBridge (cron lun-ven 18h Paris)
    └─► Orchestrator Lambda
            ├─ Scrape mairie-begles.fr (liste des conseils + PDFs)
            ├─ Écrit dans DynamoDB (watchdog-councils)
            └─ Envoie les URLs PDF dans SQS (watchdog-pdf-queue)
                    └─► Worker Lambda (déclenché par SQS)
                            ├─ Télécharge chaque PDF
                            ├─ Analyse via Gemini (gemini-2.5-flash)
                            └─ Écrit dans DynamoDB (watchdog-deliberations)
                                    └─► Aggregator Lambda (déclenché par DynamoDB Streams)
                                            ├─ Attend que tous les PDFs soient traités
                                            ├─ Synthèse IA via Gemini (gemini-2.5-pro)
                                            └─► Publisher Lambda
                                                    ├─ Génère data.json → S3 → CloudFront invalidation
                                                    └─► Notifier Lambda (async)
                                                            ├─ Génère newsletter via Gemini (gemini-2.5-flash)
                                                            └─ Crée et envoie campagne Brevo

API Gateway
    ├─ POST /subscribe  → Subscriber Lambda → Brevo (email confirmation)
    ├─ GET  /confirm    → Confirmer Lambda  → Brevo (ajout contact liste)
    └─ POST /contact    → Contact Lambda   → Brevo (email vers admin)

Frontend statique : S3 + CloudFront + domaine custom (ACM)
```

### Stack technique
| Couche | Technologie |
|---|---|
| Infrastructure | AWS CDK (Python) |
| Compute | AWS Lambda, Go 1.22, ARM64 |
| Base de données | DynamoDB (3 tables) |
| Queue | SQS + DLQ |
| Storage | S3 (site statique) |
| CDN | CloudFront |
| Email | Brevo (transactionnel + campagnes) |
| IA | Google Gemini API (v1 / v1beta) |
| DNS/TLS | Route53 + ACM (externe au CDK) |

---

## 2. Audit de Couplage — Dette Spécifique à Bègles

### 2.1 URLs de scraping (couplage fort, bloquant)

**Fichier :** `lambdas/orchestrator/main.go`, lignes 21-22

```go
const (
    deliberationsListURL = "https://www.mairie-begles.fr/d%C3%A9lib%C3%A9rations/"
    nextCouncilURL       = "https://www.mairie-begles.fr/vie-municipale/le-conseil-municipal-2/les-seances-du-conseil-municipal/"
)
```

Ces deux constantes sont hardcodées. Pour chaque nouvelle ville, elles sont différentes.

### 2.2 Sélecteurs CSS du scraper (couplage fort, bloquant)

**Fichier :** `lambdas/orchestrator/scraper.go`

Le scraper est écrit pour le CMS spécifique de la mairie de Bègles. Sélecteurs non portables :
- `li.list__item` / `a.publications-list-item__title-link` — liste des délibérations
- `.publications-list-item__excerpt` / `.publications-list-item__text` — résumé
- `span.theme` — catégorie
- `.telecharger-item` / `.telecharger-item__link` — liens PDF
- `.infowidget .rte ul li strong` — date du prochain conseil

Les catégories normalisées sont hardcodées pour Bègles :

```go
func normalizeCategory(cat string) string {
    if strings.Contains(cat, "ccas") || strings.Contains(cat, "centre communal") {
        return "CCAS"
    }
    if strings.Contains(cat, "estey") {  // Quartier de Bègles
        return "Estey"
    }
    return "Conseil municipal"
}
```

### 2.3 Prompts Gemini (couplage moyen)

**Fichier :** `lambdas/worker/gemini.go`, lignes 20-23

```go
const deliberationPrompt = `...
Analyse ce document PDF de délibération du conseil municipal de Bègles.
...
impacts : conséquences directes pour les Béglaises et Béglais
```

**Fichier :** `lambdas/notifier/handler.go`, lignes 381-382, 407, 419

```go
sb.WriteString("Tu es rédacteur de newsletter municipale pour L'Observatoire de Bègles.\n")
// ...
"pour les Béglaises et les Béglais"
// ...
"website_url": "https://lobservatoiredebegles.fr",
```

### 2.4 Sender Brevo (couplage fort)

**Fichier :** `lambdas/notifier/handler.go`, ligne 579

```go
"sender": map[string]string{
    "name":  "L'Observatoire de Bègles",  // hardcodé
    "email": d.senderEmail,
},
```

### 2.5 Email de contact (couplage fort)

**Fichier :** `lambdas/contact/handler.go`, lignes 66, 77

```go
subject := fmt.Sprintf("[Observatoire Bègles] Message de %s", cr.Name)
// ...
"depuis l'Observatoire de Bègles"
```

### 2.6 CDK — valeurs par défaut spécifiques à Bègles

**Fichier :** `cdk/watchdog_stack.py`

| Variable CDK | Valeur par défaut hardcodée |
|---|---|
| `domain_name` | `"lobservatoiredebegles.fr"` (ligne 73) |
| `historical_bucket_name` | `"watchdogstack-websitebucket75c24d94-clsmaf2ocvxq"` (ligne 65) — S3 existant |
| `FROM_EMAIL` (publisher) | `"watchdog@begles.citoyen"` (ligne 166) |
| `site_url` | `"https://www.lobservatoiredebegles.fr"` (ligne 209) |
| `sender_email` | `"newsletter@lobservatoiredebegles.fr"` (ligne 210) |
| `contact_sender` | `"contact@lobservatoiredebegles.fr"` (ligne 211) |
| `ses_identity_arns` | `identity/lobservatoiredebegles.fr` (lignes 213-215) |
| `dashboard_name` | `"Watchdog-Begles-Health"` (ligne 362) |

### 2.7 Noms des ressources AWS (couplage faible — naming convention)

| Ressource | Nom actuel |
|---|---|
| DynamoDB councils | `watchdog-councils` |
| DynamoDB deliberations | `watchdog-deliberations` |
| DynamoDB subscribers | `watchdog-subscribers` |
| SQS queue | `watchdog-pdf-queue` |
| SQS DLQ | `watchdog-pdf-dlq` |
| CDK Stack | `WatchdogStack` |

En mono-compte multi-tenant, ces noms entrent en collision. En multi-stack isolé, ils doivent être préfixés par ville.

### 2.8 Schedule EventBridge (couplage contextuel)

```python
events.Schedule.cron(week_day="MON-FRI", hour="16", minute="0")  # 18h Paris
```

Le cron est calé sur le calendrier des séances de Bègles. Une autre commune peut publier ses délibérations à d'autres moments, ou moins fréquemment.

### 2.9 Bucket S3 référencé par nom physique (couplage opérationnel)

Le bucket S3 existant est importé par son nom physique plutôt que créé par CDK. Ce pattern ne peut pas être répliqué pour une nouvelle ville sans connaitre son nom de bucket au préalable.

---

## 3. Architecture Cible (To-Be) — Recommandation

### Option A : Multi-tenant logique (un compte AWS, partitionnement par `city_id`)

**Principe :** Toutes les villes partagent les mêmes tables DynamoDB, les mêmes queues SQS et les mêmes Lambdas. Un champ `city_id` partitionne les données.

| Avantage | Risque |
|---|---|
| Coût inférieur à grande échelle (100+ villes) | Blast radius élevé : un bug affecte toutes les villes |
| Une seule infrastructure à opérer | Isolation RGPD compliquée (données mixées) |
| Déploiement instantané d'une nouvelle ville | Tables DynamoDB pouvant devenir des hot partitions |
| | Complexité opérationnelle : filtrage systématique par `city_id` dans toutes les requêtes |
| | Brevo : une seule liste commune → impossible de segmenter par ville |
| | Impossible de facturer/limiter une ville indépendamment |

### Option B : Multi-stack isolé (un CDK stack complet par ville) ✅ Recommandée

**Principe :** Un déploiement CDK complet et indépendant par ville, piloté par un fichier de configuration `.env.{city}`. Les ressources AWS sont préfixées (`watchdog-{city}-*`).

| Avantage | Risque |
|---|---|
| Isolation totale : incident d'une ville sans impact sur les autres | Coût fixe par stack (Lambda idle est gratuit, DynamoDB PAY_PER_REQUEST aussi — risque marginal) |
| RGPD simple : données d'une ville = compte/région dédié possible | N stacks à surveiller en CloudWatch |
| Billing par ville immédiat | Déploiement d'un fix doit être répercuté sur tous les stacks → CI/CD matrix obligatoire |
| Une ville peut avoir son propre calendrier, ses propres Brevo lists | |
| Onboarding d'une ville = ajouter un fichier `.env.city` + `make deploy CITY=xxx` | |
| Configuration Brevo entièrement indépendante par ville | |

**Verdict :** L'option B est recommandée pour la phase 0 à 10 villes. Le coût AWS reste négligeable (DynamoDB PAY_PER_REQUEST, Lambda facturation à l'exécution). L'isolation et la simplicité opérationnelle l'emportent largement. À 50+ villes, réévaluer vers un plan de contrôle centralisé avec data planes isolés (Option B mais avec Terraform ou CDK pipelines).

---

## 4. Plan de Transformation — Roadmap Technique

### Étape 1 — Configuration as Code par ville (CDK)

**Objectif :** Éliminer toutes les valeurs hardcodées dans `watchdog_stack.py`. Chaque ville est décrite par un fichier JSON unique qui sert de contrat d'instanciation.

**Format retenu : JSON validé par schéma** (plutôt que des fichiers `.env` dispersés).

> Fichiers créés (draft, sans impact prod) :
> - `cdk/city.schema.json` — schéma JSON Schema Draft-07, contrat formel de toutes les clés requises
> - `cdk/cities/begles.json` — configuration complète de Bègles, conforme au schéma

**Pourquoi JSON + schéma plutôt que `.env` :**
- Validation automatique avant tout `cdk deploy` (structure, types, patterns de nommage)
- Hiérarchie lisible (scraping.selectors, brevo.list_id, etc.) vs clés plates en majuscules
- Diffable proprement dans git lors de l'onboarding d'une nouvelle ville
- Un seul fichier par ville vs plusieurs `.env` à synchroniser entre dev/CI/prod

**Gestion des secrets : SSM Parameter Store** (pas de valeurs sensibles dans le JSON).
Toutes les clés API (`brevo_api_key`, `gemini_api_key`), l'email admin et le certificat ACM sont référencés par leur **chemin SSM** (`/watchdog/{city_id}/brevo_api_key`). Le CDK résout ces valeurs au moment du deploy via `ssm.StringParameter.value_from_lookup()`. Aucun secret ne transite par git ou la CI.

Convention de nommage SSM imposée par le schéma :
```
/watchdog/{city.id}/brevo_api_key       → SecureString
/watchdog/{city.id}/gemini_api_key      → SecureString
/watchdog/{city.id}/admin_email         → String
/watchdog/{city.id}/acm_certificate_arn → String
```

**Structure cible du repo :**
```
cdk/
  city.schema.json          ← contrat (ne pas modifier sans migration)
  cities/
    begles.json             ← Bègles (existant, validé)
    bordeaux.json           ← future ville
  watchdog_stack_v2.py      ← stack paramétré (à créer, ne pas toucher watchdog_stack.py)
  app_v2.py                 ← entry point multi-ville (à créer)
```

Modifier `app_v2.py` pour charger et valider la config depuis `CITY` env var :
```python
city = os.environ.get("CITY")
config = load_and_validate_city_config(f"cities/{city}.json", "city.schema.json")
WatchdogStack(app, config["aws"]["stack_name"], city_config=config)
```

Modifier `watchdog_stack_v2.py` pour :
- Accepter `city_config` (dict) comme paramètre unique de configuration
- Préfixer toutes les ressources nommées avec `config["aws"]["resource_prefix"]`
- Importer le bucket S3 uniquement si `config["aws"]["existing_s3_bucket"]` est présent
- Résoudre les secrets SSM via `ssm.StringParameter.value_from_lookup()`

**Résultat :** `make deploy CITY=bordeaux` déploie un stack complet et étanche pour Bordeaux, sans toucher au code ni à la config de Bègles.

---

### Étape 2 — Abstraction du Scraper (interface Go par CMS)

**Objectif :** Rendre l'orchestrator agnostique du CMS de la mairie cible.

**Approche retenue : interface Go `CityScraper` avec implémentation par type de CMS** (plutôt que sélecteurs CSS en env vars).

Injecter des dizaines de sélecteurs CSS en variables d'environnement est fragile : pas de validation de cohérence, impossible de tester unitairement, et inutile si deux villes partagent le même CMS. La bonne abstraction est au niveau Go.

**Interface à créer dans `lambdas/orchestrator/scraper.go` :**

```go
type CityScraper interface {
    ScrapeCouncilList() ([]CouncilListing, error)
    ScrapePDFLinks(councilURL string) ([]PDFItem, error)
    ScrapeNextCouncilDate() (string, error)
}
```

**Implémentations à créer (un fichier par CMS) :**

```
lambdas/orchestrator/
  scraper.go                  ← interface CityScraper + NewScraper factory
  scraper_openmairie_v2.go    ← implémentation actuelle (Bègles) ← refactoring du code existant
  scraper_openmairie_v1.go    ← future (si besoin)
  scraper_wordpress.go        ← future
  scraper_custom.go           ← sélecteurs 100% depuis config JSON
```

**Factory dans `main.go` :**

```go
scraperType := os.Getenv("SCRAPER_TYPE") // "openmairie_v2", "wordpress", "custom"
listURL     := os.Getenv("SCRAPING_LIST_URL")
nextURL     := os.Getenv("SCRAPING_NEXT_COUNCIL_URL")
configJSON  := os.Getenv("SCRAPING_CONFIG_JSON") // sélecteurs + category_mapping sérialisés

scraper := NewScraper(scraperType, listURL, nextURL, configJSON)
```

Le CDK sérialise le bloc `scraping` du fichier `cities/{city}.json` et l'injecte comme variable d'environnement `SCRAPING_CONFIG_JSON`. Un seul point d'injection, validé par le schéma JSON en amont.

**`normalizeCategory` :** externaliser vers la map `category_mapping` du JSON de config (déjà présente dans `begles.json`). Le scraper la désérialise depuis `SCRAPING_CONFIG_JSON`.

**Avantage :** toute la logique de scraping pour un CMS est testable unitairement avec un fichier HTML fixture, indépendamment du CDK et de AWS.

---

### Étape 3 — Paramétisation des Prompts Gemini

**Objectif :** Supprimer toutes les références géographiques hardcodées dans les prompts.

**Worker (`gemini.go`)** : Remplacer les références dans `deliberationPrompt` par des placeholders injectés via env :

```go
cityName := os.Getenv("CITY_NAME") // "Bègles", "Bordeaux", etc.
citizenLabel := os.Getenv("CITY_CITIZEN_LABEL") // "les Béglaises et Béglais", "les Bordelais", etc.

deliberationPrompt = fmt.Sprintf(`...
Analyse ce document PDF de délibération du conseil municipal de %s.
...
impacts pour %s
...`, cityName, citizenLabel)
```

**Notifier (`handler.go`)** : Même principe pour le prompt newsletter :

```go
cityLabel := os.Getenv("CITY_LABEL")     // "L'Observatoire de Bègles"
websiteURL := os.Getenv("SITE_URL")      // "https://lobservatoiredebegles.fr"

prompt = fmt.Sprintf("Tu es rédacteur de newsletter municipale pour %s.\n...", cityLabel)
// Remplacer "website_url" hardcodée dans le schéma JSON par websiteURL
```

**Contact (`handler.go`)** : Paramétrer le sujet et le footer :

```go
subject := fmt.Sprintf("[%s] Message de %s", os.Getenv("CITY_LABEL"), cr.Name)
```

Ajouter `CITY_NAME`, `CITY_LABEL`, `CITY_CITIZEN_LABEL` dans `common_env` du CDK, alimentés depuis `city_config`.

---

### Étape 4 — Découplage Brevo par ville

**Objectif :** Chaque ville dispose de ses propres listes et templates Brevo, éventuellement de son propre compte Brevo.

Actions CDK :
- `BREVO_API_KEY` : variable par ville (peut pointer vers un compte Brevo distinct)
- `BREVO_LIST_ID`, `BREVO_TEMPLATE_ID`, `BREVO_NEWSLETTER_TEMPLATE_ID` : déjà paramétrisables via env — s'assurer qu'ils sont dans `city_config`

Modifier le `notifier` pour injecter `CITY_LABEL` comme sender name Brevo :
```go
"sender": map[string]string{
    "name":  os.Getenv("CITY_LABEL"),
    "email": d.senderEmail,
},
```

Valider que les identités SES (`ses_identity_arns`) sont générées dynamiquement depuis `DOMAIN_NAME` — c'est déjà le cas dans le CDK, mais vérifier que le domaine est bien vérifié dans SES pour chaque ville avant déploiement.

---

### Étape 5 — Refonte CI/CD pour cibles multiples

**Objectif :** Un pipeline unique capable de déployer N villes, avec promotion par environnement.

Structure cible du repo :
```
cdk/
  cities/
    begles.env
    bordeaux.env
    paris-14.env
  watchdog_stack.py       # générique
  app.py                  # paramétré par CITY env var
Makefile                  # make deploy CITY=bordeaux
```

Pipeline GitHub Actions (ou équivalent) :
```yaml
strategy:
  matrix:
    city: [begles, bordeaux, paris-14]
steps:
  - run: make deploy CITY=${{ matrix.city }}
    env:
      GEMINI_API_KEY: ${{ secrets[format('{0}_GEMINI_KEY', matrix.city)] }}
      BREVO_API_KEY:  ${{ secrets[format('{0}_BREVO_KEY', matrix.city)] }}
      AWS_PROFILE:    watchdog-admin
```

Gestion des secrets : un secret par ville + par service dans AWS Secrets Manager ou GitHub Actions secrets, avec convention de nommage `{CITY}_{SERVICE}_KEY`.

Makefile cible :
```makefile
build:
    # Build de toutes les Lambdas (inchangé, binaires génériques)

deploy:
    @test -n "$(CITY)" || (echo "Usage: make deploy CITY=<city>"; exit 1)
    cd cdk && CITY=$(CITY) cdk deploy WatchdogStack-$(CITY) --require-approval never
```

---

### Récapitulatif de la Roadmap

| Étape | Nouveaux fichiers | Fichiers modifiés | Bloquant prod Bègles | Bloquant multi-ville |
|---|---|---|---|---|
| 1. Config CDK | `city.schema.json`, `cities/*.json`, `app_v2.py` | aucun (`watchdog_stack.py` intact) | Non | ✅ Oui |
| 2. Scraper interface Go | `scraper_openmairie_v2.go`, `scraper_custom.go` | `scraper.go`, `main.go` (orchestrator) | Non | ✅ Oui |
| 3. Prompts génériques | — | `worker/gemini.go`, `notifier/handler.go`, `contact/handler.go` | Non | ✅ Oui |
| 4. Découplage Brevo | — | `notifier/handler.go`, `watchdog_stack_v2.py` | Non | Recommandé |
| 5. CI/CD matrix | `.github/workflows/deploy.yml` | `Makefile` | Non | Recommandé |

**Règle de migration :** les étapes 1 à 4 se font dans des fichiers nouveaux ou dans des branches feature. `watchdog_stack.py` et les Lambdas existantes ne sont modifiés qu'une fois la version v2 testée sur un stack de staging isolé (`CITY=staging`).

**Pré-requis avant déploiement d'une nouvelle ville :**
1. DNS délégué + certificat ACM créé en `us-east-1`
2. Domaine vérifié dans AWS SES
3. Secrets SSM pré-peuplés (`/watchdog/{city_id}/brevo_api_key`, etc.)
4. Templates Brevo créés dans le compte Brevo de la ville
5. Fichier `cdk/cities/{city_id}.json` validé contre `city.schema.json`

---

## 5. Référence d'Implémentation — Guide Chirurgical

> Section destinée à accélérer l'exécution du refacto. Chaque point liste le fichier exact, les lignes à modifier, le code avant/après, et les dépendances entre étapes.

---

### 5.1 Carte complète des couplages — localisations exactes

#### `lambdas/orchestrator/main.go`
| Ligne | Couplage | Action |
|---|---|---|
| 21-22 | URLs hardcodées `deliberationsListURL`, `nextCouncilURL` | Lire `os.Getenv("SCRAPING_LIST_URL")` et `os.Getenv("SCRAPING_NEXT_COUNCIL_URL")` |
| 47 | `NewScraper(deliberationsListURL)` | `NewScraper(scraperType, listURL, nextURL, configJSON)` |

#### `lambdas/orchestrator/scraper.go`
| Ligne | Couplage | Action |
|---|---|---|
| 30 | `NewScraper(listURL string)` | Refactorer en factory `NewScraper(cms, listURL, nextURL, cfgJSON string) CityScraper` |
| 41 | `doc.Find("li.list__item")` | Déplacer dans `scraper_openmairie_v2.go` |
| 86 | `doc.Find(".telecharger-item")` | Idem |
| 101 | `doc.Find(".infowidget .rte ul li strong")` | Idem |
| 129-137 | `normalizeCategory()` hardcodée | Lire depuis `category_mapping` dans `SCRAPING_CONFIG_JSON` |

#### `lambdas/worker/gemini.go`
| Ligne | Couplage | Action |
|---|---|---|
| 20 | `const deliberationPrompt = \`...\`` | Transformer en fonction `buildDeliberationPrompt(cityName, citizenLabel string) string` |
| 21 | `"du conseil municipal de Bègles"` | `fmt.Sprintf("... de %s", cityName)` |
| 35 | `"les Béglaises et Béglais"` | `fmt.Sprintf("... pour %s", citizenLabel)` |
| 111 | `analyzeWithGemini(ctx, apiKey, pdfBytes)` | Ajouter `cityName, citizenLabel string` en paramètres |

#### `lambdas/notifier/handler.go`
| Ligne | Couplage | Action |
|---|---|---|
| 143 | `envOrDefault("SENDER_EMAIL", "noreply@lobservatoiredebegles.fr")` | Fallback générique ou forcer via CDK |
| 381 | `"Tu es rédacteur de newsletter municipale pour L'Observatoire de Bègles."` | `fmt.Sprintf("... pour %s.", os.Getenv("CITY_LABEL"))` |
| 407 | `"pour les Béglaises et les Béglais"` | `os.Getenv("CITY_CITIZEN_LABEL")` |
| 419 | `"website_url": "https://lobservatoiredebegles.fr"` | `fmt.Sprintf("\"website_url\": \"%s\"", os.Getenv("SITE_URL"))` |
| 579 | `"name": "L'Observatoire de Bègles"` | `"name": os.Getenv("CITY_LABEL")` |

#### `lambdas/contact/handler.go`
| Ligne | Couplage | Action |
|---|---|---|
| 66 | `"[Observatoire Bègles] Message de %s"` | `fmt.Sprintf("[%s] Message de %%s", os.Getenv("CITY_LABEL"), cr.Name)` |
| 77 | `"depuis l'Observatoire de Bègles"` | `os.Getenv("CITY_LABEL")` |

#### `lambdas/confirmer/main.go`
| Ligne | Couplage | Action |
|---|---|---|
| 19 | Fallback `"https://lobservatoiredebegles.fr/merci.html"` hardcodé | Supprimer le fallback — `REDIRECTION_URL` est déjà injecté par CDK, pas besoin de fallback dans le code |

#### `lambdas/brevo-subscriber/handler.py` ⚠️ Couplage non listé dans l'audit initial
| Ligne | Couplage | Action |
|---|---|---|
| 42-43 | `'name': "L'Observatoire de Bègles"` et `'email': 'contact@lobservatoiredebegles.fr'` hardcodés | Lire `os.environ['CITY_LABEL']` et `os.environ['SENDER_EMAIL']` |
| 47 | `'subject': "Bienvenue à l'Observatoire"` | `os.environ.get('WELCOME_SUBJECT', 'Bienvenue')` |
| 48-49 | HTML de bienvenue hardcodé en français et lié à Bègles | Passer par un template Brevo (comme les autres Lambdas) |

> Cette Lambda Python est actuellement **hors CDK** (pas référencée dans `watchdog_stack.py`). À intégrer au stack v2.

#### `cdk/watchdog_stack.py`
| Ligne | Couplage | Action |
|---|---|---|
| 65 | `historical_bucket_name = "watchdogstack-websitebucket75c24d94-clsmaf2ocvxq"` | Lire depuis `city_config["aws"]["existing_s3_bucket"]`, conditionnel |
| 73 | `os.environ.get("DOMAIN_NAME", "lobservatoiredebegles.fr")` | `city_config["domain"]["apex"]` |
| 166 | `"FROM_EMAIL": "watchdog@begles.citoyen"` | `city_config["email"]["sender_transactional"]` |
| 209-211 | Fallbacks `site_url`, `sender_email`, `contact_sender` | Depuis `city_config` |
| 213-215 | `ses_identity_arns` hardcodé sur `lobservatoiredebegles.fr` | `city_config["email"]["ses_identity_domain"]` |
| 235 | `"gemini-2.5-flash"` hardcodé pour Notifier | `city_config["gemini"]["notifier_model"]` |
| 362 | `dashboard_name="Watchdog-Begles-Health"` | `f"Watchdog-{city_id}-Health"` |

---

### 5.2 Variables d'environnement Lambda — État actuel vs Cible

Toutes les variables marquées **NOUVEAU** doivent être ajoutées dans `common_env` du CDK et alimentées depuis `city_config`.

| Variable | Lambdas concernées | Statut | Source dans city_config |
|---|---|---|---|
| `CITY_NAME` | worker, notifier | **NOUVEAU** | `city.name` |
| `CITY_LABEL` | notifier, contact, brevo-subscriber | **NOUVEAU** | `city.label` |
| `CITY_CITIZEN_LABEL` | worker, notifier | **NOUVEAU** | `city.citizen_label` |
| `SITE_URL` | notifier, confirmer | Existant (CDK le passe) | `domain.www` |
| `SCRAPING_LIST_URL` | orchestrator | **NOUVEAU** | `scraping.deliberations_list_url` |
| `SCRAPING_NEXT_COUNCIL_URL` | orchestrator | **NOUVEAU** | `scraping.next_council_url` |
| `SCRAPER_TYPE` | orchestrator | **NOUVEAU** | `scraping.cms_type` |
| `SCRAPING_CONFIG_JSON` | orchestrator | **NOUVEAU** | Sérialisation du bloc `scraping` entier |
| `GEMINI_MODEL` | worker, aggregator, notifier | Existant | `gemini.worker_model` etc. |
| `BREVO_API_KEY` / `MAIL_API_KEY` | subscriber, confirmer, contact, notifier | Existant | SSM `brevo.api_key_ssm` |
| `BREVO_LIST_ID` | subscriber, confirmer, notifier | Existant | `brevo.list_id` |
| `BREVO_TEMPLATE_ID` | subscriber | Existant | `brevo.confirmation_template_id` |
| `BREVO_NEWSLETTER_TEMPLATE_ID` | notifier | Existant | `brevo.newsletter_template_id` |
| `SENDER_EMAIL` | notifier, subscriber, brevo-subscriber | Existant | `email.sender_newsletter` |
| `WELCOME_SUBJECT` | brevo-subscriber | **NOUVEAU** | À ajouter au schéma si besoin |

---

### 5.3 Architecture des modules Go — Points de vigilance

Chaque Lambda est un **module Go indépendant**. Il n'y a pas de module partagé (`shared/` ou `internal/`). Conséquences pour le refacto :

| Module | Chemin | Nom de module |
|---|---|---|
| orchestrator | `lambdas/orchestrator/` | `github.com/watchdog/orchestrator` |
| worker | `lambdas/worker/` | `github.com/watchdog/worker` |
| aggregator | `lambdas/aggregator/` | `aggregator` (nom court, à harmoniser) |
| publisher | `lambdas/publisher/` | `github.com/watchdog/publisher` |
| subscriber | `lambdas/subscriber/` | `github.com/watchdog/subscriber` |
| confirmer | `lambdas/confirmer/` | `github.com/watchdog/confirmer` |
| contact | `lambdas/contact/` | `github.com/watchdog/contact` |
| notifier | `lambdas/notifier/` | `github.com/watchdog/notifier` |

**Règle de build :** chaque `go mod tidy` et `go build` doit être exécuté depuis le répertoire du module. Le `Makefile` actuel gère cela correctement — ne pas centraliser les modules sans revoir le build.

**Si du code partagé émerge** (ex: `ScraperConfig` struct utilisée par l'orchestrator ET le CDK via sérialisation JSON) : le définir uniquement dans le module qui en a besoin côté Go. Côté Python/CDK, lire directement depuis le JSON de config.

---

### 5.4 Couverture de tests existante — Ce qui est déjà testé

| Fichier de test | Couvre | À adapter pour le refacto |
|---|---|---|
| `lambdas/orchestrator/scraper_test.go` | `ScrapeCouncilList`, `ScrapePDFLinks` avec HTML fixtures | ✅ Oui — ajouter tests par implémentation CMS |
| `lambdas/worker/gemini_test.go` | `parseGeminiResponse`, `budgetAmountFloatRe` | ✅ Oui — ajouter test `buildDeliberationPrompt(cityName, citizenLabel)` |
| `lambdas/worker/handler_test.go` | `handleRecord`, `deliberationID`, `attrInt` | Pas d'impact direct |
| `lambdas/publisher/handler_test.go` | `buildDataJSON`, `fetchNextCouncilDate` | Pas d'impact direct |
| `lambdas/subscriber/handler_test.go` | `handleSubscribe`, `isValidEmail` | Pas d'impact direct |
| `lambdas/confirmer/handler_test.go` | Handler confirmer | Pas d'impact direct |
| `lambdas/contact/handler_test.go` | Handler contact | ✅ Oui — tester avec `CITY_LABEL` env var |
| `lambdas/aggregator/aggregator_test.go` | `computeStats`, `dominantTheme`, `voteClimat` | Pas d'impact direct |

**Tests manquants à écrire avant merge du refacto :**
- `scraper_openmairie_v2_test.go` : mêmes fixtures que `scraper_test.go` actuel, migré vers la nouvelle interface
- `scraper_factory_test.go` : vérifie que `NewScraper("openmairie_v2", ...)` retourne le bon type
- `gemini_test.go` : `TestBuildDeliberationPrompt` — vérifier que `cityName` et `citizenLabel` apparaissent dans le prompt généré

---

### 5.5 Pattern CDK pour résolution SSM

Utiliser `ssm.StringParameter.value_from_lookup()` pour les secrets non sensibles en synthèse, et `ssm.StringParameter.value_for_string_parameter()` pour les valeurs résolues au deploy :

```python
from aws_cdk import aws_ssm as ssm

# Pour les valeurs non-sensibles (résolues à la synthèse CDK)
acm_arn = ssm.StringParameter.value_from_lookup(
    self, "AcmCertArn",
    parameter_name=city_config["domain"]["acm_certificate_arn_ssm"]
)

# Pour les clés API (résolues au deploy, jamais en clair dans cdk.out/)
brevo_key = ssm.StringParameter.value_for_string_parameter(
    self, "BrevoApiKey",
    parameter_name=city_config["brevo"]["api_key_ssm"]
)
# → passe directement comme Lambda env var : "BREVO_API_KEY": brevo_key
```

> ⚠️ `value_from_lookup()` nécessite un accès AWS au moment de `cdk synth`. En CI sans credentials, utiliser une valeur dummy avec `os.environ.get(..., "dummy")` — ne pas appeler `value_from_lookup` sans guard.

---

### 5.6 Ordre d'exécution recommandé du refacto (sans casser prod)

```
1. [Branche: feat/city-config]
   - Créer watchdog_stack_v2.py + app_v2.py
   - Valider : cdk synth avec CITY=begles retourne un template identique à l'actuel
   - PAS de deploy

2. [Branche: feat/scraper-interface]  ← dépend de rien
   - Créer scraper.go (interface) + scraper_openmairie_v2.go (code actuel migré)
   - Migrer scraper_test.go → scraper_openmairie_v2_test.go
   - go test ./... doit passer

3. [Branche: feat/generic-prompts]  ← dépend de rien
   - Modifier worker/gemini.go : deliberationPrompt → buildDeliberationPrompt()
   - Modifier notifier/handler.go : buildNewsletterPrompt() lit CITY_LABEL, CITY_CITIZEN_LABEL
   - Modifier contact/handler.go : subject et footer depuis CITY_LABEL
   - go test ./... doit passer (les tests existants ajouteront les env vars)

4. [Branche: feat/brevo-subscriber-fix]  ← dépend de rien
   - Paramétiser brevo-subscriber/handler.py
   - L'intégrer dans watchdog_stack_v2.py

5. [Merge dans main, après validation staging]
   - Deploy CITY=staging (nouveau stack isolé, vrai AWS, vrai Gemini)
   - Vérifier pipeline complet sur staging
   - Basculer watchdog_stack.py → watchdog_stack_v2.py pour Bègles
```
