# Gold Corpus — Deliberation Extraction Accuracy

Hand-annotated ground truth for Gemini extraction quality regression testing.

## Purpose

Any prompt or model change must be measured against a held-out corpus of real
PDFs. Without this, prompt edits are blind — accuracy can regress silently and
the only signal is user complaint.

## Layout

```
corpus/
├── README.md              ← this file
├── manifest.json          ← list of fixtures + their expected fields
└── fixtures/
    └── <slug>/
        ├── source.pdf     ← original PDF (committed binary, tracked by git)
        └── expected.json  ← ground-truth fields (hand-annotated)
```

## Adding a fixture

1. Pick a representative deliberation PDF from production data.
2. Drop the PDF in `fixtures/<slug>/source.pdf` (lowercase kebab-case slug).
3. Hand-annotate `fixtures/<slug>/expected.json`. Only annotate fields you
   are willing to enforce — leave the rest absent.
4. Append to `manifest.json`:
   ```json
   { "slug": "vote-budget-primitif-2026", "tags": ["budget", "high-value"] }
   ```
5. Run `go test -tags=corpus -run TestCorpusAccuracy ./...` to verify.

## Expected fields (subset of GeminiResult)

```jsonc
{
  "title_contains": ["Budget primitif"],          // substring match, all required
  "topic_tag": "Budget",                            // exact enum match
  "budget_type": "DÉPENSE",                         // exact enum match
  "budget_impact_min": 1000000,                     // tolerance ranges
  "budget_impact_max": 5000000,
  "climate_impact": "neutre",
  "has_vote": true,
  "is_substantial": true,
  "impacts_is_neant": false,                        // analysis_data.impacts == "Néant"
  "min_breakdown_items": 5                          // budget_breakdown length lower bound
}
```

## Why not assert exact text?

Gemini summary / context output is stochastic even at temperature 0. Asserting
exact strings produces a brittle test that flips with every model upgrade. The
corpus enforces **structural and numeric** invariants (enum membership, budget
range, breakdown count) which are the high-signal failure modes.

## Cost

Each test run = 1 Gemini call per fixture. Set `GEMINI_API_KEY` and run
explicitly with `-tags=corpus`. CI does **not** run this suite by default —
it's an opt-in tool for prompt / model migrations.
