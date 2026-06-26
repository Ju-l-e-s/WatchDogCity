# Prompt Alignment Audit — Pipeline Prompts vs Deterministic QC Gateway

> Scope: align the three generative prompts (Worker, Notifier/newsletter, Aggregator)
> with the deterministic *Correctness by Construction* gate (`lambdas/shared/qc.go`,
> rules D1–D10 / S1–S6) and the sensory-deprivation neutrality guarantee.
>
> This document is **diagnosis only** — no prompt was modified. Each finding lists the
> gate rule it threatens, why the current prompt risks it, and the concrete fix.
>
> Severity legend:
> - **HIGH** — current wording can cause a QC `QUARANTINED` verdict (whole council blocked
>   from publication) **or** breaks the neutrality guarantee on a public surface.
> - **MED** — increases hallucination/variance or wastes the model; no hard block.
> - **LOW** — cosmetic / belt-and-suspenders.

---

## Why prompt alignment matters now

The gate is binary and fail-closed. A *single* HIGH violation on *one* deliberation
quarantines the **entire council** — nothing publishes, no newsletter sends. The model is
no longer the last line of defense; it is the first. Any latitude the prompt leaves
(a nullable field, an unconstrained enum, an ambiguous "significant amount") is now a
production outage waiting for the right PDF. Prompts must be written to make the gate's
HIGH rules *structurally unreachable*, not merely discouraged.

---

## Part A — The three flagged issues (confirmed)

### A1. Worker — the math trap (user point #1) → threatens **D4** · HIGH
`lambdas/worker/gemini.go:50-51`. The breakdown instruction never states that
`Σ budget_breakdown[].amount` must equal `budget_impact`. The gate (`qc.go` D4) now
**hard-fails** (promoted from the old silent `WARN`+`fmt.Printf`) when
`|Σamount − budget_impact| > 1 €`. Confirmed real.

Fix — add an explicit accounting invariant to the breakdown rule:

```
- "budget_breakdown" : si non vide, la SOMME EXACTE des "amount" DOIT être égale à
  "budget_impact" (tolérance 0). Avant de produire le JSON, additionne mentalement les
  "amount" et corrige jusqu'à l'égalité parfaite. Si tu ne peux pas ventiler la totalité,
  laisse "budget_breakdown" = [] plutôt que de produire une ventilation partielle.
```

### A2. Notifier/newsletter — contradictory persona (user point #2) → neutrality · HIGH
`lambdas/shared/newsletter.go:289` (`"Tu es rédacteur en chef…"`), reinforced by
`:295` (`"Analyse journalistique"`) and `:343` (`"STYLE : Journalistique, actif"`).
The sensory-deprivation preamble (`:284-287`) and the editor persona are in direct
conflict; the persona wins on tone and pulls the model toward editorial framing and
partisan adjectives. Confirmed real.

Fix — replace the persona and the style line:

```
:289  "Tu es un vulgarisateur neutre et un traducteur factuel pour L'Observatoire de
       Bègles. Tu n'es PAS journaliste : tu ne produis aucune ligne éditoriale, aucune
       interprétation politique, aucun adjectif d'appréciation. Tu transformes des faits
       structurés en phrases simples et neutres."
:343  "- STYLE : descriptif, factuel, neutre. Phrases courtes. Aucun adjectif évaluatif
       (excellent, ambitieux, coûteux, important, crucial…)."
```

### A3. Aggregator — call to extrapolation (user point #3) → hallucination + neutrality · HIGH
`lambdas/aggregator/main.go:314-315`. `"identifiant l'enjeu politique ou social majeur"`
plus `"Sois précis sur l'impact citoyen"` is an open door to inferring intent the source
does not state (the stadium-grant → global-policy hallucination). Confirmed real, and see
**C3** below for why this prompt is the *most* dangerous of the three.

Fix — replace with descriptive compaction, no intent attribution:

```go
prompt := fmt.Sprintf(`Voici les résumés des délibérations d'un conseil municipal :
%s

Rédige une synthèse de 1 à 2 phrases (25 à 45 mots) qui DÉCRIT factuellement la séance :
cite les 1 ou 2 domaines concentrant le plus gros budget et, le cas échéant, les sujets
ayant suscité une opposition (votes contre / abstentions).
N'attribue AUCUNE intention politique à la mairie. N'invente aucun fait, chiffre ou enjeu
absent des résumés. Ne déduis aucune "politique globale". Pas de préfixe "Enjeu Clé :".`,
    strings.Join(summaries, "\n"))
```

---

## Part B — Additional findings (my own diagnostic)

### Worker (`lambdas/worker/gemini.go`)

**B1. Breakdown `topic_tag` is unconstrained — both prompt AND schema → D5 · HIGH.**
Gate D5 quarantines any breakdown line whose `topic_tag` is not one of the 10 canonical
tags. But the prompt (`:51`) only shows `{"topic_tag":"...","label":"...","amount":...}`
with no enum, and the **schema itself does not enforce it**: `gemini.go:128`
(`"topic_tag": {Type: genai.TypeString}`) has no `Enum`, unlike the top-level
`topic_tag` (`:100-104`). So the model can freely emit `"Stade"`, `"Voirie"`, etc. and the
council quarantines. Two-part fix:
- Schema: add `Format: "enum", Enum: validTopicTags` to the breakdown item `topic_tag`.
- Prompt: enumerate the 10 valid tags in the breakdown rule.

**B2. `analysis_data.impacts` can be `null` on impactless-but-non-administrative delibs → D9 · HIGH.**
Schema marks `impacts` `Nullable: true` (`:112`) and the prompt (`:42`) only forces
`"Néant"` for *purely administrative* items. A deliberation that is substantive but has no
*citizen* impact (or where the model simply hesitates) can come back `null` → D9 hard-fail
(`qc.go:288` nil, `:296` empty/`"null"`). Fix — make the field total:

```
- "impacts" n'est JAMAIS null ni vide. Soit il décrit un impact citoyen concret, soit il
  vaut EXACTEMENT la chaîne "Néant". Aucune autre valeur (pas de "null", "N/A", "-").
```

**B3. `analysis_data.decision` never mandated → D8 · HIGH.**
`decision` is `Nullable: true` (`:111`) and no prompt line requires it. Gate D8
(`qc.go:279`) quarantines on nil/empty `decision`. Fix — add: `"decision" est OBLIGATOIRE
et non vide : indique ce qui a été décidé/voté, en une phrase factuelle.`

**B4. `has_vote=true` half covered → D6 · HIGH.**
Prompt `:53` covers only the false branch (`"Si pas de vote, has_vote=false et compteurs à
null"`). Gate D6 (`qc.go:233`) also quarantines `has_vote=true` with all three counters
nil. Fix — add the inverse: `Si has_vote=true, au moins un des compteurs (pour/contre/
abstention) doit être un entier ≥ 0 ; ne les laisse pas tous à null.`

**B5. `budget_type=AUCUN` defined by a fuzzy word → D2 · MED.**
`:38` defines `AUCUN` as `"Aucun montant significatif"`. "Significatif" invites the model to
tag a small non-zero amount as `AUCUN` → D2 hard-fail (`qc.go:162`). Fix — make it a strict
biconditional: `"AUCUN" ⇔ budget_impact = 0. Tout montant non nul (même petit) DOIT porter
un type DÉPENSE/RECETTE/CAUTION. Réciproquement, budget_impact=0 impose budget_type=AUCUN.`

**B6. No vote-count realism guard → S6 · MED.**
Gate S6 (`qc.go:421`) quarantines `pour+contre+abstention > 60`. If the model lifts a
quorum/attendance/population figure into a vote counter, S6 fires. Fix — anchor the model:
`Les compteurs de vote correspondent aux conseillers municipaux élus (≈ 35) ; jamais au
public, à la population ou à un quorum. Total plausible ≤ ~40.`

**B7. `title` neutrality is load-bearing for the newsletter → neutrality · HIGH.**
This is the subtle one. Under sensory deprivation the newsletter generator receives the
deliberation `Title` as its **only** free-text input (`ColdDeliberation.Title`,
`newsletter.go:21-33`). Every other narrative field is withheld. So a partisan or
editorialized worker `title` ("Coûteux cadeau au club de foot") flows *straight* onto the
public newsletter, untouched by the gate (no D-rule checks title *tone*, only that it is
non-empty, D8). The worker prompt's GROUNDING block (`:44-48`) governs prose but never
constrains the *title* register. Fix — add a title rule:

```
- "title" : titre factuel, neutre et descriptif (objet de la délibération). Aucun
  adjectif évaluatif, aucune accroche. C'est la SEULE phrase reprise telle quelle par la
  newsletter : elle doit pouvoir être publiée sans relecture.
```

**B8. Euros vs centimes / scaling → S5 · LOW.**
S5 (`qc.go:400`, ceiling 500 M€) exists to catch 100× parse bugs. Cheap insurance in the
prompt: `Exprime les montants en EUROS entiers (pas en centimes) ; ne multiplie ni ne
convertis aucun montant.`

### Notifier / newsletter (`lambdas/shared/newsletter.go`)

**B9. `email_subject` is both partisan AND dead code → MED.**
`:292` asks for an `"accrocheur, < 60 caractères, reflète l'enjeu politique majeur"`
subject — clickbait framing *and* the same political-stakes extrapolation as A3. It is also
**discarded**: `GenerateNewsletterParams` overrides it with the constant
`newsletterEmailSubject` (`:473`). So the instruction trains a partisan frame for zero
output value. Fix — tell the model the subject is fixed: `"email_subject": "laisse vide,
imposé par le système"` (or drop the field from the prompt body).

**B10. "percutant" / "explicite et percutant" titles → neutrality · MED.**
`:304` (`"Titre explicite et percutant"`) and the punchy framing fight the neutral persona.
The generator is sensory-deprived: it should *lightly vulgarize the provided factual Title*,
not invent a headline. Fix: `"title": "reformulation neutre et simple du titre factuel
fourni ; pas d'accroche, pas d'adjectif."`

**B11. `main_issue` asks "why is this council important?" → neutrality · MED.**
`:295` (`"Pourquoi ce conseil est-il important ?"`) is an importance/value judgment. Reframe
as descriptive compaction (mirror A3): `"main_issue": "1 à 2 phrases factuelles décrivant
le ou les domaines au plus gros budget et la présence/absence d'opposition. Aucune notion
d'importance, d'enjeu politique ou de jugement."`

**B12. Missing `Temperature: 0` on the neutrality-critical generator → MED.**
`newsletter.go:450-455` builds `GenerateContentConfig` with `ResponseMIMEType`,
`ResponseSchema`, `MaxOutputTokens` — **no `Temperature`**. The Worker sets
`Temperature: ptrFloat32(0)` (`gemini.go:211`); the spec's calling convention (§1) mandates
it. For the one LLM whose entire job is *non-creative* fact-phrasing, a default
(non-zero) temperature is exactly backwards. Fix — add `Temperature: ptrFloat32(0)`.

### Aggregator (`lambdas/aggregator/main.go`)

**B13. No `Temperature: 0`, and `APIVersion: "v1"` instead of `"v1beta"` → MED.**
`askGeminiForSynthesis` (`:303-324`) omits `Temperature` and uses `APIVersion: "v1"` while
Worker/newsletter use `"v1beta"`. Non-deterministic synthesis on a public-facing string.
Fix — `Temperature: ptrFloat32(0)` and align the API version.

---

## Part C — Cross-cutting / systemic (the things easy to miss)

### C3. The Aggregator synthesis BYPASSES both neutrality controls — and still reaches the public. **HIGH**
This is the most important finding. The architecture's neutrality guarantee rests on two
pillars: (1) the deterministic gate, which only inspects **deliberation** fields
(`DeliberationView` in `qc.go`), and (2) sensory deprivation, which applies only to the
**newsletter** generator. The Aggregator's `askGeminiForSynthesis` output (`voteSummary`)
is written to `council.analysis.vote_summary` (`main.go:177-193`) and is **not covered by
either**:
- the gate never reads `council.analysis.*`, so D/S rules cannot quarantine it;
- it is full free-text generated from worker `summary` prose, the opposite of sensory
  deprivation;
- it lands in `data.json` via the Publisher and is rendered on the public website.

So the *least-controlled* generative text in the whole system is also on a public surface.
Tightening A3's wording is necessary but, structurally, this is a hole: either the synthesis
prompt must be the strictest of the three, or the synthesis should be made deterministic
(template from `dominantTheme` + vote stats, both already computed in Go at `:149-150`) and
the LLM dropped here entirely. Recommend raising this as a design decision, not just a prompt
tweak.

### C4. Worker "strict Néant" instruction can collide with gate S2. **Awareness, not a prompt fix**
The worker prompt (`:42`) correctly forces `impacts="Néant"` for administrative items. But
gate S2 (`qc.go:361`, ceiling 0.65) quarantines any council where >65% of delibs are
`"Néant"`. A genuinely administrative-heavy session (lots of internal/procedural items) will
be **correctly** analyzed and then **quarantined** for telling the truth. The fix is *not*
to weaken the Néant instruction (that would induce fabricated impacts — the exact failure
the gate exists to prevent). Flag for **gate calibration**: either raise `NeantRateCeiling`,
or make S2 conditional on council size / category. Listed so the prompt author doesn't
"fix" it in the wrong layer.

### C5. Enforce enums in the schema, not only the prompt. **MED**
B1's breakdown `topic_tag` is the concrete case, but the principle is general: every field a
D-rule canonicalizes (D1, D5) should carry `Format:"enum"` in the `ResponseSchema` so the API
refuses bad values at generation, leaving the prompt as reinforcement and the gate as the
final backstop. Prompt-only enforcement is the weakest of the three layers.

---

## Priority summary

| # | Lambda | Finding | Gate rule | Severity |
|---|--------|---------|-----------|----------|
| A1 | Worker | Breakdown sum must equal budget_impact | D4 | HIGH |
| A2 | Newsletter | Editor persona contradicts neutrality | — (neutrality) | HIGH |
| A3 | Aggregator | "enjeu politique majeur" → extrapolation | — (neutrality) | HIGH |
| B1 | Worker | Breakdown topic_tag unconstrained (prompt+schema) | D5 | HIGH |
| B2 | Worker | impacts can be null | D9 | HIGH |
| B3 | Worker | decision not mandated | D8 | HIGH |
| B4 | Worker | has_vote=true counters not mandated | D6 | HIGH |
| B7 | Worker | title tone unconstrained → leaks to newsletter | — (neutrality) | HIGH |
| C3 | Aggregator | synthesis bypasses gate + sensory deprivation, hits public site | — (neutrality) | HIGH |
| B5 | Worker | "AUCUN = montant significatif" ambiguous | D2 | MED |
| B6 | Worker | no vote-count realism guard | S6 | MED |
| B9 | Newsletter | email_subject partisan + discarded | — | MED |
| B10 | Newsletter | "percutant" titles | — (neutrality) | MED |
| B11 | Newsletter | main_issue "why important?" | — (neutrality) | MED |
| B12 | Newsletter | missing Temperature:0 | — (variance) | MED |
| B13 | Aggregator | missing Temperature:0; v1 vs v1beta | — (variance) | MED |
| C5 | All | enforce enums in schema, not just prompt | D1/D5 | MED |
| B8 | Worker | euros-not-centimes guard | S5 | LOW |
| C4 | Worker↔gate | strict-Néant vs S2 ceiling collision | S2 | calibration |

**Recommended sequencing**: land the HIGH worker fixes first (A1, B1–B4, B7) — they are the
ones that silently quarantine councils. Then the neutrality persona rewrites (A2, A3, B9–B11)
and the two `Temperature:0` config fixes (B12, B13). Treat C3 as a separate design decision
and C4 as a gate-tuning ticket, not prompt work.
