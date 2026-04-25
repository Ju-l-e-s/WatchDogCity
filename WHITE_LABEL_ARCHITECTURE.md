# White-Label Architecture — Watchdog Municipal Newsletter

---

## 1. Cartographie de l'Existant (As-Is)

### Vue d'ensemble

Pipeline entièrement serverless sur AWS, géré par AWS CDK (Python). Neuf fonctions Lambda Go (ARM64) orchestrent le cycle de vie complet : scraping → analyse IA → publication web → newsletter.

```text
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
| IA | Google Gemini API |
| DNS/TLS | Route53 + ACM (externe au CDK) |

---

## 2. Audit de Couplage — Dette Spécifique à Bègles

### 2.1 URLs de scraping (couplage fort, bloquant)
Les constantes `deliberationsListURL` et `nextCouncilURL` sont hardcodées dans `lambdas/orchestrator/main.go`.

### 2.2 Sélecteurs CSS du scraper (couplage fort, bloquant)
Le scraper dans `lambdas/orchestrator/scraper.go` est écrit pour le CMS spécifique de Bègles. Les sélecteurs (ex: `li.list__item`) et la fonction `normalizeCategory()` ne sont pas portables.

### 2.3 Prompts Gemini (couplage moyen)
Références hardcodées à "Bègles" et "Béglaises" dans `lambdas/worker/gemini.go` et `lambdas/notifier/handler.go`.

### 2.4 Configuration Brevo & Email (couplage fort)
Noms d'expéditeurs et URLs de site en dur dans les fonctions de notification et de contact.

### 2.5 Noms des ressources AWS & CDK (dette d'extensibilité)
Valeurs par défaut hardcodées dans `cdk/watchdog_stack.py`. Les tables DynamoDB n'ont pas de notion d'appartenance à une ville.

---

## 3. Architecture Cible (To-Be) — Recommandation & Sécurités

Le choix architectural initial d'isoler chaque ville dans un stack AWS distinct (Option B) a été écarté car il génère une dette opérationnelle critique (maintenir N pipelines).

### 3.1 L'Infrastructure : Multi-Tenant Centralisé (Single-Table Design) ✅ Recommandée

**Principe** : Un seul déploiement AWS CDK, une seule infrastructure à maintenir. Le cloisonnement des villes se fait logiquement au niveau de la base de données.

- **Le Single-Table Design** : Les tables DynamoDB incluent une clé de partitionnement universelle (`city_id`).
  - *Ex: PK = CITY#begles | SK = COUNCIL#2026-02-24*
- **Configuration Dynamique** : Les Lambdas interrogent une table `watchdog-configs` (ou le Parameter Store) en utilisant le `city_id` pour récupérer les identifiants Brevo, URLs de scraping, et prompts spécifiques.

**Avantages** : Zéro surcoût opérationnel à l'ajout d'une ville. Une correction de bug profite instantanément à toutes les villes.

### 3.2 Transparence Algorithmique & Risque Démocratique

L'utilisation d'un LLM introduit un biais de lissage. Pour garantir la probité du projet :

1. **Traçabilité absolue (Lien Source)** : Le modèle de données et le prompt Gemini doivent restituer l'URL exacte du PDF original.
2. **L'Open-Prompting (Page /transparence)** : Une route dédiée affichera publiquement la version du modèle, la température, et le texte intégral des Prompts.
3. **Avertissement Légal** : Inclusion automatique d'un disclaimer : *"Cette synthèse est générée par IA... Seul le document PDF officiel fait foi."*

---

## 4. Plan de Transformation — Roadmap Technique

### Étape 1 — Refonte Base de Données (Single-Table & Config)
- **Modèle de Données** : Mettre à jour les schémas DynamoDB pour exiger un `city_id` dans la Partition Key (PK).
- **Table de Configuration** : Créer `watchdog-cities-config` pour stocker le JSON de chaque ville.
- **Mise à jour Lambdas** : Modifier les fonctions pour propager le `city_id` dans SQS et DynamoDB.

### Étape 2 — Abstraction du Scraper (Pattern Strategy en Go)
Créer une interface Go `CityScraper` et des implémentations par type de site :
- `scraper_openmairie.go` (code actuel refactorisé).
- `scraper_wordpress.go` (futures villes).

### Étape 3 — Paramétrisation des Prompts et Transparence
- **Worker** : Variables injectées (`cityName`, `citizenLabel`).
- **Extraction URL** : Forcer le retour de `source_pdf_url` par l'IA.
- **Frontend Data** : Générer un `data.json` incluant les métadonnées de transparence.

### Étape 4 — Découplage des Communications (Brevo & SES)
Router dynamiquement les emails via les clés API et identités récupérées en fonction du `city_id`.

---

## 5. Référence d'Implémentation — Guide Chirurgical

### 5.1 Modifications DynamoDB (Le Contrat de Données)

| Table Actuelle | Clé Primaire Cible (PK) | Clé de Tri Cible (SK) |
|---|---|---|
| watchdog-councils | `CITY#{city_id}` | `COUNCIL#{date}` |
| watchdog-deliberations | `COUNCIL#{city_id}#{date}` | `DELIB#{id}` |
| watchdog-subscribers | `CITY#{city_id}` | `USER#{email}` |
| **NOUVEAU: configs** | `CITY#{city_id}` | `METADATA` |

### 5.2 Le Fichier Central : `cities_registry.json` (Source of Truth)

Un registre central servira à initialiser la table de configuration.

```json
{
  "begles": {
    "identity": {
      "name": "Bègles",
      "label": "L'Observatoire de Bègles",
      "citizen_label": "les Béglaises et Béglais"
    },
    "scraper": {
      "engine": "openmairie",
      "list_url": "...",
      "next_council_url": "..."
    },
    "brevo": {
      "ssm_api_key_path": "/watchdog/begles/brevo_key",
      "list_id": "2"
    }
  }
}
```

### 5.3 Ordre d'exécution recommandé (Safe Rollout)

1. **[Branche: feat/dynamo-multitenant]** : Migration vers le partitionnement `CITY#id`.
2. **[Branche: feat/scraper-strategy]** : Extraction de la logique vers l'interface `CityScraper`.
3. **[Branche: feat/transparency]** : Prompt forçant l'URL source et page `/transparence`.
4. **[Branche: feat/dynamic-config]** : Lecture de la configuration dynamique dans les Lambdas.
