# Plan d'implémentation : Migration des Secrets vers GitHub Actions

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Supprimer la dépendance à AWS Secrets Manager pour économiser des coûts et injecter la clé Gemini API via GitHub Secrets au moment du déploiement.

**Architecture:** Build-time injection. La clé est passée de GitHub -> CDK -> Lambda Environment Variable.

**Tech Stack:** GitHub Actions (YAML), AWS CDK (Python), Go (Lambda).

---

### Task 1: Update GitHub Workflow

**Files:**
- Modify: `.github/workflows/deploy.yml`

- [ ] **Step 1: Injecter GEMINI_API_KEY dans l'étape de déploiement**

Modifier la section `env` de l'étape `Deploy to AWS`.

- [ ] **Step 2: Commiter le changement**

```bash
git add .github/workflows/deploy.yml
git commit -m "ci: inject GEMINI_API_KEY into CDK deployment"
```

---

### Task 2: Update Infrastructure (CDK)

**Files:**
- Modify: `cdk/watchdog_stack.py`

- [ ] **Step 1: Lire la variable d'environnement et supprimer Secrets Manager**

Modifier `WatchdogStack` pour :
1. Lire `GEMINI_API_KEY` depuis `os.environ`.
2. Supprimer la création de `GeminiSecret`.
3. Ajouter `GEMINI_API_KEY` dans `common_env`.
4. Supprimer `GEMINI_SECRET_ARN` de `common_env`.
5. Supprimer `gemini_secret.grant_read(worker)`.

- [ ] **Step 2: Vérifier la synthèse CDK**

Run: `cd cdk && cdk synth`

- [ ] **Step 3: Commiter le changement**

```bash
git add cdk/watchdog_stack.py
git commit -m "infra: replace Secrets Manager with build-time env injection"
```

---

### Task 3: Update Worker Lambda (Go)

**Files:**
- Modify: `lambdas/worker/handler.go`
- Modify: `lambdas/worker/main.go`

- [ ] **Step 1: Supprimer l'usage de Secrets Manager dans le handler**

Modifier `HandleRequest` pour utiliser `os.Getenv("GEMINI_API_KEY")`.
Nettoyer le struct `WorkerHandler` (supprimer le champ `sm`).

- [ ] **Step 2: Supprimer l'initialisation du client Secrets Manager dans main.go**

- [ ] **Step 3: Vérifier la compilation**

Run: `cd lambdas/worker && go build ./...`

- [ ] **Step 4: Commiter le changement**

```bash
git add lambdas/worker/
git commit -m "feat(worker): read Gemini API key from environment variables"
```
