# Spécification : Refonte Citoyenne de l'Interface

**Date :** 2026-04-04
**Sujet :** Transformation de l'interface technique en un outil de transparence démocratique accessible.

## 1. Objectifs
- Supprimer le jargon technique et les métadonnées inutiles pour le citoyen.
- Améliorer la lisibilité des décisions et des résultats de vote.
- Clarifier la position de l'opposition sans artifices typographiques (italiques/guillemets).

## 2. Changements Backend (Analyseur Gemini)

### 2.1 Modification du Prompt (`lambdas/worker/gemini.go`)
Le prompt doit être mis à jour pour inclure la génération d'un tag thématique unique.
- **Nouveau champ :** `topic_tag` (un seul mot, ex: "Budget", "Urbanisme", "Social").
- **Langue :** Exclusivement en français.

### 2.2 Mise à jour des structures de données
- `GeminiResult` (Worker Go)
- `DeliberationRecord` (DynamoDB)
- `DeliberationOutput` (Publisher Go / JSON final)

## 3. Changements Frontend (`frontend/index.html`)

### 3.1 Suppression des badges techniques
- Retrait définitif des badges affichant l'ID (REF) ou le mode de collecte (manual).
- Affichage du nouveau `topic_tag` à la place, avec un style "pilule" (pill) coloré et lisible.

### 3.2 Refonte de la section "Votes"
- **Hiérarchie :** Les chiffres (Pour, Contre, Abstention) deviennent l'élément visuel principal.
- **Style :** Utilisation de `text-2xl font-bold` pour les nombres.
- **Barre de progression :** Devient un élément de soulignement fin (quelques pixels de hauteur) sous les chiffres.

### 3.3 Reformulation des désaccords
- **Libellé :** Remplacer "Points de friction" par "Position de l'opposition".
- **Typographie :** Retirer les guillemets et l'italique du texte pour un rendu plus factuel et neutre.

## 4. Plan de Validation
- **Tests unitaires :** Vérifier que le Worker extrait correctement le `topic_tag` du JSON Gemini.
- **Test visuel :** Confirmer le rendu responsive de la nouvelle section des votes sur mobile et desktop.
- **Intégrité des données :** S'assurer que les anciennes délibérations (sans `topic_tag`) ne cassent pas l'affichage (fallback sur la catégorie du conseil).
