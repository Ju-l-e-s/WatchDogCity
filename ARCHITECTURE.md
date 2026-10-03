# 🔭 Architecture Globale - WatchdogCity

Ce document décrit l'architecture de la plateforme de veille citoyenne et de transparence municipale **WatchdogCity** (opérant actuellement sous le nom *L'Observatoire de Bègles*).

---

## 1. Topologie Globale & Flux de Données (Event-Driven)

La plateforme repose sur un pipeline 100% serverless, asynchrone et guidé par les événements, géré par AWS CDK.

```text
EventBridge (cron lun-ven 18h Paris / 16h UTC)
    └─► Orchestrator Lambda (Go, ARM64)
            ├─ Scrape le site de la mairie (délibérations + PDFs)
            ├─ Écrit/Met à jour le conseil dans DynamoDB (watchdog-councils)
            └─ Envoie un message par PDF dans SQS (watchdog-pdf-queue)
                    └─► Worker Lambda (Go, ARM64)
                            ├─ Télécharge le PDF depuis le site municipal
                            ├─ Extrait & analyse les données structurées via Gemini (gemini-2.5-flash)
                            ├─ Écrit la délibération dans DynamoDB (watchdog-deliberations, status=PENDING)
                            └─ Incrémente atomiquement le compteur de PDFs traités.
                               Si processed_pdfs == total_pdfs:
                                    └─► Définit le conseil à PENDING et invoque le Validator Lambda.

DynamoDB Streams (sur la table watchdog-deliberations)
    └─► Aggregator Lambda (Go, ARM64)
            ├─ Calcule et agrège les statistiques globales du conseil (climat, votes, thèmes)
            ├─ Écrit la synthèse globale du conseil dans DynamoDB
            └─ Invoque également le Validator Lambda en fin de traitement (si non encore fait).

Validator Lambda (Go, ARM64) — Le "QC Gateway" & Générateur Deprivé
    ├─ 1. Charge le conseil et toutes ses délibérations (Query paginée via GSI).
    ├─ 2. Calcule la Baseline statistique des conseils approuvés historiques (moyenne Néant, médiane budget).
    ├─ 3. Exécute les validations déterministes (D1-D10) et statistiques (S1-S6).
    ├─ 4a. Verdict = QUARANTINED (si anomalie HIGH) :
    │       ├─ Écrit le rapport de violation (qc_report) et passe le statut à QUARANTINED.
    │       ├─ Émet une métrique CloudWatch "QcQuarantined" (Alarme SNS activée).
    │       └─ [Optionnel] Auto-guérison : Ré-enfile les PDFs des délibérations en faute dans SQS (limite de 2 essais).
    └─ 4b. Verdict = APPROVED :
            ├─ Sélectionne les champs ColdDeliberation, dont le résumé et les impacts rédigés par le Worker.
            ├─ Invoque Gemini (gemini-2.5-pro) sous privation sensorielle pour générer le contenu de la newsletter.
            ├─ Sauvegarde les newsletter_params et passe le statut du conseil à APPROVED.
            └─ Invoque le Publisher puis le Notifier (de manière asynchrone).

Pour rééditer une newsletter d'un conseil déjà approuvé et encore non envoyé, l'événement manuel `{"council_id":"…","refresh_newsletter":true}` relance les contrôles QC et la génération Gemini depuis les délibérations stockées. Il remplace seulement `newsletter_params_json` si le conseil est toujours `APPROVED` et sans `newsletter_sent_at` ; il ne déclenche ni publication ni envoi. Le Notifier exige `fact_check_version` dans les paramètres validés, puis actualise le sujet, les paramètres et la liste du brouillon Brevo existant avant le nouvel essai. Un échec de cette actualisation bloque l'envoi.

Le Worker demande une seconde lecture Gemini du PDF original et des champs extraits avant toute écriture DynamoDB. Le Validator vérifie les identifiants source de chaque rubrique de newsletter, les montants, les votes et l'introduction, puis fait relire le texte final par Gemini à partir des champs contrôlés. Une erreur de contenu peut déclencher au plus deux réécritures guidées par les constats ; chaque nouvelle version repasse tous les contrôles. Une réponse absente, invalide ou encore défavorable bloque la publication ou l'envoi concerné. Les anciens conseils n'ont pas automatiquement cette preuve de relecture PDF ; leur remise à niveau reste un chantier de backfill.

À chaque passage, l'Orchestrator compare les PDF des conseils déjà traités avec leur liste source enregistrée. Un ajout, retrait ou remplacement déclenche une erreur de cycle et une alerte pour correction contrôlée ; l'Orchestrator ne modifie pas automatiquement un conseil déjà approuvé. Les anciens conseils sans liste source restent à inventorier séparément.

Publisher Lambda (Go, ARM64)
    ├─ Publie les délibérations rattachées aux conseils avec qc_status = APPROVED.
    ├─ Écarte les conseils vides ; pour les nouvelles approbations marquées par la QC Gateway,
    │  revérifie que le nombre de délibérations et le compteur traité égalent total_pdfs.
    │  Les anciens conseils incomplets restent visibles en attendant un backfill contrôlé.
    ├─ Génère le fichier public data.json et le déploie sur le bucket S3.
    └─ Déclenche une invalidation de cache CloudFront chirurgicale.

Notifier Lambda (Go, ARM64)
    ├─ Reçoit les newsletter_params pré-générés et approuvés.
    ├─ Relit les paramètres validés du conseil APPROVED, puis crée le brouillon Brevo pour la liste de production.
    ├─ Avec AUTO_SEND_ENABLED=false (défaut), crée et envoie une campagne distincte de même contenu vers la liste
    │  test #3, sans envoyer le brouillon de production ; sendTest ajoute un aperçu si BREVO_TEST_EMAIL est configuré.
    ├─ Avec AUTO_SEND_ENABLED=true, appelle sendNow sur la campagne de production après l'aperçu.
    ├─ Enregistre l'identifiant de la campagne de production dans DynamoDB ; une invocation reconcile_only
    │  vérifie son statut Brevo après envoi manuel et renseigne newsletter_sent_at seulement si elle est sent.
    └─ En mode test isolé (test_list_id = 3), envoie uniquement à la liste test sans toucher au registre public.

BrevoCampaignWebhook Lambda (Go, ARM64) — créée si WEBHOOK_SECRET est configuré, avec Function URL publique protégée par ce jeton
    ├─ Reçoit les événements marketing email `delivered` de Brevo (camp_id, ts_sent).
    ├─ Retrouve le conseil APPROVED par newsletter_campaign_id et contrôle la campagne Brevo et sa liste de production.
    └─ Inscrit newsletter_sent_at conditionnellement, sans écraser un envoi déjà enregistré.

API Gateway (Endpoints publics d'interaction)
    ├─ POST /subscribe  ──► Subscribe Lambda (Go) ──► Enregistrement DynamoDB + Email confirmation Brevo
    ├─ GET  /confirm    ──► Confirmer Lambda (Go)  ──► Activation du contact dans la liste Brevo + redirection S3
    └─ POST /contact    ──► Contact Lambda (Go)    ──► Validation du jeton Turnstile + Email admin via SES
```

---

## 2. Choix FinOps (Scale-to-Zero & ARM64)

Pour maintenir un coût d'exploitation proche de **0€ (Free Tier First)** même sous de fortes charges :
- **AWS Lambda Graviton (ARM64)** : Toutes les fonctions s'exécutent sur architecture ARM64 via le runtime personnalisé `PROVIDED_AL2023`, offrant un gain de coût d'environ 20% par rapport à x86.
- **DynamoDB Pay-per-Request** : Aucun provisionnement de débit inutile (RCU/WCU). Le stockage et les requêtes s'adaptent dynamiquement à la demande et s'annulent en période d'inactivité.
- **CDN Caching** : CloudFront et la mise en cache statique de Cloudflare capturent 99% du trafic de lecture, protégeant S3 des requêtes répétitives et minimisant les coûts de transfert de données (Egress).
- **Protection par WAF** : Le filtrage Cloudflare en amont (Bot Fight Mode, Turnstile) évite l'exécution inutile de fonctions Lambda payantes par des robots ou du spam.

---

## 3. Règles de Validation Déterministes de la QC Gateway (Validator)

Le module de validation de la QC Gateway s'assure qu'aucun résumé incohérent ou altéré par une anomalie de LLM ne soit publié ou envoyé aux citoyens. Aucun modèle d'IA ne juge les données ; la logique est 100% déterministe.

### 3.1 Règles Déterministes (ValidateDeterministic - HIGH = Quarantaine)

| Réf | Règle | Condition d'Échec / Comportement |
|---|---|---|
| **D1** | Validité des Enums | `topic_tag`, `budget_type` ou `climate_impact` non canoniques (inconnus de la configuration). |
| **D2** | Cohérence Budget/AUCUN | Si `budget_type` est "AUCUN" mais `budget_impact` > 0 (ou vice-versa). |
| **D3** | Signe du Budget | Un montant d'impact budgétaire global ou de sous-catégorie est négatif. |
| **D4** | Ventilation Budgétaire | Si la somme des montants ventilés diverge du total déclaré `budget_impact` au-delà de 1 EUR. |
| **D5** | Enums de Ventilation | Une catégorie dans la liste de ventilation du budget n'est pas canonique. |
| **D6** | Cohérence des Votes | `has_vote = false` mais des votes (pour/contre/abstention) sont déclarés > 0, OU `has_vote = true` mais les 3 compteurs sont vides (nil). |
| **D7** | Signe des Votes | N'importe quel compteur de vote est inférieur à 0. |
| **D8** | Texte Obligatoire | Le `title`, `summary` ou `decision` est vide ou ne contient que des espaces. |
| **D9** | Impacts Présents | Le champ `impacts` est vide, absent, ou contient la chaîne littérale `"null"` (doit être soit `"Néant"`, soit un texte valide). |
| **D10** | Balisage Fuité (WARN) | Le résumé ou le texte décisionnel contient du code HTML, des liens markdown, ou des appels à l'action promotionnels (`En savoir plus`, `Voir sur le site`, `→`). |
| **D11** | Réattribution de surface | Un impact présente la surface totale de terrains cédés comme une surface d'espaces paysagers alors que le résumé ne justifie pas cette attribution. |

### 3.2 Règles Statistiques (ValidateStatistical - HIGH = Quarantaine)

| Réf | Règle | Sévérité | Condition d'Échec |
|---|---|---|---|
| **S1** | Conseil Vide | **HIGH** | `processed_pdfs` > 0 mais aucune délibération n'a été insérée en base pour ce conseil. |
| **S2** | Plafond Néant | **HIGH** | Plus de 65% des délibérations du conseil retournent un impact qualifié de `"Néant"`. |
| **S3** | Dérive du Néant | **WARN** | Le taux de `"Néant"` s'écarte de la moyenne historique avec un z-score > 3.0 (nécessite une base historique de ≥ 5 conseils approuvés). |
| **S4** | Effondrement des Catégories | **WARN** | Toutes les délibérations partagent exactement le même thème (`topic_tag`) pour un conseil de ≥ 4 délibérations. |
| **S5** | Aberration Budgétaire | **HIGH** | Une délibération unique affiche un impact > 500 000 000 EUR. |
| **S6** | Plausibilité des Votants | **HIGH** | La somme (Pour + Contre + Abstention) dépasse 60 votants (la mairie de Bègles comptant ~35-39 élus). |
| **S7** | Complétude des PDF traités | **HIGH** | `total_pdfs` est nul, `processed_pdfs` diffère de `total_pdfs`, ou le nombre de délibérations stockées diffère de `total_pdfs`. Ce contrôle ne vérifie pas que la liste des PDF source ne contient que des délibérations. |
| **S8** | Concordance avec la liste source | **HIGH** | Les identifiants PDF attendus, les URL et les lignes effectivement comptées doivent correspondre exactement. Les ordres du jour et les annexes budgétaires sont exclus de cette liste. |
| **S9** | Provenance du contrôle PDF | **HIGH** | Chaque délibération d'un nouveau conseil doit porter le modèle et la date du contrôle factuel du PDF. |

---

## 4. Sécurité & Protection de la Neutralité Éditioriale

### 4.1 Sécurité AWS & IAM
- **Principe de moindre privilège** : Chaque fonction possède un rôle IAM unique et restreint au strict minimum (ex : la Lambda `Worker` n'a pas accès à SES ou S3 ; `Notifier` ne peut pas modifier la file SQS).
- **Chiffrement & Restauration** : Les tables DynamoDB ont le Point-In-Time-Recovery (PITR) activé. Le bucket S3 est protégé contre la suppression accidentelle.

### 4.2 Champs transmis au modèle de rédaction
Le Validator construit une liste `ColdDeliberation` avant de générer la newsletter :
1. **PDF source exclu** : Le modèle de rédaction ne reçoit pas le PDF complet ni le détail textuel des désaccords.
2. **Champs sélectionnés** : Il reçoit le titre, la catégorie, le budget, les votes et les indicateurs calculés, mais aussi `Summary` et `Impacts`, deux champs textuels produits par le Worker. Le prompt utilise ces textes pour rédiger le contexte et l'impact citoyen.
3. **Limite de la garantie** : La sélection des champs et les règles déterministes de la QC Gateway réduisent certains risques, mais elles ne rendent pas les textes générés exempts d'inventions ou de biais. Les statistiques et calculs financiers restent calculés en Go.
