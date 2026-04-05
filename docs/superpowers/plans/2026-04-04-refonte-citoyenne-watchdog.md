# Refonte Citoyenne de l'Interface — Plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Transformer l'interface technique du Watchdog en un outil de transparence démocratique accessible en remplaçant les badges techniques par des thématiques et en améliorant la lisibilité des votes.

**Architecture:** Mise à jour du pipeline serverless (Go) pour inclure un nouveau champ `topic_tag` généré par Gemini, persistance dans DynamoDB, et rendu dynamique dans le frontend HTML/Tailwind.

**Tech Stack:** Go 1.22, AWS DynamoDB, Gemini 3.1 Pro, Tailwind CSS.

---

### Task 1: Mise à jour du Worker (Prompt et Structure)

**Files:**
- Modify: `lambdas/worker/gemini.go`
- Test: `lambdas/worker/gemini_test.go`

- [ ] **Step 1: Mettre à jour la structure GeminiResult et le prompt**

Modifier `lambdas/worker/gemini.go` pour ajouter `TopicTag` et mettre à jour `deliberationPrompt`.

```go
// Dans lambdas/worker/gemini.go

const deliberationPrompt = `Tu es un journaliste municipal factuel et neutre spécialisé dans les affaires locales.
Analyse ce document PDF de délibération du conseil municipal de Bègles.

Retourne UNIQUEMENT un objet JSON valide avec cette structure exacte :
{
  "title": "titre exact de la délibération tel qu'il figure dans le document",
  "topic_tag": "un seul mot thématique en français (ex: Budget, Urbanisme, Social, Culture, Environnement, Éducation, Sport, Mobilité)",
  "summary": "résumé factuel en 2-3 phrases maximum, accessible à un citoyen non-spécialiste",
  "analysis": "...",
  "key_points": [...],
  "vote": {"pour": <nombre entier>, "contre": <nombre entier>, "abstention": <nombre entier>},
  "disagreements": "description factuelle des désaccords entre majorité et opposition, ou chaîne vide si vote unanime sans opposition"
}
...`

type GeminiResult struct {
	Title         string   `json:"title"`
	TopicTag      string   `json:"topic_tag"` // Nouveau champ
	Summary       string   `json:"summary"`
	Analysis      string   `json:"analysis"`
	KeyPoints     []string `json:"key_points"`
	Vote struct {
		Pour       int `json:"pour"`
		Contre     int `json:"contre"`
		Abstention int `json:"abstention"`
	} `json:"vote"`
	Disagreements string `json:"disagreements"`
}
```

- [ ] **Step 2: Mettre à jour le test de parsing**

Modifier `lambdas/worker/gemini_test.go` pour vérifier le nouveau champ.

```go
func TestParseGeminiResponse(t *testing.T) {
    raw := `{"title": "Test", "topic_tag": "Budget", "summary": "Short summary", "vote": {"pour": 10, "contre": 0, "abstention": 2}}`
    result, err := parseGeminiResponse(raw)
    assert.NoError(t, err)
    assert.Equal(t, "Budget", result.TopicTag)
}
```

- [ ] **Step 3: Vérifier les tests**

Run: `go test ./lambdas/worker/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add lambdas/worker/gemini.go lambdas/worker/gemini_test.go
git commit -m "feat(worker): add topic_tag to Gemini prompt and result structure"
```

---

### Task 2: Mise à jour du Handler Worker (DynamoDB)

**Files:**
- Modify: `lambdas/worker/handler.go`

- [ ] **Step 1: Sauvegarder topic_tag dans DynamoDB**

Modifier `handleRecord` dans `lambdas/worker/handler.go` pour inclure `topic_tag`.

```go
// Dans lambdas/worker/handler.go, fonction handleRecord

item, err := attributevalue.MarshalMap(map[string]interface{}{
    "id":              id,
    "council_id":      msg.CouncilID,
    "title":           result.Title,
    "topic_tag":       result.TopicTag, // Ajout ici
    "pdf_url":         msg.PDFURL,
    "summary":         result.Summary,
    // ... reste inchangé
})
```

- [ ] **Step 2: Vérifier la compilation**

Run: `go build -o /dev/null ./lambdas/worker`
Expected: Pas d'erreur

- [ ] **Step 3: Commit**

```bash
git add lambdas/worker/handler.go
git commit -m "feat(worker): persist topic_tag in DynamoDB"
```

---

### Task 3: Mise à jour du Publisher (Structures et JSON)

**Files:**
- Modify: `lambdas/publisher/handler.go`

- [ ] **Step 1: Mettre à jour les structures DeliberationRecord et DeliberationOutput**

```go
// Dans lambdas/publisher/handler.go

type DeliberationRecord struct {
    // ...
    TopicTag       string `dynamodbav:"topic_tag"` // Ajout
    // ...
}

type DeliberationOutput struct {
    // ...
    TopicTag      string    `json:"topic_tag"` // Ajout
    // ...
}
```

- [ ] **Step 2: Mettre à jour buildDataJSON**

```go
// Dans lambdas/publisher/handler.go, fonction buildDataJSON

for _, d := range delibs[c.CouncilID] {
    co.Deliberations = append(co.Deliberations, DeliberationOutput{
        ID:            d.ID,
        Title:         d.Title,
        TopicTag:      d.TopicTag, // Passage au JSON final
        PDFURL:        d.PDFURL,
        // ...
    })
}
```

- [ ] **Step 3: Vérifier la compilation**

Run: `go build -o /dev/null ./lambdas/publisher`
Expected: Pas d'erreur

- [ ] **Step 4: Commit**

```bash
git add lambdas/publisher/handler.go
git commit -m "feat(publisher): include topic_tag in data.json"
```

---

### Task 4: Refonte du Frontend (HTML/Tailwind)

**Files:**
- Modify: `frontend/index.html`

- [ ] **Step 1: Supprimer les badges techniques et ajouter le TopicTag**

Rechercher le bloc de badges dans `renderDeliberation` et le remplacer.

```javascript
// Remplacer :
// <div class="flex items-center gap-2 mb-4">
//     <span class="px-2.5 py-1 rounded-md ${config.color} text-[10px] font-bold text-white uppercase tracking-wider">
//         ${config.label}
//     </span>
// </div>

// Par :
<div class="flex items-center gap-2 mb-4">
    <span class="px-3 py-1 rounded-full bg-brand-100 text-brand-700 text-[11px] font-bold uppercase tracking-wider">
        ${d.topic_tag || config.label}
    </span>
</div>
```

- [ ] **Step 2: Refonte de la section des votes**

Mettre les chiffres en gros (`text-2xl font-bold`) et affiner la barre.

```javascript
// Remplacer le bloc <!-- Detailed Vote View -->
<div class="space-y-4">
    <div class="flex flex-wrap items-center gap-8 mb-2">
        <div>
            <div class="text-2xl font-bold text-emerald-600">${d.vote.pour}</div>
            <div class="text-[10px] font-bold text-slate-400 uppercase tracking-widest">Pour</div>
        </div>
        <div>
            <div class="text-2xl font-bold text-rose-600">${d.vote.contre}</div>
            <div class="text-[10px] font-bold text-slate-400 uppercase tracking-widest">Contre</div>
        </div>
        <div>
            <div class="text-2xl font-bold text-slate-400">${d.vote.abstention}</div>
            <div class="text-[10px] font-bold text-slate-400 uppercase tracking-widest">Abst.</div>
        </div>
    </div>
    <div class="flex h-1 rounded-full overflow-hidden bg-slate-100">
        <div class="bg-emerald-500 transition-all duration-1000" style="width: ${pourPct}%"></div>
        <div class="bg-rose-500 transition-all duration-1000" style="width: ${contrePct}%"></div>
        <div class="bg-slate-300 transition-all duration-1000" style="width: ${abstentionPct}%"></div>
    </div>
</div>
```

- [ ] **Step 3: Renommer et styliser "Position de l'opposition"**

```javascript
// Remplacer le bloc disagreements
<div class="mt-8 bg-slate-50 border-l-2 border-slate-300 rounded-r-xl p-5">
    <h4 class="text-[11px] font-bold text-slate-900 uppercase tracking-widest mb-2">
        Position de l'opposition
    </h4>
    <p class="text-[14px] text-slate-600 leading-relaxed">
        ${d.disagreements}
    </p>
</div>
```

- [ ] **Step 4: Commit**

```bash
git add frontend/index.html
git commit -m "feat(frontend): citizen-friendly UI refresh"
```

---

### Task 5: Validation finale

- [ ] **Step 1: Build complet**

Run: `make build`
Expected: Tous les zip générés dans dist/

- [ ] **Step 2: Vérification visuelle (Mockup)**

Ouvrir `frontend/index.html` avec des données fictives incluant `topic_tag`.

- [ ] **Step 3: Commit final**

```bash
git commit --allow-empty -m "chore: refonte citoyenne complete"
```
