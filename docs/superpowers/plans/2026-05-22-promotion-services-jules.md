# Promotion Services Jules Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Intégrer les services professionnels de Jules Laconfourque dans le footer et la modale "À propos" de l'Observatoire.

**Architecture:** Modifications chirurgicales de `frontend/index.html` pour ajouter du contenu HTML statique et des triggers JavaScript existants (`toggleContactModal`, `toggleAboutModal`).

**Tech Stack:** HTML5, Tailwind CSS (via classes existantes), Vanilla JS.

---

### Task 1: Mise à jour du Footer

**Files:**
- Modify: `frontend/index.html`

- [ ] **Step 1: Modifier la ligne de copyright dans le footer**

Rechercher la ligne vers 385 et la remplacer.

```html
<p>© 2026 Observatoire Bègles — Conçu par <button onclick="toggleContactModal(true)" class="font-medium hover:text-brand-700 transition-colors underline decoration-slate-300 underline-offset-4">Jules Laconfourque</button>. Contactez-moi pour vos projets.</p>
```

- [ ] **Step 2: Vérifier visuellement la cohérence**
S'assurer que le bouton utilise les classes `text-slate-600` héritées du parent ou les surcharger si nécessaire pour rester discret.

---

### Task 2: Enrichissement de la Modale "Le Projet"

**Files:**
- Modify: `frontend/index.html`

- [ ] **Step 1: Ajouter la section de services professionnels**

Insérer ce bloc juste avant la fermeture de la div `prose prose-slate` (vers la ligne 218) ou juste avant le bouton "J'AI COMPRIS".

```html
          <section class="mt-10 pt-10 border-t border-slate-100">
            <h4 class="text-slate-900 font-bold text-lg mb-4">Un projet informatique en tête ?</h4>
            <p class="text-slate-600 mb-6">Au-delà de cette démarche citoyenne, je mets mes compétences en développement informatique au service de vos projets. Que vous soyez un particulier, une association ou une petite entreprise, je suis disponible pour vous accompagner.</p>
            
            <div class="space-y-6 mb-8">
              <div class="flex gap-4">
                <div class="w-10 h-10 bg-brand-50 rounded-xl flex items-center justify-center text-brand-700 shrink-0">
                  <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13 10V3L4 14h7v7l9-11h-7z"></path></svg>
                </div>
                <div>
                  <p class="font-bold text-slate-900 text-sm mb-1">Simplifier et automatiser</p>
                  <p class="text-xs text-slate-500 leading-relaxed">Création d'outils sur-mesure pour vos tâches répétitives et l'organisation de vos données.</p>
                </div>
              </div>
              <div class="flex gap-4">
                <div class="w-10 h-10 bg-brand-50 rounded-xl flex items-center justify-center text-brand-700 shrink-0">
                  <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.364-6.364l-.707-.707M6.343 17.657l-.707.707m12.728 0l-.707-.707"></path></svg>
                </div>
                <div>
                  <p class="font-bold text-slate-900 text-sm mb-1">Donner vie à vos idées</p>
                  <p class="text-xs text-slate-500 leading-relaxed">Accompagnement complet pour vos applications et sites web, de la conception à la réalisation.</p>
                </div>
              </div>
              <div class="flex gap-4">
                <div class="w-10 h-10 bg-brand-50 rounded-xl flex items-center justify-center text-brand-700 shrink-0">
                  <svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 12h.01M12 12h.01M16 12h.01M21 12c0 4.418-4.03 8-9 8a9.863 9.863 0 01-4.255-.949L3 20l1.395-3.72C3.512 15.042 3 13.574 3 12c0-4.418 4.03-8 9-8s9 3.582 9 8z"></path></svg>
                </div>
                <div>
                  <p class="font-bold text-slate-900 text-sm mb-1">Conseil et accompagnement</p>
                  <p class="text-xs text-slate-500 leading-relaxed">Avis technique et sécurisation de vos outils numériques au quotidien.</p>
                </div>
              </div>
            </div>

            <p class="text-sm text-slate-500 italic mb-8 border-l-2 border-brand-200 pl-4">
              "Mon approche est simple : construire des solutions robustes, utiles et surtout, faciles à utiliser."
            </p>

            <button onclick="toggleAboutModal(false); setTimeout(() => toggleContactModal(true), 300)" class="w-full bg-brand-700 text-white font-bold py-4 rounded-2xl hover:bg-brand-600 transition-all shadow-lg shadow-brand-700/10">
              DISCUTER DE MON PROJET
            </button>
          </section>
```

- [ ] **Step 2: Vérifier le bouton de fermeture existant**
S'assurer que le bouton "J'AI COMPRIS" (vers la ligne 222) est toujours présent après l'insertion ou le supprimer s'il fait doublon avec le nouveau CTA. (Recommandation : Le garder car il est générique).

---

### Task 3: Validation Finale

- [ ] **Step 1: Vérification du flux de navigation**
Cliquer sur le nom dans le footer -> Vérifier que la modale de contact s'ouvre.
Ouvrir "Le Projet" -> Cliquer sur "DISCUTER DE MON PROJET" -> Vérifier que "Le Projet" se ferme et que "Contact" s'ouvre.
