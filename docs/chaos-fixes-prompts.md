# Watchdog — Prompts de correction (revue Chaos Engineering)

Recueil des prompts prêts à coller pour corriger les bugs identifiés en revue de
résilience sur le pipeline `watchdog`. Chaque prompt est autonome : il peut être
donné à un agent sans aucun contexte préalable.

## Table des modèles recommandés

| Bloc | Sujet | Bugs | Modèle |
|---|---|---|---|
| Critiques | 5 bugs bloquants | 5 | **Opus 4.7** | fait
| Groupe 1 | Hygiène défensive | 9 | **Haiku 4.5** | fait
| Groupe 2 | Timeouts & ctx | 4 | **Sonnet 4.6** | fait
| Groupe 3 | Pagination DDB | 4 | **Sonnet 4.6** | fait 
| Groupe 4 | Race + counters + S3 atomic | 5 | **Opus 4.7** | fait
| Groupe 5 | Gemini hardening | 3 | **Opus 4.7** | fait
| Groupe 6 | Brevo idempotency | 1 | **Opus 4.7** |
| Groupe 7 | Orchestrator resilience + SQS batch | 4 | **Sonnet 4.6** | fait
| Groupe 8 | Cold start + PII + enums | 3 | **Sonnet 4.6** | fait
| Groupe 9 | CDK infra (DLQ + IAM) | 2 | **Sonnet 4.6** | fait

## Ordre d'exécution conseillé

1. **Critiques** (Opus) — débloque le pipeline.
2. **Groupe 1** (Haiku) — sweep défensif, préchauffe le repo.
3. **Groupes 2, 3, 7** en parallèle (Sonnet, worktrees git distincts). Note :
   2 et 7 touchent l'orchestrator → enchaîner séquentiellement entre eux.
4. **Groupes 8, 9** en parallèle (Sonnet).
5. **Groupe 5** (Opus) — crée les helpers `shared/` réutilisés par le groupe 4.
6. **Groupe 4** (Opus) — dépend de `errors.As` (groupe 1) et des helpers (groupe 5).
7. **Groupe 6** (Opus) — dépend du claim 2-phase (commit BUG #4 du bloc critiques).

> Dépendances dures : Groupe 6 exige que le commit `two-phase newsletter claim`
> (BUG #4 ci-dessous) soit déjà appliqué. Groupe 4 commit 1 réutilise le pattern
> `errors.As` du groupe 1.

---

# BLOC CRITIQUES — 5 bugs bloquants | Opus 4.7

````
Tu es Senior Cloud/Backend Engineer Go + AWS. Repo: watchdog (pipeline serverless
d'analyse de délibérations municipales). Stack: Lambda Go arm64, DynamoDB, SQS,
S3, Gemini API, Brevo API.

MISSION : corriger 5 bugs critiques identifiés en revue chaos engineering.
Fais 5 commits séparés (un par bug), conventional commits, en français-anglais
mixte selon convention repo (cf. git log). PAS de Co-Authored-By Claude.

═══════════════════════════════════════════════════════════════
BUG #1 — aggregator/main.go:37-42 — Corruption silencieuse de vote_climat
═══════════════════════════════════════════════════════════════

SYMPTÔME : voteClimat retourne toujours "consensus" même en séance tendue.

CAUSE : struct `Deliberation.Vote` tagué `dynamodbav:"vote"` (nested), mais
worker/handler.go:120-123 écrit en FLAT (`vote_pour`, `vote_contre`,
`vote_abstention` au top-level de l'item). Reflection AWS SDK ne trouve aucun
attribut `vote` map → Pour/Contre/Abstention toujours nil → totals à 0.

FIX :
- Aplatir les tags dans aggregator/main.go Deliberation struct :
    VotePour   *int `dynamodbav:"vote_pour"`
    VoteContre *int `dynamodbav:"vote_contre"`
    VoteAbst   *int `dynamodbav:"vote_abstention"`
- Adapter computeStats() pour lire les nouveaux champs.
- Ajouter test aggregator_test.go qui marshale un item via worker (key=value
  flat) puis l'unmarshal dans Deliberation et vérifie Pour/Contre non-nil.
- Vérifier qu'aucun autre consommateur (publisher OK, lui lit déjà flat) n'est
  cassé par le changement.

═══════════════════════════════════════════════════════════════
BUG #2 — aggregator/main.go:172-175 — Publisher invoqué sans council_id
═══════════════════════════════════════════════════════════════

SYMPTÔME : newsletter jamais envoyée quand pipeline déclenchée via DynamoDB
Stream → aggregator → publisher.

CAUSE : aggregator invoke publisher avec `InvokeInput` SANS `Payload`. Publisher
décode `PublisherEvent{CouncilID: ""}` → invoque notifier avec
`{"council_id": ""}` → `fetchCouncil("")` retourne erreur 404 → newsletter
silently dropped.

FIX :
- Marshaler payload `{"council_id": councilID}` avant Invoke, comme
  worker.invokePublisher (worker/handler.go:200-210).
- Capturer json.Marshal err.
- Ajouter test qui mock lambdaClient et vérifie Payload non vide + contient
  council_id.

═══════════════════════════════════════════════════════════════
BUG #3 — notifier/handler.go:145 — http.Client sans Timeout
═══════════════════════════════════════════════════════════════

SYMPTÔME : Lambda timeout 15min quand Brevo hang. Claim DDB déjà pris.
Newsletter perdue OU dupliquée selon où a hang.

CAUSE : `httpClient: &http.Client{}` = zero value = pas de timeout = infini.

FIX :
- Remplacer par :
    httpClient: &http.Client{
        Timeout: 30 * time.Second,
        Transport: &http.Transport{
            MaxIdleConns:        10,
            IdleConnTimeout:     90 * time.Second,
            TLSHandshakeTimeout: 10 * time.Second,
        },
    },
- Vérifier que tous les http.NewRequestWithContext passent ctx (déjà OK pour
  createCampaign et triggerSend).

═══════════════════════════════════════════════════════════════
BUG #4 — notifier/handler.go:188-211 — Claim-before-send asymétrique
═══════════════════════════════════════════════════════════════

SYMPTÔME : 1 panne Brevo = newsletter hebdo morte définitivement.
ConditionalCheckFailedException bloque tout retry.

CAUSE : claim DDB `newsletter_sent_at` posé AVANT `sendCampaign`. Si Brevo
échoue, claim reste, mail jamais parti.

FIX : pattern 2-phase commit logique :
1. Phase pending : UpdateItem conditionnel `SET newsletter_pending_at = :now`
   avec `ConditionExpression: attribute_not_exists(newsletter_sent_at) AND
   (attribute_not_exists(newsletter_pending_at) OR newsletter_pending_at < :stale_threshold)`
   où :stale_threshold = now - 10min (TTL pending).
2. Si Brevo `sendCampaign` réussit (campaign 201 ET sendNow ≤204) → UpdateItem
   `SET newsletter_sent_at = :now REMOVE newsletter_pending_at`.
3. Si Brevo échoue → UpdateItem `REMOVE newsletter_pending_at` pour permettre
   retry après backoff.
4. Si Lambda crash entre pending et sent : un retry après 10min reprendra la
   main grâce au stale_threshold.

Garder le mode TestListID branch comme aujourd'hui (pas de claim si test).

Ajouter test :
- 2 invocations concurrentes → seule la 1ère atteint Brevo.
- Brevo fail → claim pending nettoyé, retry possible immédiat.
- Claim pending vieille de 11min → retry passe.

═══════════════════════════════════════════════════════════════
BUG #5 — orchestrator/main.go:130-151 — Overwrite total_pdfs en course
═══════════════════════════════════════════════════════════════

SYMPTÔME : councils figés "incomplets" ou complétion déclenchée 2× quand site
mairie change le nombre de PDFs entre 2 scrapes alors que workers tournent.

CAUSE : PutItem unconditionnel écrase `total_pdfs` et réinitialise
`processed_pdfs` à `len(processedSet)`. Worker en cours d'incrément voit un
counter modifié → écart total/processed incohérent.

FIX :
- Remplacer PutItem par UpdateItem :
    UpdateExpression: `SET title=:t, summary=:s, category=:c, date=:d,
                       source_url=:u, total_pdfs=:tp,
                       processed_pdfs=if_not_exists(processed_pdfs, :pp),
                       created_at=if_not_exists(created_at, :ca)`
- :pp = len(processedSet) (utilisé uniquement si attribut absent).
- :ca = time.Now()…RFC3339.
- Si le scrape ramène un total_pdfs INFÉRIEUR à processed_pdfs existant,
  logger warn explicite (`processed > total`, anomalie source).
- Ajouter test scraper_test.go (ou main_test.go) qui simule :
  (a) première découverte → write complet.
  (b) re-découverte avec processed_pdfs=3 existant → vérifier processed
      préservé.

═══════════════════════════════════════════════════════════════
CONTRAINTES GLOBALES
═══════════════════════════════════════════════════════════════

- Toutes Lambda compilées arm64 (GOOS=linux GOARCH=arm64). Vérifier `make build`
  passe.
- `go test ./...` doit passer (existant + nouveaux tests).
- Pas d'ajout de dépendance externe sans justification.
- Commits :
  * Sujet ≤72 char, conventional commits (`fix(aggregator):`, `fix(notifier):`,
    `fix(orchestrator):`).
  * Corps explique POURQUOI le bug existait + impact prod, pas seulement le
    quoi.
- Pas de Co-Authored-By Claude (cf. memory/feedback_commit_style.md).
- Avant de finir, run `cdk synth` pour valider que rien ne casse côté infra.
- Reporte à la fin : 5 commits faits, hash courts, résumé 1 ligne par bug,
  status `go test` + `make build`.

Tu peux commencer par lire :
- lambdas/aggregator/main.go
- lambdas/notifier/handler.go
- lambdas/orchestrator/main.go
- lambdas/worker/handler.go (référence pour pattern flat write)
- Tests existants pour respecter conventions.

Pose UNE question si ambiguïté bloquante. Sinon procède.
````

---

# GROUPE 1 — Hygiène défensive | Haiku 4.5

````
# Rôle
Tu es un développeur Go senior travaillant sur un projet open source de transparence municipale. Tu corriges des bugs défensifs mineurs mais nombreux, dans un sweep coordonné.

# Contexte projet
Le projet `watchdog` est un pipeline serverless AWS qui scrape les délibérations du conseil municipal de Bègles, les analyse via Gemini, et publie un site statique + une newsletter Brevo. Les Lambdas sont écrites en Go, compilées en ARM64, et déployées via AWS CDK (Python). Branche de travail : `main`. Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Appliquer 9 micro-corrections défensives identifiées en revue chaos engineering. Toutes sont mécaniques (remplacements ciblés). Un seul commit final.

# Lectures préalables obligatoires (lecture seule, pour comprendre les conventions)
1. `lambdas/worker/handler.go`
2. `lambdas/notifier/handler.go`
3. `lambdas/publisher/handler.go`
4. `lambdas/aggregator/main.go`
5. `lambdas/worker/gemini.go`
6. `lambdas/shared/enums.go`
7. Le `Makefile` à la racine, pour comprendre comment lancer build et tests.
8. `git log --oneline -20` pour le style de commit utilisé.

Ne modifie AUCUN fichier avant d'avoir lu ces 8 sources.

# Bugs à corriger

## Bug 1.1 — Détection d'erreur DynamoDB fragile (worker + notifier)

**Fichiers** : `lambdas/worker/handler.go:140`, `lambdas/notifier/handler.go:201`

**Symptôme** : si AWS SDK v2 change le format du message d'erreur, le `strings.Contains(err.Error(), "ConditionalCheckFailedException")` cesse de matcher, et le code suit la mauvaise branche (erreur propagée au lieu d'être traitée comme un duplicate).

**Correctif** : remplacer la détection par `errors.As` typé.

```go
import (
    "errors"
    ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

var ccfe *ddbtypes.ConditionalCheckFailedException
if errors.As(err, &ccfe) {
    // branche duplicate / claim déjà pris
} else if err != nil {
    return fmt.Errorf("...: %w", err)
}
```

Adapter le bloc déjà présent autour de chaque ligne sans changer la sémantique des branches.

## Bug 1.2 — Erreurs JSON ignorées dans la création de campagne Brevo

**Fichier** : `lambdas/notifier/handler.go:653-656`

**Symptôme** : si `params` contient un champ non-sérialisable, `json.Marshal` retourne une erreur silencieusement avalée, puis `json.Unmarshal` reçoit `nil` → la campagne Brevo est créée avec `params: null` → le template reçoit un mail vide.

**Correctif** : capturer les deux erreurs.

```go
paramsJSON, err := json.Marshal(params)
if err != nil {
    return 0, fmt.Errorf("marshal newsletter params: %w", err)
}
var paramsMap map[string]interface{}
if err := json.Unmarshal(paramsJSON, &paramsMap); err != nil {
    return 0, fmt.Errorf("unmarshal newsletter params to map: %w", err)
}
```

## Bug 1.3 — Overflow silencieux sur parsing budget

**Fichier** : `lambdas/notifier/handler.go:344-362` (fonction `formatStr`)

**Symptôme** : `fmt.Sscanf(digitsOnly, "%d", &val)` ignore les erreurs. Une chaîne >19 chiffres overflow `int64` et retourne 0 → budget affiché à 0 € dans la newsletter alors qu'il est en réalité énorme.

**Correctif** : utiliser `strconv.ParseInt`.

```go
val, err := strconv.ParseInt(digitsOnly, 10, 64)
if err != nil || val == 0 {
    return ""
}
return formatBudgetFR(val)
```

## Bug 1.4 — Vérification magic PDF trop stricte

**Fichier** : `lambdas/worker/handler.go:232-238` (fonction `downloadPDF`)

**Symptôme** : `bytes.HasPrefix(pdfBytes, []byte("%PDF-"))` rejette des PDF légitimes qui ont un BOM UTF-8 ou des espaces en tête (PDFs générés par certaines vieilles mairies).

**Correctif** : chercher la signature dans les 128 premiers octets.

```go
head := pdfBytes
if len(head) > 128 {
    head = head[:128]
}
if !bytes.Contains(head, []byte("%PDF-")) {
    prefix := pdfBytes
    if len(prefix) > 8 {
        prefix = prefix[:8]
    }
    return nil, fmt.Errorf("not a PDF: got prefix %q", prefix)
}
```

## Bug 1.5 — ID dégénéré sur URL avec slash final

**Fichier** : `lambdas/worker/handler.go:242-245` (fonction `deliberationID`)

**Symptôme** : si une URL finit par `/`, `parts[len(parts)-1]` retourne `""`. L'ID DynamoDB devient une chaîne vide → toutes les délibérations avec ce pattern collisionnent sur la même clé primaire.

**Correctif** :

```go
import "crypto/sha256"
import "encoding/hex"

func deliberationID(url string) string {
    u := strings.TrimRight(url, "/")
    parts := strings.Split(u, "/")
    last := parts[len(parts)-1]
    if last == "" {
        sum := sha256.Sum256([]byte(url))
        return hex.EncodeToString(sum[:8])
    }
    return last
}
```

Ajouter un test unitaire dans `lambdas/worker/handler_test.go` couvrant : URL normale, URL avec slash final, URL vide.

## Bug 1.6 — Erreur MarshalIndent ignorée

**Fichier** : `lambdas/publisher/handler.go:218`

**Symptôme** : `jsonBytes, _ := json.MarshalIndent(...)` avale l'erreur. Si elle échoue, on upload `null` ou un buffer vide sur S3 → le site affiche zéro conseil municipal.

**Correctif** :

```go
jsonBytes, err := json.MarshalIndent(data, "", "  ")
if err != nil {
    return fmt.Errorf("marshal data.json: %w", err)
}
```

## Bug 1.7 — Erreurs UnmarshalListOfMaps ignorées

**Fichier** : `lambdas/publisher/handler.go:269`, `lambdas/publisher/handler.go:290`

**Symptôme** : un seul item DynamoDB corrompu fait silencieusement échouer l'unmarshalling de tout le batch → des conseils disparaissent du `data.json` sans alerte.

**Correctif** : capturer l'erreur et la propager.

```go
var batch []CouncilRecord
if err := attributevalue.UnmarshalListOfMaps(cOut.Items, &batch); err != nil {
    return nil, nil, fmt.Errorf("unmarshal council batch: %w", err)
}
```

Idem pour le bloc deliberations (`var batch []DeliberationRecord`).

## Bug 1.8 — Placeholder « now » au lieu d'un vrai timestamp

**Fichier** : `lambdas/aggregator/main.go:164`

**Symptôme** : l'attribut DynamoDB `updated_at` contient littéralement la chaîne `"now"` au lieu d'un timestamp RFC 3339.

**Correctif** :

```go
":u": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
```

## Bug 1.9 — Gemini sans cap MaxOutputTokens

**Fichiers** : `lambdas/worker/gemini.go:208-213`, `lambdas/aggregator/main.go:257`, `lambdas/notifier/handler.go:323`

**Symptôme** : un PDF adversarial ou un prompt qui dérive peut faire boucler Gemini jusqu'au cap du modèle (32k+ tokens) → coût explose, latence Lambda explose.

**Correctif** : ajouter `MaxOutputTokens: ptrInt32(8192)` dans chaque `GenerateContentConfig`. Si la fonction `ptrInt32` n'existe pas déjà dans le fichier, l'ajouter :

```go
func ptrInt32(i int32) *int32 { return &i }
```

# Contraintes de livraison

- Un seul commit final, message conventionnel :
  ```
  fix(workspace): defensive hygiene sweep across lambdas
  ```
  avec un corps listant les 9 corrections en bullet points (1 ligne chacune, expliquant le pourquoi).
- **Pas de mention « Co-Authored-By: Claude »** dans le commit. Auteur = Jules uniquement.
- Pas d'ajout de dépendance externe.
- Ne touche que les fichiers listés ci-dessus.
- Préserve le style de code existant (tabs, naming, ordering des imports).

# Critères d'acceptation

Avant de commiter, lancer dans l'ordre :

1. `gofmt -l lambdas/` → doit retourner vide.
2. `go vet ./lambdas/...` → aucune sortie.
3. `go test ./lambdas/...` → tous les tests existants + le nouveau test deliberationID passent.
4. `make build` → succès (les binaires ARM64 doivent se construire).

Si une de ces commandes échoue, corrige avant de commiter.

# Format de rapport final

Réponds en français avec :
1. Le hash court du commit.
2. La liste des fichiers modifiés.
3. Le résultat de `go test ./...` (OK/FAIL par paquet).
4. Le résultat de `make build` (succès/échec).
5. Une question si quelque chose te bloque, sinon une seule phrase de clôture.

Procède maintenant.
````

---

# GROUPE 2 — Timeouts & propagation de contexte | Sonnet 4.6

````
# Rôle
Tu es un développeur Go senior spécialisé en résilience d'applications serverless. Tu corriges des fuites de contexte et l'absence de timeouts sur les appels externes.

# Contexte projet
`watchdog` est un pipeline AWS Lambda Go qui appelle plusieurs services externes lents : téléchargement de PDF mairie, API Gemini, scraping HTML. Quand un de ces appels hang, la Lambda reste bloquée jusqu'à son timeout maximum (15 min), bloquant la concurrence et coûtant cher. Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Propager `context.Context` sur tous les appels externes et envelopper chaque appel d'un `context.WithTimeout` adapté à sa latence p99.

# Lectures préalables obligatoires
1. `lambdas/worker/handler.go` (tout)
2. `lambdas/worker/gemini.go` (tout)
3. `lambdas/orchestrator/main.go` (tout)
4. `lambdas/orchestrator/scraper.go` (tout)
5. `lambdas/aggregator/main.go` (au moins lignes 230-270)
6. `lambdas/orchestrator/scraper_test.go` pour comprendre les conventions de test du scraper.

# Bugs à corriger

## Bug 2.1 — `downloadPDF` ignore le contexte Lambda

**Fichier** : `lambdas/worker/handler.go:212-240`

**Cause** : la signature actuelle est `func downloadPDF(url string) ([]byte, error)`. Le `*http.Client` a un `Timeout: 60*time.Second` mais aucune annulation côté contexte. Si le Lambda timeout (par exemple 5 min) fire, la goroutine HTTP continue jusqu'à expiration TCP.

**Correctif** :

1. Changer la signature :
   ```go
   func downloadPDF(ctx context.Context, url string) ([]byte, error) {
       req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
       ...
   }
   ```
2. Côté caller (`lambdas/worker/handler.go:79`), envelopper :
   ```go
   dlCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
   pdfBytes, err := downloadPDF(dlCtx, msg.PDFURL)
   cancel()
   ```

## Bug 2.2 — Appel Gemini worker sans timeout dédié

**Fichier** : `lambdas/worker/handler.go:86`

**Cause** : `analyzeWithGemini(ctx, ...)` réutilise le ctx parent Lambda. Un PDF de 50 Mo peut faire dériver Gemini sur 10 min, monopolisant un slot de concurrence (`reserved_concurrent_executions=5`).

**Correctif** :

```go
gemCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
result, err := analyzeWithGemini(gemCtx, apiKey, pdfBytes)
cancel()
```

## Bug 2.3 — Appel Gemini aggregator sans timeout ni propagation

**Fichier** : `lambdas/aggregator/main.go:236-270` (fonction `askGeminiForSynthesis`)

**Cause** : pas de wrapper timeout. La fonction reçoit `ctx` mais ne le borne pas.

**Correctif** : ajouter en tête de fonction :

```go
ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
defer cancel()
```

## Bug 2.4 — Scraper sans contexte et sans borne mémoire

**Fichier** : `lambdas/orchestrator/scraper.go:120-135` (fonction `fetchDocument`)

**Cause** :
- `fetchDocument(url string)` n'accepte pas de contexte.
- `goquery.NewDocumentFromReader(resp.Body)` lit la totalité du body sans borne → un attaquant qui force `Content-Length: 10GB` peut OOM le Lambda.

**Correctif** :

1. Nouvelle signature :
   ```go
   func fetchDocument(ctx context.Context, url string) (*goquery.Document, error) {
       client := &http.Client{Timeout: 15 * time.Second}
       req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
       if err != nil {
           return nil, err
       }
       req.Header.Set("User-Agent", "Mozilla/5.0 ...")
       resp, err := client.Do(req)
       if err != nil {
           return nil, err
       }
       defer resp.Body.Close()
       limited := http.MaxBytesReader(nil, resp.Body, 10<<20) // 10 MiB
       return goquery.NewDocumentFromReader(limited)
   }
   ```
2. Propager `ctx` dans les méthodes `Scraper.ScrapeCouncilList`, `Scraper.ScrapePDFLinks`, `Scraper.ScrapeNextCouncilDate` (changer leurs signatures pour accepter `ctx context.Context` en premier argument).
3. Adapter les call sites dans `lambdas/orchestrator/main.go` pour passer le ctx Lambda.

## Test à ajouter

Dans `lambdas/orchestrator/scraper_test.go`, ajouter un test qui appelle `fetchDocument` avec un `ctx` déjà annulé via `context.WithCancel` immédiatement annulé, et vérifie que la fonction retourne une erreur `context.Canceled` rapidement (< 100 ms).

# Contraintes de livraison

- Un seul commit, message :
  ```
  fix(workspace): propagate ctx and add per-call timeouts on external calls
  ```
- Corps de commit listant chaque appel modifié et son timeout.
- **Pas de Co-Authored-By: Claude.**
- Préserver le comportement fonctionnel : aucune des modifications ne doit changer la sémantique métier hors annulation.

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/...` passe.
4. `make build` succès.
5. Le nouveau test scraper (ctx annulé) doit passer en < 1 seconde.

# Format de rapport final

En français :
1. Hash court du commit.
2. Tableau récapitulatif : appel externe → timeout choisi.
3. Résultat de chaque commande de validation (3 lignes).
4. Une seule phrase de clôture.

Procède maintenant.
````

---

# GROUPE 3 — Pagination DynamoDB | Sonnet 4.6

````
# Rôle
Tu es un développeur Go senior expert en DynamoDB. Tu ajoutes la pagination sur des Query et Scan qui peuvent silencieusement tronquer à 1 MiB.

# Contexte projet
`watchdog` interroge DynamoDB via plusieurs Query et Scan. Les lambdas notifier et orchestrator font des appels qui ne bouclent pas sur `LastEvaluatedKey`. Quand la table dépasse 1 MiB sur la page, les résultats sont tronqués sans erreur → données absentes côté front et newsletter. Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Factoriser un helper de pagination dans `lambdas/shared/` et l'appliquer à 3 sites d'appel.

# Lectures préalables obligatoires
1. `lambdas/shared/enums.go` (pour comprendre le layout du paquet `shared`)
2. `lambdas/notifier/handler.go:239-300` (Query + Scan)
3. `lambdas/orchestrator/main.go:113-128` (Query)
4. `lambdas/publisher/handler.go:252-298` (déjà paginé — sert de référence)

# Travail à effectuer

## Étape 1 — Créer le helper

Nouveau fichier `lambdas/shared/ddb.go` :

```go
package shared

import (
    "context"

    "github.com/aws/aws-sdk-go-v2/service/dynamodb"
    "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// DDBQueryAPI est l'interface minimale nécessaire pour PaginateQuery.
type DDBQueryAPI interface {
    Query(ctx context.Context, in *dynamodb.QueryInput, opts ...func(*dynamodb.Options)) (*dynamodb.QueryOutput, error)
}

// DDBScanAPI est l'interface minimale nécessaire pour PaginateScan.
type DDBScanAPI interface {
    Scan(ctx context.Context, in *dynamodb.ScanInput, opts ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
}

// PaginateQuery exécute une Query DynamoDB en bouclant sur LastEvaluatedKey
// et renvoie l'ensemble complet des items.
func PaginateQuery(ctx context.Context, api DDBQueryAPI, in *dynamodb.QueryInput) ([]map[string]types.AttributeValue, error) {
    var all []map[string]types.AttributeValue
    var lastKey map[string]types.AttributeValue
    for {
        if lastKey != nil {
            in.ExclusiveStartKey = lastKey
        }
        out, err := api.Query(ctx, in)
        if err != nil {
            return nil, err
        }
        all = append(all, out.Items...)
        if out.LastEvaluatedKey == nil {
            return all, nil
        }
        lastKey = out.LastEvaluatedKey
    }
}

// PaginateScan exécute un Scan DynamoDB en bouclant sur LastEvaluatedKey.
func PaginateScan(ctx context.Context, api DDBScanAPI, in *dynamodb.ScanInput) ([]map[string]types.AttributeValue, error) {
    var all []map[string]types.AttributeValue
    var lastKey map[string]types.AttributeValue
    for {
        if lastKey != nil {
            in.ExclusiveStartKey = lastKey
        }
        out, err := api.Scan(ctx, in)
        if err != nil {
            return nil, err
        }
        all = append(all, out.Items...)
        if out.LastEvaluatedKey == nil {
            return all, nil
        }
        lastKey = out.LastEvaluatedKey
    }
}

// PaginateScanCount renvoie la somme des Count sur toutes les pages.
// Utile quand le caller fait Select=COUNT pour stats globales.
func PaginateScanCount(ctx context.Context, api DDBScanAPI, in *dynamodb.ScanInput) (int64, error) {
    var total int64
    var lastKey map[string]types.AttributeValue
    for {
        if lastKey != nil {
            in.ExclusiveStartKey = lastKey
        }
        out, err := api.Scan(ctx, in)
        if err != nil {
            return 0, err
        }
        total += int64(out.Count)
        if out.LastEvaluatedKey == nil {
            return total, nil
        }
        lastKey = out.LastEvaluatedKey
    }
}
```

Ajouter un fichier de test `lambdas/shared/ddb_test.go` couvrant : 1 page sans LastEvaluatedKey, 2 pages, propagation d'erreur. Mock minimal de l'interface.

## Étape 2 — Appliquer aux call sites

### 2a. `lambdas/notifier/handler.go:239-256` — `fetchDeliberations`

Remplacer la Query brute par un appel à `shared.PaginateQuery`. Garder l'`UnmarshalListOfMaps` sur le résultat agrégé.

L'interface `dynamoQuerier` du notifier expose déjà `Query`, donc elle satisfait `shared.DDBQueryAPI` (tu peux passer `d.ddb` directement).

### 2b. `lambdas/notifier/handler.go:277-299` — `fetchGlobalStats`

Remplacer les deux `Scan` Select=COUNT par `shared.PaginateScanCount`. La fonction retourne `(int, int)` actuellement ; garder cette signature, juste corriger l'agrégation.

### 2c. `lambdas/orchestrator/main.go:113-127` — query processedSet

Remplacer la Query brute par `shared.PaginateQuery`. Conserver la logique de remplissage du `map[string]bool`.

# Contraintes de livraison

- Un seul commit, message :
  ```
  fix(workspace): paginate DynamoDB Query/Scan to avoid silent truncation
  ```
- Corps : énumérer les 3 sites paginés + l'helper créé.
- **Pas de Co-Authored-By: Claude.**
- Le helper doit rester dans `lambdas/shared/` (paquet `shared`) et être importé via le module `github.com/watchdog/shared`.
- Préserver le comportement quand il y a une seule page (zéro régression sur les tests existants).

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/...` passe, incluant le nouveau test du helper.
4. `make build` succès.

# Format de rapport final

En français :
1. Hash court.
2. Liste des 3 appels paginés et le helper utilisé pour chacun.
3. Résultats des 4 commandes de validation.
4. Une phrase de clôture.

Procède maintenant.
````

---

# GROUPE 4 — Race conditions, counters, S3 atomique | Opus 4.7

````
# Rôle
Tu es ingénieur principal back-end, expert en cohérence transactionnelle DynamoDB et en patterns d'idempotence sur AWS Lambda. Ce travail demande du raisonnement attentif : il s'agit de races et de cohérence de compteur, où chaque branche conditionnelle compte.

# Contexte projet
`watchdog` est un pipeline serverless. Quand une délibération est traitée, le worker Lambda met à jour DynamoDB et incrémente un compteur `processed_pdfs` sur la table `councils`. Quand ce compteur atteint `total_pdfs`, le worker invoque le Publisher Lambda, qui à son tour invoque le Notifier (envoi newsletter). Plusieurs races ont été identifiées :
- deux workers concurrents peuvent passer un check d'idempotence avant qu'aucun n'écrive (race read-then-write) ;
- deux workers peuvent franchir simultanément la frontière processed >= total et invoquer le Publisher deux fois ;
- le Publisher écrit `data.json` directement sur S3 sans atomicité (last-write-wins entre deux invocations concurrentes) ;
- l'Aggregator peut invoquer Publisher sans `council_id`, ce qui rend l'invocation Notifier silencieusement inutile.

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Livrer **5 commits séparés**, chacun corrigeant une race identifiée. Conventional commits, en français.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `lambdas/worker/handler.go` entièrement (lis le 2 fois si nécessaire).
2. `lambdas/publisher/handler.go` entièrement.
3. `lambdas/aggregator/main.go` lignes 50-180.
4. `lambdas/notifier/handler.go` lignes 137-215 (pour comprendre le claim 2-phase déjà en place côté newsletter — c'est le pattern à imiter pour `published_at`).
5. `cdk/watchdog_stack.py` pour vérifier si le bucket S3 a le versioning activé (cherche `versioned=` ou `IBucket.versioned`).
6. `git log --oneline -20` pour le style des commits.

# Commits attendus

## Commit 1 — `fix(worker): drop pre-check race in idempotence path`

**Fichier** : `lambdas/worker/handler.go:60-77` + `:102-198` (la fonction `handleRecord`).

**Problème** : la séquence GetItem (idempotence check) → PutItem ConditionalCheck crée une fenêtre de race. Deux workers concurrents sur la même `id` peuvent tous les deux passer le GetItem (qui ne voit rien) avant que l'un d'eux ait écrit. L'un gagne, l'autre tombe sur ConditionalCheckFailed et part dans la branche "duplicate" — qui aujourd'hui met à jour `budget_*` mais **n'incrémente pas le compteur**. Résultat : le compteur reste sous-évalué pour toujours, le council reste figé "en cours", le Publisher n'est jamais invoqué.

**Refactor attendu** :

1. Supprimer entièrement le bloc `GetItem` initial (lignes 60-77).
2. Sur ConditionalCheckFailedException (à la suite du PutItem), faire un GetItem **post-fail** pour décider :
   - si l'item a déjà un attribut `analysis_data` non vide → c'est un vrai duplicate, log et `continue`.
   - sinon → c'est un état partiel (probablement le worker précédent a crashé entre PutItem et l'incrément). Reprendre la suite : UpdateItem qui set tous les champs analysis avec `ConditionExpression: attribute_not_exists(analysis_data)`. Si cette UpdateItem échoue à son tour par CCF, c'est qu'un troisième worker a complété entre-temps → log et `continue` sans incrémenter le compteur (voir commit 3 pour la règle d'incrémentation).
3. Utiliser `errors.As` typé (cf. groupe 1, à coordonner si déjà mergé).
4. Conserver la sémantique : `isNewInsert = true` uniquement si on a réellement gagné le PutItem initial.

Ajouter un test `lambdas/worker/handler_test.go` qui simule deux invocations concurrentes via `sync.WaitGroup` + mock DDB qui partage un état → vérifier qu'à la fin le compteur a été incrémenté exactement une fois et que `analysis_data` est présent.

## Commit 2 — `fix(worker): atomic completion guard on processed_pdfs`

**Fichier** : `lambdas/worker/handler.go:168-192`.

**Problème** : deux workers traitant les deux derniers PDFs d'un council peuvent tous les deux incrémenter le compteur, tous les deux voir `processed >= total`, et tous les deux invoquer le Publisher. Le Notifier dédoublonne, mais le Publisher lui ne dédoublonne pas → double scan complet DynamoDB + double write S3.

**Refactor attendu** :

1. Ajouter un attribut `published_at` (string RFC 3339) sur la table `councils`. Il sera posé lors du premier franchissement de la frontière.
2. Modifier le `UpdateItem` du compteur :
   ```go
   UpdateExpression: aws.String("ADD processed_pdfs :one"),
   ConditionExpression: aws.String("processed_pdfs < total_pdfs"),
   ```
   Sur ConditionalCheckFailed : log `council already capped`, ne pas invoquer le Publisher.
3. Après l'incrément réussi, si `processed >= total && total > 0`, faire un `UpdateItem` séparé :
   ```go
   UpdateExpression: aws.String("SET published_at = :ts"),
   ConditionExpression: aws.String("attribute_not_exists(published_at)"),
   ExpressionAttributeValues: map[string]types.AttributeValue{
       ":ts": &types.AttributeValueMemberS{Value: time.Now().UTC().Format(time.RFC3339)},
   },
   ```
   Seul le gagnant invoque le Publisher.
4. Sur la CCF de ce deuxième UpdateItem, log uniquement (le Publisher a déjà été déclenché par un autre worker).

Test : 3 mocks workers concurrents franchissent la frontière → exactement un appel à `invokePublisher`.

## Commit 3 — `fix(worker): make counter increment idempotent per pdf`

**Fichier** : `lambdas/worker/handler.go` (paths "duplicate clean" et "état partiel").

**Problème** : sur le chemin "item existait sans `analysis_data`" introduit au commit 1, il ne faut **pas** ré-incrémenter le compteur (la première tentative l'avait déjà incrémenté ou non, on ne sait pas). Pour résoudre proprement, on découple comptabilité et complétion.

**Refactor attendu** :

1. Introduire un attribut booléen `counted` sur les items `deliberations`. Il est posé `true` exactement quand le compteur du council a été incrémenté pour cette délibération.
2. Modifier le flux :
   - PutItem initial : ne pose pas `counted`.
   - Incrément counter : transactionnel via `TransactWriteItems` (2 actions) — `Update councils SET processed_pdfs += 1` ET `Update deliberations SET counted = true` avec `ConditionExpression: attribute_not_exists(counted)`. Si la transaction échoue par CCF sur `deliberations`, c'est qu'un autre worker a déjà compté → on ignore.
3. Si tu juges la transaction trop coûteuse, alternative acceptable : faire l'incrément counter conditionnel sur `attribute_not_exists(counted_marker_<id>)` au niveau du council, mais cela bloat le council item. **Préfère la transaction.**

Test : invocation simulant un crash entre PutItem et l'incrément, suivie d'une seconde invocation → compteur final = 1, pas 0 ni 2.

## Commit 4 — `fix(publisher): atomic data.json swap`

**Fichier** : `lambdas/publisher/handler.go:218-247`.

**Problème** : `PutObject` direct sur `data.json` autorise deux publishers concurrents à écraser arbitrairement.

**Refactor attendu** :

1. Vérifier d'abord si le bucket a le versioning activé (lis `cdk/watchdog_stack.py`).
2. Indépendamment du versioning, prendre un **verrou DynamoDB courte durée** sur la table `councils` via un item `metadata#publisher_lock` :
   ```go
   lockTTL := time.Now().Add(2 * time.Minute).Unix()
   _, err := ddb.UpdateItem(ctx, &dynamodb.UpdateItemInput{
       TableName: aws.String(os.Getenv("COUNCILS_TABLE")),
       Key: map[string]types.AttributeValue{
           "council_id": &types.AttributeValueMemberS{Value: "metadata#publisher_lock"},
       },
       UpdateExpression: aws.String("SET lock_ttl = :ttl, lock_owner = :owner"),
       ConditionExpression: aws.String("attribute_not_exists(lock_ttl) OR lock_ttl < :now"),
       ExpressionAttributeValues: map[string]types.AttributeValue{
           ":ttl":   &types.AttributeValueMemberN{Value: strconv.FormatInt(lockTTL, 10)},
           ":now":   &types.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Unix(), 10)},
           ":owner": &types.AttributeValueMemberS{Value: lambdaRequestID(ctx)},
       },
   })
   ```
   Sur CCF : un autre publisher tourne, log et `return nil`. Pas d'erreur (l'autre instance se chargera de publier l'état le plus récent).
3. Une fois le verrou pris, exécuter le PutObject S3 normalement, puis libérer le verrou (`UpdateItem REMOVE lock_ttl, lock_owner` avec `ConditionExpression: lock_owner = :owner` pour éviter de libérer un verrou pris par quelqu'un d'autre après expiration).
4. Helper `lambdaRequestID(ctx)` : extraire via `lambdacontext.FromContext(ctx)` (paquet `github.com/aws/aws-lambda-go/lambdacontext`). Fallback UUID si absent.

## Commit 5 — `fix(publisher): bail out and emit metric on missing council_id`

**Fichier** : `lambdas/publisher/handler.go:233-247`.

**Refactor attendu** :

1. Si `event.CouncilID == ""` :
   ```go
   log.Printf("notifier skip: empty council_id (probably invoked by aggregator without payload)")
   return nil
   ```
   Mettre ce check juste avant le bloc `if fn := os.Getenv("NOTIFIER_FUNCTION_NAME"); fn != "" { ... }`.
2. Sur erreur d'invocation du Notifier, émettre une métrique EMF embedded log :
   ```go
   log.Printf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Watchdog","Dimensions":[["FunctionName"]],"Metrics":[{"Name":"NotifierInvokeFailed","Unit":"Count"}]}]},"FunctionName":"publisher","NotifierInvokeFailed":1}`,
       time.Now().UnixMilli())
   ```
   (Format Embedded Metric Format de CloudWatch.)

# Contraintes de livraison

- 5 commits **distincts**, dans l'ordre listé.
- Messages conventionnels avec corps explicatif (3-5 lignes : pourquoi le bug existait, quel comportement prod il causait, comment la correction l'élimine).
- **Pas de Co-Authored-By: Claude.**
- Tu peux introduire des dépendances déjà présentes (`github.com/aws/aws-lambda-go/lambdacontext` est déjà transitivement présent via `aws-lambda-go`).
- Pas de changement d'infra côté CDK (sauf si tu juges absolument nécessaire de marquer `published_at` ou `counted` dans un GSI — auquel cas demande confirmation avant).

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/...` passe, incluant les 3 nouveaux tests (commits 1, 2, 3).
4. `make build` succès.

# Format de rapport final

En français :
1. 5 hashes courts dans l'ordre.
2. Pour chaque commit : 1 phrase rappelant la race corrigée et l'invariant garanti après.
3. Résultats des 4 commandes de validation.
4. Question si ambiguïté bloquante. Sinon une seule phrase de clôture.

Si à un moment tu as un doute sur un choix de design (par exemple : faut-il vraiment une transaction au commit 3 ou un simple Update suffit-il), **pose la question avant de continuer** plutôt que deviner.

Procède maintenant.
````

---

# GROUPE 5 — Gemini hardening (schema + retry + circuit breaker) | Opus 4.7

````
# Rôle
Tu es ingénieur principal back-end, spécialisé en intégration LLM en production. Tu durcis tous les appels à l'API Gemini contre les pannes, la dérive de format et les emballements coûts.

# Contexte projet
`watchdog` appelle Gemini depuis 3 lambdas distinctes :
- `worker` : analyse d'un PDF → JSON structuré (a déjà ResponseSchema).
- `aggregator` : synthèse texte libre (pas de schema).
- `notifier` : génération newsletter JSON (pas de schema).

Aucun de ces appels n'a de retry sur 429/5xx, ni de circuit breaker. Une panne Gemini cascade en cascade d'échecs Lambda → SQS retry → quota saturé → blocage du pipeline.

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Livrer **3 commits** :
1. Imposer un ResponseSchema strict sur le notifier.
2. Ajouter un retry exponentiel partagé sur les 3 sites d'appel.
3. Ajouter un circuit breaker DynamoDB-backed pour court-circuiter Gemini quand il est en panne prolongée.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `lambdas/worker/gemini.go` entièrement (c'est la référence du pattern ResponseSchema).
2. `lambdas/notifier/handler.go:303-388` (génération newsletter).
3. `lambdas/aggregator/main.go:236-270` (synthèse).
4. `lambdas/shared/enums.go`.
5. La documentation du SDK `google.golang.org/genai` : type `Schema`, `GenerateContentConfig`, et le format d'erreur retourné par `GenerateContent` (cherche le code via `go doc google.golang.org/genai.Error` si présent).

# Commit 1 — `feat(notifier): enforce Gemini ResponseSchema on newsletter generation`

**Fichier** : `lambdas/notifier/handler.go:303-388`.

**Travail** :

1. Définir un schéma `newsletterSchema *genai.Schema` modélisant exactement `NewsletterParams` :
   - Tous les champs scalaires (`email_subject`, `council_title`, `council_date`, `main_issue`, `budget_total`, `has_global_budget`, `vote_climat`, `climat_color`, `vote_stats`, `total_delibs_in_council`, `next_meeting`, `website_url`, `total_councils`, `total_delibs`) avec le type Gemini approprié.
   - `tensions` : array d'objets avec propriétés (`title`, `context`, `impact`, `budget`, `has_budget`, `vote_details`).
   - `adopted` : array d'objets avec propriétés (`tag` enum sur les 10 valeurs de `shared.TopicTags`, `title`, `context`, `impact`, `budget`, `has_budget`).
   - `briefs` : array d'objets avec (`tag` enum, `summary`).
   - `PropertyOrdering` explicite sur l'objet racine et sur chaque sous-objet.
   - `Required` sur les champs absolument nécessaires (`email_subject`, `council_title`, `council_date`, `main_issue`, `tensions`, `adopted`, `briefs`).
2. Brancher `ResponseSchema: newsletterSchema` dans le `GenerateContentConfig` ligne 322-326.
3. Conserver les post-traitements existants (`stripLinks`, `formatStr`, etc.) — ils restent utiles pour les sorties édulcorées.
4. Ajouter un test `lambdas/notifier/handler_test.go` qui passe un fixture council via `parseNewsletterParams` et vérifie qu'un JSON conforme au schéma est correctement unmarshallé. (Le test ne fait pas d'appel réseau ; il valide juste que le parseur accepte ce que le schéma autorise.)

# Commit 2 — `feat(workspace): exponential backoff retry on Gemini calls`

**Travail** :

1. Créer `lambdas/shared/gemini_retry.go` :
   ```go
   package shared

   import (
       "context"
       "fmt"
       "math/rand"
       "strings"
       "time"

       "google.golang.org/genai"
   )

   // GeminiCall est la signature d'une lambda enveloppant un appel
   // GenerateContent. Le wrapper applique le retry exponentiel.
   type GeminiCall func(ctx context.Context) (*genai.GenerateContentResponse, error)

   // CallGeminiWithRetry réessaie un appel Gemini avec backoff exponentiel
   // (1s, 2s, 4s, 8s, 16s, cap 16s) + jitter ±20%.
   // Réessais uniquement sur 429, 500, 502, 503, 504 et timeouts réseau.
   func CallGeminiWithRetry(ctx context.Context, call GeminiCall, maxAttempts int) (*genai.GenerateContentResponse, error) {
       if maxAttempts <= 0 {
           maxAttempts = 4
       }
       var lastErr error
       for attempt := 0; attempt < maxAttempts; attempt++ {
           resp, err := call(ctx)
           if err == nil {
               return resp, nil
           }
           lastErr = err
           if !isRetriable(err) {
               return nil, err
           }
           if attempt == maxAttempts-1 {
               break
           }
           wait := backoff(attempt)
           select {
           case <-time.After(wait):
           case <-ctx.Done():
               return nil, ctx.Err()
           }
       }
       return nil, fmt.Errorf("gemini retry exhausted after %d attempts: %w", maxAttempts, lastErr)
   }

   func isRetriable(err error) bool {
       msg := err.Error()
       for _, sig := range []string{"429", "500", "502", "503", "504", "deadline exceeded", "connection reset"} {
           if strings.Contains(msg, sig) {
               return true
           }
       }
       return false
   }

   func backoff(attempt int) time.Duration {
       base := time.Second * time.Duration(1<<attempt)
       if base > 16*time.Second {
           base = 16 * time.Second
       }
       jitter := time.Duration(rand.Int63n(int64(base) / 5)) // ±20% one-sided
       return base + jitter
   }
   ```

2. Test `lambdas/shared/gemini_retry_test.go` : 3 erreurs 429 puis succès → 4 appels totaux, retour OK ; erreur non-retriable → 1 seul appel, erreur immédiate ; ctx annulé pendant le wait → retour `context.Canceled`.

3. Brancher dans :
   - `lambdas/worker/gemini.go:204` — wrapper l'appel `client.Models.GenerateContent`.
   - `lambdas/aggregator/main.go:257` — idem.
   - `lambdas/notifier/handler.go:316` — idem.

   Exemple d'intégration dans worker :
   ```go
   resp, err := shared.CallGeminiWithRetry(ctx, func(ctx context.Context) (*genai.GenerateContentResponse, error) {
       return client.Models.GenerateContent(ctx, modelName, contents, &genai.GenerateContentConfig{
           Temperature:      ptrFloat32(0),
           ResponseMIMEType: "application/json",
           ResponseSchema:   deliberationSchema,
           MaxOutputTokens:  ptrInt32(8192),
       })
   }, 4)
   ```

# Commit 3 — `feat(shared): Gemini circuit breaker backed by DynamoDB`

**Travail** :

1. Créer `lambdas/shared/circuit.go` :
   ```go
   package shared

   import (
       "context"
       "strconv"
       "time"

       "github.com/aws/aws-sdk-go-v2/aws"
       "github.com/aws/aws-sdk-go-v2/service/dynamodb"
       "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
   )

   const circuitItemKey = "metadata#gemini_circuit"
   const errorThreshold = 10
   const windowDuration = 5 * time.Minute
   const openDuration = 10 * time.Minute

   type CircuitAPI interface {
       GetItem(ctx context.Context, in *dynamodb.GetItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
       UpdateItem(ctx context.Context, in *dynamodb.UpdateItemInput, opts ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
   }

   // GeminiCircuitOpen renvoie true si l'on doit court-circuiter Gemini.
   func GeminiCircuitOpen(ctx context.Context, api CircuitAPI, table string) (bool, error) {
       out, err := api.GetItem(ctx, &dynamodb.GetItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: circuitItemKey},
           },
       })
       if err != nil || out.Item == nil {
           return false, err
       }
       openUntilAttr, ok := out.Item["open_until"].(*types.AttributeValueMemberN)
       if !ok {
           return false, nil
       }
       openUntil, _ := strconv.ParseInt(openUntilAttr.Value, 10, 64)
       return time.Now().Unix() < openUntil, nil
   }

   // RecordGeminiError incrémente le compteur d'erreurs et ouvre le circuit
   // si le seuil est atteint dans la fenêtre courante.
   func RecordGeminiError(ctx context.Context, api CircuitAPI, table string) error {
       now := time.Now().Unix()
       windowEnd := now + int64(windowDuration.Seconds())
       openUntil := now + int64(openDuration.Seconds())

       // Étape 1 : incrémenter le compteur (réinit si fenêtre expirée).
       _, err := api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: circuitItemKey},
           },
           UpdateExpression: aws.String(`
               SET error_count = if_not_exists(error_count, :zero) + :one,
                   window_end = if_not_exists(window_end, :wend)
           `),
           ExpressionAttributeValues: map[string]types.AttributeValue{
               ":zero": &types.AttributeValueMemberN{Value: "0"},
               ":one":  &types.AttributeValueMemberN{Value: "1"},
               ":wend": &types.AttributeValueMemberN{Value: strconv.FormatInt(windowEnd, 10)},
           },
       })
       if err != nil {
           return err
       }

       // Étape 2 : si le seuil est franchi, ouvrir le circuit.
       _, err = api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: circuitItemKey},
           },
           UpdateExpression: aws.String("SET open_until = :ou"),
           ConditionExpression: aws.String("error_count >= :thresh AND attribute_not_exists(open_until)"),
           ExpressionAttributeValues: map[string]types.AttributeValue{
               ":ou":     &types.AttributeValueMemberN{Value: strconv.FormatInt(openUntil, 10)},
               ":thresh": &types.AttributeValueMemberN{Value: strconv.Itoa(errorThreshold)},
           },
       })
       // CCF ici = seuil pas encore atteint OU circuit déjà ouvert → ignorer.
       return nil
   }

   // RecordGeminiSuccess réinitialise le compteur si la fenêtre est expirée.
   func RecordGeminiSuccess(ctx context.Context, api CircuitAPI, table string) error {
       now := time.Now().Unix()
       _, err := api.UpdateItem(ctx, &dynamodb.UpdateItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: circuitItemKey},
           },
           UpdateExpression: aws.String("REMOVE error_count, window_end, open_until"),
           ConditionExpression: aws.String("attribute_exists(window_end) AND window_end < :now"),
           ExpressionAttributeValues: map[string]types.AttributeValue{
               ":now": &types.AttributeValueMemberN{Value: strconv.FormatInt(now, 10)},
           },
       })
       return nil // ignorer CCF
   }
   ```
2. Brancher :
   - `worker/handler.go` en tête de la boucle SQS : si `GeminiCircuitOpen` → ajouter tous les records restants à `BatchItemFailure` (retry SQS plus tard).
   - `notifier/handler.go` en tête de `handle()` : si ouvert → `log` + `return nil` (la newsletter sera relancée par un schedule futur).
   - `aggregator/main.go` en tête de `runSynthesis` : si ouvert → fallback string + skip Gemini.
   - Après chaque appel à `CallGeminiWithRetry` : sur succès → `RecordGeminiSuccess` ; sur erreur retournée non-nil → `RecordGeminiError`.

3. Test : injecter 11 erreurs simulées via mock DDB → la 12ᵉ vérification doit retourner `open=true`.

# Contraintes de livraison

- 3 commits dans l'ordre listé (commit 2 dépend de l'helper créé en 2, commit 3 dépend de 2 pour récupérer la signature d'erreur retriable).
- Messages conventionnels avec corps détaillé.
- **Pas de Co-Authored-By: Claude.**
- L'item DynamoDB `metadata#gemini_circuit` est stocké dans la table `councils` (même pattern que `metadata#next_council`).

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/...` passe, incluant 3 nouveaux tests (un par commit).
4. `make build` succès.

# Format de rapport final

En français :
1. 3 hashes courts.
2. Pour chaque commit : 1 phrase sur la garantie ajoutée.
3. Résultats des 4 commandes.
4. Une phrase de clôture.

Procède maintenant.
````

---

# GROUPE 6 — Brevo idempotency | Opus 4.7

````
# Rôle
Tu es ingénieur back-end senior. Tu sécurises l'envoi de newsletters via l'API Brevo contre tout double envoi accidentel.

# Contexte projet
Le notifier `watchdog` crée une campagne Brevo via `POST /v3/emailCampaigns`, puis déclenche son envoi via `POST /v3/emailCampaigns/{id}/sendNow`. Le client HTTP du SDK peut retenter en cas de timeout réseau côté client alors que le serveur a déjà accepté la requête → deux campagnes créées et envoyées. Le claim DynamoDB ajouté précédemment (champ `newsletter_sent_at`) bloque le re-déclenchement par re-livraison Lambda, mais pas le retry HTTP interne.

**Pré-requis impératif** : le commit `fix(notifier): two-phase newsletter claim` (séparation `newsletter_pending_at` / `newsletter_sent_at`, plan BUG #4 du bloc critiques) doit déjà être appliqué. Si ce n'est pas le cas, arrête immédiatement et signale-le.

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Un seul commit : `fix(notifier): guarantee single Brevo send via deterministic campaign name`.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `lambdas/notifier/handler.go:638-727` (fonctions `sendCampaign`, `createCampaign`, `triggerSend`).
2. La documentation Brevo (via WebFetch ou Context7 si besoin) sur :
   - `GET /v3/emailCampaigns` (filtres `status`, `limit`, `offset`).
   - `GET /v3/emailCampaigns/{id}` (statuts retournés : `draft`, `sent`, `queued`, `archive`, etc.).
   - Comportement sur `name` dupliqué (Brevo accepte ou rejette ?).
   - Support du header `Idempotency-Key` (à vérifier).

# Travail à effectuer

## Étape 1 — Nom de campagne déterministe

Remplacer ligne 659 :
```go
"name": fmt.Sprintf("Newsletter - %s", params.CouncilDate),
```
par :
```go
"name": fmt.Sprintf("Newsletter-%s-%s", councilID, council.Date),
```

Pour cela, propager `councilID` (et au besoin `council.Date`) en argument :
- `sendCampaign(ctx context.Context, params *NewsletterParams, councilID, councilDate string)`.
- `createCampaign(ctx context.Context, params *NewsletterParams, name string)` — accepte directement le nom déjà construit.

Adapter le call site dans `handle()` (autour de la ligne 209).

## Étape 2 — Lookup avant création

Avant `createCampaign`, ajouter une méthode `lookupCampaign(ctx, name string) (campaignID int, status string, err error)` qui fait :
```
GET /v3/emailCampaigns?limit=50&offset=0
```
filtre côté Go sur le `name` exact, retourne le premier match. Brevo n'autorise pas de filtre `name` côté API, donc le filtrage est local.

Comportements :
- match avec status `sent`, `queued`, `in_process` → retourner `(id, status, nil)` → ne pas créer de nouvelle campagne, ne pas déclencher sendNow (déjà fait).
- match avec status `draft` → réutiliser cet ID pour le sendNow (skip la création).
- aucun match → créer normalement.

## Étape 3 — Header Idempotency-Key

Sur les deux requêtes `POST /v3/emailCampaigns` et `POST /v3/emailCampaigns/{id}/sendNow`, ajouter :
```go
import "crypto/sha256"
import "encoding/hex"

sum := sha256.Sum256([]byte(councilID + "|" + councilDate))
idemKey := hex.EncodeToString(sum[:16])
req.Header.Set("Idempotency-Key", idemKey)
```
Documenter dans le commit que si Brevo ignore ce header, c'est sans impact négatif.

## Étape 4 — Pré-check du statut avant sendNow

Modifier `triggerSend(ctx, campaignID)` : avant le POST sendNow, faire un `GET /v3/emailCampaigns/{id}`. Si `status == "sent" || status == "queued" || status == "in_process"`, log et `return nil`. Sinon, procéder.

## Étape 5 — Tests

Ajouter dans `lambdas/notifier/handler_test.go` :

1. `TestSendCampaign_SkipsWhenAlreadySent` : mock httpClient renvoie pour `GET /emailCampaigns` une liste contenant le nom déterministe avec status `sent` → la fonction ne POST jamais sur `/emailCampaigns` ni `/sendNow`.

2. `TestSendCampaign_ReusesDraft` : mock renvoie un draft existant → un seul POST `/sendNow` est observé, zéro POST `/emailCampaigns`.

3. `TestTriggerSend_SkipsIfAlreadyQueued` : mock `GET /emailCampaigns/{id}` retourne status `queued` → pas de POST sendNow.

Utilise un mock simple `type fakeHTTP struct { ... }` qui matche sur URL prefix.

# Contraintes de livraison

- Un seul commit, message :
  ```
  fix(notifier): guarantee single Brevo send via deterministic campaign name
  ```
  Corps : 5-6 lignes expliquant le double-send observé en théorie (timeout client + retry transparent), comment le lookup déterministe le bloque, et le rôle du header Idempotency-Key.
- **Pas de Co-Authored-By: Claude.**
- Pas d'ajout de dépendance.
- Ne pas casser le mode `TestListID` (ce mode contourne le claim mais doit garder son comportement actuel).

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/notifier/...` passe, incluant les 3 nouveaux tests.
4. `make build` succès.

# Format de rapport final

En français :
1. Hash court.
2. Résumé des 4 garde-fous ajoutés (nom déterministe, lookup, header, statut pré-check).
3. Résultats des 4 commandes.
4. Une phrase de clôture.

Si tu doutes du comportement Brevo (par exemple sur le header Idempotency-Key ou la duplication de nom), pose la question avant d'écrire le code.

Procède maintenant.
````

---

# GROUPE 7 — Orchestrator resilience + SQS batch | Sonnet 4.6

````
# Rôle
Tu es développeur Go senior, expérimenté en patterns Lambda tolérants aux erreurs partielles.

# Contexte projet
La Lambda `orchestrator` scrape le site mairie-begles.fr quotidiennement (déclenchée par EventBridge). Pour chaque council découvert, elle GetItem en DynamoDB, scrape les PDFs, met à jour le council, et envoie un message SQS par PDF. Aujourd'hui, une seule erreur (par exemple GetItem fail sur le 1er council) `return` immédiatement et avorte tout le cycle quotidien. De plus, l'envoi SQS est fait message par message (1 round-trip par PDF, ~30 round-trips par council).

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Un seul commit : `fix(orchestrator): isolate per-council errors and batch SQS sends`.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `lambdas/orchestrator/main.go` entièrement.
2. `lambdas/orchestrator/scraper.go`.
3. La doc AWS SDK Go v2 sur `sqs.SendMessageBatch` : signature, contrainte 10 messages max, format `BatchResultErrorEntry`.

# Travail à effectuer

## Étape 1 — Isoler les erreurs par council

Refactor `handler()` :

1. Remplacer chaque `return fmt.Errorf(...)` à l'intérieur de la boucle `for _, council := range listings` par :
   - `log.Printf("error processing council %s: %v", council.CouncilID, err)`
   - `errs = append(errs, fmt.Errorf("council %s: %w", council.CouncilID, err))`
   - `continue`

   Pour cela, déclarer `var errs []error` en début de fonction.

2. Lignes 65-73 (GetItem) : sur erreur, **retry 1 fois** avec un backoff de 500 ms avant de basculer en `errs`. Helper local :
   ```go
   func getCouncilWithRetry(ctx context.Context, ddb *dynamodb.Client, table, councilID string) (*dynamodb.GetItemOutput, error) {
       out, err := ddb.GetItem(ctx, &dynamodb.GetItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: councilID},
           },
       })
       if err == nil {
           return out, nil
       }
       time.Sleep(500 * time.Millisecond)
       return ddb.GetItem(ctx, &dynamodb.GetItemInput{
           TableName: aws.String(table),
           Key: map[string]types.AttributeValue{
               "council_id": &types.AttributeValueMemberS{Value: councilID},
           },
       })
   }
   ```

3. En fin de handler, après la boucle :
   ```go
   if len(errs) > 0 {
       log.Printf("orchestrator finished with %d council errors (continued best-effort):", len(errs))
       for _, e := range errs {
           log.Printf("  - %v", e)
       }
   }
   return nil
   ```
   La Lambda retourne `nil` : EventBridge la rejouera demain naturellement, pas la peine d'invalider tout le cycle.

4. Compteurs invalides (lignes 79-82) : remplacer le `continue` muet par une métrique EMF :
   ```go
   log.Printf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Watchdog","Dimensions":[["FunctionName"]],"Metrics":[{"Name":"CouncilCountersInvalid","Unit":"Count"}]}]},"FunctionName":"orchestrator","CouncilCountersInvalid":1,"CouncilID":"%s"}`,
       time.Now().UnixMilli(), council.CouncilID)
   continue
   ```

## Étape 2 — SQS batch

Remplacer la boucle `for _, pdf := range pdfs` (lignes 155-176) par un envoi en batches de 10 :

```go
type pendingMsg struct {
    id   string // pour le batch entry
    body string
}
var pending []pendingMsg
for _, pdf := range pdfs {
    if processedSet[deliberationID(pdf.URL)] {
        continue
    }
    msg := SQSMessage{
        CouncilID: council.CouncilID,
        PDFTitle:  pdf.Title,
        PDFURL:    pdf.URL,
        TotalPDFs: len(pdfs),
    }
    body, _ := json.Marshal(msg)
    pending = append(pending, pendingMsg{
        id:   deliberationID(pdf.URL),
        body: string(body),
    })
}

queueURL := os.Getenv("PDF_QUEUE_URL")
queuedCount := 0
for i := 0; i < len(pending); i += 10 {
    end := i + 10
    if end > len(pending) {
        end = len(pending)
    }
    entries := make([]sqstypes.SendMessageBatchRequestEntry, 0, end-i)
    for j, m := range pending[i:end] {
        entries = append(entries, sqstypes.SendMessageBatchRequestEntry{
            Id:          aws.String(fmt.Sprintf("%d", i+j)),
            MessageBody: aws.String(m.body),
        })
    }
    out, err := sqsClient.SendMessageBatch(ctx, &sqs.SendMessageBatchInput{
        QueueUrl: aws.String(queueURL),
        Entries:  entries,
    })
    if err != nil {
        log.Printf("error sending SQS batch (council %s): %v", council.CouncilID, err)
        continue
    }
    queuedCount += len(out.Successful)
    for _, f := range out.Failed {
        log.Printf("warn: SQS batch entry %s failed: code=%s msg=%s", aws.ToString(f.Id), aws.ToString(f.Code), aws.ToString(f.Message))
    }
}
log.Printf("Queued %d new PDFs for council %s (already processed: %d/%d)", queuedCount, council.Title, len(processedSet), len(pdfs))
```

Ajouter l'import nécessaire :
```go
sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
```

## Étape 3 — Test

Ajouter un test dans `lambdas/orchestrator/scraper_test.go` ou un nouveau fichier `lambdas/orchestrator/main_test.go` :

- Mock DDB qui retourne une erreur sur le 2ᵉ council d'une liste de 3 → vérifier que les councils 1 et 3 sont bien traités (via PutItem mock observable). Mocker aussi sqsClient pour ne pas dépendre du réseau.

Si l'architecture actuelle ne facilite pas le mock (handler accède directement à `os.Getenv` et crée le client AWS dans la fonction), refactor minimal : extraire un struct `orchestrator` avec les clients en champs, comme `WorkerHandler` dans le worker. Le `main` instancie et appelle `o.handle(ctx, event)`. Cela vaut le coup même si ça ajoute ~30 lignes.

# Contraintes de livraison

- Un seul commit, message :
  ```
  fix(orchestrator): isolate per-council errors and batch SQS sends
  ```
  Corps : impact prod observé (cycle quotidien tué par une erreur, ~30× round-trips SQS), correctif (best-effort par council, batches de 10).
- **Pas de Co-Authored-By: Claude.**

# Critères d'acceptation

1. `gofmt -l lambdas/orchestrator/` vide.
2. `go vet ./lambdas/orchestrator/...` aucune sortie.
3. `go test ./lambdas/orchestrator/...` passe.
4. `make build` succès.

# Format de rapport final

En français :
1. Hash court.
2. Avant/après : `N` appels SQS pour `K` PDFs.
3. Résultats des 4 commandes.
4. Une phrase de clôture.

Procède maintenant.
````

---

# GROUPE 8 — Cold start + PII logs + enum normalize | Sonnet 4.6

````
# Rôle
Tu es ingénieur back-end senior. Tu améliores la performance cold start, le respect RGPD côté logs, et la robustesse des comparaisons d'enums.

# Contexte projet
`watchdog` recrée tous ses clients AWS à chaque invocation Lambda (latence cold start évitable). Certains logs contiennent du contenu de désaccords politiques et des noms cités dans les délibérations (surface RGPD). Les comparaisons d'enums sont strict-case (`"DÉPENSE"` exact requis), fragiles face à un drift Gemini sur la casse ou les accents.

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Livrer **3 commits**, dans l'ordre listé.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `lambdas/worker/main.go`, `lambdas/notifier/main.go`, `lambdas/publisher/main.go`, `lambdas/aggregator/main.go`, `lambdas/orchestrator/main.go` (pour voir où sont initialisés les clients).
2. `lambdas/worker/gemini.go` (validation d'enums actuelle, lignes 271-303).
3. `lambdas/shared/enums.go`.
4. `cdk/watchdog_stack.py` (pour la modification log retention).

# Commit 1 — `perf(workspace): reuse AWS clients across invocations`

**Travail** :

Pour chaque Lambda, déplacer la création des clients AWS hors du handler dans des `var` package-level initialisées par `init()`.

### Worker

`lambdas/worker/main.go` :
```go
package main

import (
    "context"
    "log"

    "github.com/aws/aws-lambda-go/lambda"
    "github.com/aws/aws-sdk-go-v2/config"
    "github.com/aws/aws-sdk-go-v2/service/dynamodb"
    awslambda "github.com/aws/aws-sdk-go-v2/service/lambda"
)

var workerHandler *WorkerHandler

func init() {
    cfg, err := config.LoadDefaultConfig(context.Background())
    if err != nil {
        log.Fatalf("init: load aws config: %v", err)
    }
    workerHandler = &WorkerHandler{
        ddb:    dynamodb.NewFromConfig(cfg),
        lambda: awslambda.NewFromConfig(cfg),
    }
}

func main() {
    lambda.Start(workerHandler.HandleRequest)
}
```

### Notifier

`lambdas/notifier/main.go` : créer une variable package-level `sharedDeps *notifierDeps` initialisée dans `init()` avec les valeurs des variables d'environnement. Modifier `HandleRequest` pour utiliser `sharedDeps` au lieu de re-créer à chaque invocation. Important : les tests doivent rester capables d'injecter leur propre `notifierDeps`, donc garder la fonction `handle` instance-side et juste éviter la duplication de config.

### Publisher

`lambdas/publisher/main.go` : extraire `var (cfg aws.Config; ddb *dynamodb.Client; s3Client *s3.Client; lambdaClient *lambdaSvc.Client)` initialisés en `init()`. Adapter `HandleRequest` pour utiliser ces vars au lieu de re-charger.

### Aggregator

`lambdas/aggregator/main.go` : idem, extraire `ddb`, `lambdaClient` en globaux.

### Orchestrator

`lambdas/orchestrator/main.go` : extraire `ddb`, `sqsClient` en globaux.

### Tests

Vérifier que `go test ./lambdas/...` passe encore. Les tests qui instancient leurs propres mocks ne devraient pas être affectés tant que tu ne touches pas aux structs handler.

## Commit 2 — `chore(notifier,worker): scrub PII from CloudWatch logs`

**Travail** :

1. Ajouter `lambdas/shared/log.go` :
   ```go
   package shared

   import "unicode/utf8"

   // TruncForLog tronque une chaîne à n octets utf-8 valides, en suffixant
   // "..." si elle a été coupée. Sûr pour les chaînes contenant des accents.
   func TruncForLog(s string, n int) string {
       if len(s) <= n {
           return s
       }
       // Reculer pour ne pas couper un rune utf-8.
       trunc := s[:n]
       for !utf8.ValidString(trunc) && len(trunc) > 0 {
           trunc = trunc[:len(trunc)-1]
       }
       return trunc + "..."
   }
   ```

2. Identifier les sites de log à scrubber. Commande indicative :
   ```
   grep -rn "log.Printf" lambdas/ | grep -Ei "(d\.Title|d\.Disagreements|councilTitle|dis :=|\.Title|\.Summary)"
   ```

   Cibles minimales à appliquer :
   - `lambdas/notifier/handler.go:605` (boucle politicalTensions) : tronquer `d.Title` et `dis` à 80 chars chacun.
   - `lambdas/worker/handler.go:74` : tronquer le contenu loggé.
   - `lambdas/worker/handler.go:165` : tronquer.
   - `lambdas/worker/handler.go:189` : pas de PII (council ID + counters), ne pas changer.

   Pattern à appliquer :
   ```go
   log.Printf("- %s | Budget: %d€ | Vote: %d/%d/%d | Désaccords: %s",
       shared.TruncForLog(d.Title, 80),
       d.BudgetImpact, pour, contre, abst,
       shared.TruncForLog(dis, 80))
   ```

3. Modifier `cdk/watchdog_stack.py` : pour chaque Lambda, fixer `log_retention=logs.RetentionDays.TWO_WEEKS` (14 jours). Si l'attribut est déjà défini, l'aligner. Si certaines Lambdas demandent une rétention plus longue (à toi de juger : audit ou conformité), garde-les telles quelles et documente le pourquoi dans le commit.

   Importer si besoin :
   ```python
   from aws_cdk import aws_logs as logs
   ```

## Commit 3 — `fix(shared): tolerate enum casing and accent drift`

**Travail** :

1. Ajouter dans `lambdas/shared/enums.go` (ou nouveau `lambdas/shared/enum_match.go`) :

   ```go
   package shared

   import (
       "strings"

       "golang.org/x/text/runes"
       "golang.org/x/text/transform"
       "golang.org/x/text/unicode/norm"
       "unicode"
   )

   // normalize retire les accents (NFD + Mn drop) et passe en bas de casse.
   func normalize(s string) string {
       t := transform.Chain(norm.NFD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
       out, _, _ := transform.String(t, s)
       return strings.ToLower(out)
   }

   // MatchTopicTag renvoie la forme canonique du tag, ou ("", false)
   // si aucune correspondance.
   func MatchTopicTag(s string) (string, bool) {
       target := normalize(s)
       for _, t := range TopicTags {
           if normalize(t) == target {
               return t, true
           }
       }
       return "", false
   }

   // MatchBudgetType : idem pour BudgetTypes.
   func MatchBudgetType(s string) (string, bool) {
       target := normalize(s)
       for _, t := range BudgetTypes {
           if normalize(t) == target {
               return t, true
           }
       }
       return "", false
   }

   // MatchClimateImpact : idem pour ClimateImpacts.
   func MatchClimateImpact(s string) (string, bool) {
       target := normalize(s)
       for _, t := range ClimateImpacts {
           if normalize(t) == target {
               return t, true
           }
       }
       return "", false
   }
   ```

2. Ajouter au `go.mod` la dépendance `golang.org/x/text` si pas déjà présente.

3. Brancher dans `lambdas/worker/gemini.go:271-303` :
   - Remplacer `if !contains(validTopicTags, r.TopicTag)` par :
     ```go
     canonical, ok := shared.MatchTopicTag(r.TopicTag)
     if !ok {
         return fmt.Errorf("invalid topic_tag %q (must be one of %v)", r.TopicTag, validTopicTags)
     }
     r.TopicTag = canonical
     ```
   - Idem pour `BudgetType` et `ClimateImpact`.
   - Idem pour la boucle `budget_breakdown` (`b.TopicTag`).

4. Test : `lambdas/shared/enum_match_test.go` couvrant `"DEPENSE"` (sans accent), `"depense"`, `"DÉPENSE"`, `"Depense"` → tous matchent `"DÉPENSE"`. `"GIFT"` → no match.

# Contraintes de livraison

- 3 commits dans l'ordre listé.
- **Pas de Co-Authored-By: Claude.**
- L'ajout de `golang.org/x/text` est acceptable (dépendance officielle).

# Critères d'acceptation

1. `gofmt -l lambdas/` vide.
2. `go vet ./lambdas/...` aucune sortie.
3. `go test ./lambdas/...` passe (incluant test enum_match).
4. `make build` succès.
5. `cdk synth` succès (pour valider le changement de log retention).

# Format de rapport final

En français :
1. 3 hashes courts.
2. Cold start avant/après (estimation rapide en ms, basée sur la doc AWS).
3. Liste des sites de log scrubbés.
4. Résultats des 5 commandes.
5. Une phrase de clôture.

Procède maintenant.
````

---

# GROUPE 9 — CDK infra (DLQ alarm + IAM least-privilege) | Sonnet 4.6

````
# Rôle
Tu es ingénieur DevOps/Cloud expérimenté en AWS CDK Python. Tu durcis l'infrastructure : alarme sur DLQ + restriction des permissions IAM des Lambdas au strict nécessaire.

# Contexte projet
Le stack CDK `watchdog` (Python, `cdk/watchdog_stack.py`) provisionne les 5 Lambdas Go, leurs tables DynamoDB, leurs queues SQS, leur bucket S3. La queue DLQ du Notifier (`notifier_dlq`) existe mais aucune alarme CloudWatch ne signale son non-vide. Les Lambdas ont probablement des permissions trop larges (`grant_read_write_data` au lieu d'actions précises).

Working directory : `/Users/jules/Desktop/watchdog`.

# Mission
Un seul commit : `chore(infra): add notifier DLQ alarm and tighten Lambda IAM`.

**Pas de Co-Authored-By: Claude.**

# Lectures préalables obligatoires
1. `cdk/watchdog_stack.py` entièrement (lecture intégrale, sans exception).
2. `cdk.json` et `cdk/app.py` (pour comprendre les paramètres CDK et les environment variables CDK utilisées).
3. La doc CDK Python : `aws_cdk.aws_cloudwatch`, `aws_cdk.aws_sns`, `aws_cdk.aws_iam`, et les `grant_*` granulaires sur `Table` / `Function` / `Bucket`.

# Travail à effectuer

## Étape 1 — Alarme DLQ Notifier

1. Créer (s'il n'existe pas déjà) un `sns.Topic` `notifier_dlq_alarm_topic`. Subscriber email paramétrable :
   ```python
   ops_email = self.node.try_get_context("ops_email")
   if ops_email:
       notifier_dlq_alarm_topic.add_subscription(
           sns_subs.EmailSubscription(ops_email)
       )
   else:
       # Pas de subscriber : le topic existe, on logue une warning dans le synth.
       print("WARN: no ops_email context provided; DLQ alarm topic has no subscriber")
   ```

2. Créer la `cloudwatch.Alarm` sur la metric `ApproximateNumberOfMessagesVisible` de `notifier_dlq` :
   ```python
   cw.Alarm(
       self, "NotifierDLQAlarm",
       metric=notifier_dlq.metric_approximate_number_of_messages_visible(
           period=Duration.minutes(5),
           statistic="Maximum",
       ),
       threshold=0,
       evaluation_periods=1,
       comparison_operator=cw.ComparisonOperator.GREATER_THAN_THRESHOLD,
       treat_missing_data=cw.TreatMissingData.NOT_BREACHING,
       alarm_description="Notifier DLQ non-empty: at least one newsletter failed.",
   ).add_alarm_action(cw_actions.SnsAction(notifier_dlq_alarm_topic))
   ```

   Imports nécessaires (à adapter si déjà présents) :
   ```python
   from aws_cdk import (
       aws_cloudwatch as cw,
       aws_cloudwatch_actions as cw_actions,
       aws_sns as sns,
       aws_sns_subscriptions as sns_subs,
       Duration,
   )
   ```

## Étape 2 — Resserrer IAM Worker

Remplacer les `table.grant_read_write_data(worker_lambda)` (ou équivalent) par des permissions ciblées via `grant`. Inventaire des actions réellement utilisées (vérifie via lecture de `lambdas/worker/handler.go`) :

- `deliberations_table` : `GetItem`, `PutItem`, `UpdateItem`.
- `councils_table` : `UpdateItem` (counter increment + publish flag).
- `publisher_lambda` : `lambda:InvokeFunction`.

Implementation :
```python
deliberations_table.grant(worker_lambda, "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem")
councils_table.grant(worker_lambda, "dynamodb:UpdateItem")
publisher_lambda.grant_invoke(worker_lambda)
```

Si `Table.grant` n'accepte pas ce format dans la version CDK utilisée, utiliser `worker_lambda.add_to_role_policy(iam.PolicyStatement(...))` direct.

## Étape 3 — Resserrer IAM Notifier

Actions réellement utilisées (`lambdas/notifier/handler.go`) :
- `councils_table` : `GetItem`, `UpdateItem`, `Scan`.
- `deliberations_table` : `Query`, `Scan`.

```python
councils_table.grant(notifier_lambda, "dynamodb:GetItem", "dynamodb:UpdateItem", "dynamodb:Scan")
deliberations_table.grant(notifier_lambda, "dynamodb:Query", "dynamodb:Scan")
```

## Étape 4 — Resserrer IAM Aggregator

Actions réellement utilisées (`lambdas/aggregator/main.go`) :
- `councils_table` : `GetItem`, `UpdateItem`.
- `deliberations_table` : `Query`.
- `publisher_lambda` : `lambda:InvokeFunction`.
- Stream consumer : déjà géré par `grant_stream_read` ou équivalent — préserver.

## Étape 5 — Resserrer IAM Orchestrator

Actions réellement utilisées (`lambdas/orchestrator/main.go`) :
- `councils_table` : `GetItem`, `PutItem`, `UpdateItem`.
- `deliberations_table` : `Query`.
- `pdf_queue` : `SendMessage`, `SendMessageBatch`.

```python
councils_table.grant(orchestrator_lambda, "dynamodb:GetItem", "dynamodb:PutItem", "dynamodb:UpdateItem")
deliberations_table.grant(orchestrator_lambda, "dynamodb:Query")
pdf_queue.grant_send_messages(orchestrator_lambda)
```

## Étape 6 — Resserrer IAM Publisher

Actions réellement utilisées (`lambdas/publisher/handler.go`) :
- `councils_table` : `Scan`, `GetItem`, `UpdateItem` (verrou publisher_lock).
- `deliberations_table` : `Scan`.
- `website_bucket` : `s3:PutObject`.
- `notifier_lambda` : `lambda:InvokeFunction`.

```python
councils_table.grant(publisher_lambda, "dynamodb:Scan", "dynamodb:GetItem", "dynamodb:UpdateItem")
deliberations_table.grant(publisher_lambda, "dynamodb:Scan")
website_bucket.grant_put(publisher_lambda)
notifier_lambda.grant_invoke(publisher_lambda)
```

## Étape 7 — Validation

Lancer `cdk synth` et **comparer la diff** des Policy IAM générées avec la version précédente (`cdk diff` si possible). Vérifier qu'aucune action n'a été retirée à tort. Inclure un extrait de la diff dans le commit message s'il est court.

## Étape 8 — Vérification optionnelle Bucket versioning

Si la version actuelle du bucket n'a pas `versioned=True`, activer le versioning :
```python
website_bucket = s3.Bucket(
    self, "WebsiteBucket",
    versioned=True,
    ...
)
```

C'est utile pour le rollback de `data.json` en cas d'incident (corruption silencieuse, scan tronqué non détecté). Documenter dans le commit.

# Contraintes de livraison

- Un seul commit, message :
  ```
  chore(infra): add notifier DLQ alarm and tighten Lambda IAM
  ```
  Corps : enumeration des IAM resserrés et de l'alarme ajoutée.
- **Pas de Co-Authored-By: Claude.**
- Ne pas casser le déploiement existant : `cdk synth` doit passer sans erreur.

# Critères d'acceptation

1. `cdk synth` succès (pas d'exception, pas de warning bloquante).
2. La diff IAM générée ne montre **aucune** action retirée qui était effectivement utilisée par le code Go (validation croisée manuelle).
3. L'alarme CloudWatch apparaît dans le template synthétisé (cherche `AWS::CloudWatch::Alarm`).

# Format de rapport final

En français :
1. Hash court.
2. Tableau Lambda → actions accordées (avant / après).
3. Résultat de `cdk synth` (OK / liste de warnings).
4. Extrait de la diff IAM si court.
5. Une phrase de clôture, ou question si tu as eu un blocage sur la validation croisée.

Procède maintenant.
````
