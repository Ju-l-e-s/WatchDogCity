# Budget Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ajouter une page "Budget" permettant de visualiser les dépenses publiques votées (dashboard global + flux détaillé classé par thématique).

**Architecture:** Pur Frontend (HTML/JS/CSS). Les données sont extraites des délibérations. Bien que la spec mentionnait la recherche d'un badge HTML, le fichier `app.js` génère ce badge à partir de la propriété `budget_impact` déjà présente dans le JSON. Nous utiliserons donc directement `budget_impact` et `topic_tag` pour grouper les données sans avoir besoin de Regex complexe.

**Tech Stack:** HTML5, Vanilla JavaScript, TailwindCSS (utilisant les classes utilitaires existantes).

---

### Task 1: Ajouter la navigation pour la page Budget

**Files:**
- Modify: `frontend/index.html`
- Modify: `frontend/app.js`

- [ ] **Step 1: Ajouter le bouton "Budget" dans le menu Desktop**
Modifier `frontend/index.html` pour ajouter un bouton "Budget" à côté des autres liens du menu desktop.

```html
<!-- À insérer dans la div de navigation desktop (autour de la ligne 65), par exemple après le bouton "Les Élus" -->
<button onclick="toggleView('budget')" id="nav-budget-btn" class="text-[13px] font-medium text-slate-500 hover:text-slate-900 transition-colors tracking-wide min-h-[44px] px-2">Budget</button>
<button onclick="toggleView('timeline')" id="nav-timeline-btn" class="text-[13px] font-bold text-brand-700 transition-colors tracking-wide min-h-[44px] px-2">Actualités</button>
```
*Note: Il faudra adapter le style pour refléter l'onglet actif dans le JS.*

- [ ] **Step 2: Ajouter le conteneur principal du Budget**
Dans `frontend/index.html`, juste après la balise de fermeture de `<div id="timeline" ...>`, ajouter le conteneur pour la vue budget, masqué par défaut.

```html
    <div id="budget-view" class="hidden space-y-16 min-h-[400px]">
      <!-- Le contenu du budget sera injecté ici par JS -->
    </div>
```

- [ ] **Step 3: Implémenter la logique de bascule de vue dans JS**
Dans `frontend/app.js`, ajouter une fonction `toggleView(viewName)` pour masquer/afficher les conteneurs et mettre à jour le menu.

```javascript
// À ajouter à la fin du fichier frontend/app.js
function toggleView(viewName) {
    const timelineView = document.getElementById("timeline");
    const budgetView = document.getElementById("budget-view");
    const globalDashboard = document.getElementById("global-dashboard");
    const navBudgetBtn = document.getElementById("nav-budget-btn");
    const navTimelineBtn = document.getElementById("nav-timeline-btn");

    if (viewName === 'budget') {
        if(timelineView) timelineView.classList.add("hidden");
        if(globalDashboard) globalDashboard.classList.add("hidden");
        if(budgetView) budgetView.classList.remove("hidden");
        
        if(navBudgetBtn) {
            navBudgetBtn.classList.add("text-brand-700", "font-bold");
            navBudgetBtn.classList.remove("text-slate-500", "font-medium");
        }
        if(navTimelineBtn) {
            navTimelineBtn.classList.remove("text-brand-700", "font-bold");
            navTimelineBtn.classList.add("text-slate-500", "font-medium");
        }
        
        renderBudgetView();
    } else {
        if(timelineView) timelineView.classList.remove("hidden");
        if(globalDashboard) globalDashboard.classList.remove("hidden");
        if(budgetView) budgetView.classList.add("hidden");
        
        if(navTimelineBtn) {
            navTimelineBtn.classList.add("text-brand-700", "font-bold");
            navTimelineBtn.classList.remove("text-slate-500", "font-medium");
        }
        if(navBudgetBtn) {
            navBudgetBtn.classList.remove("text-brand-700", "font-bold");
            navBudgetBtn.classList.add("text-slate-500", "font-medium");
        }
    }
}
```

### Task 2: Extraire et structurer les données du budget

**Files:**
- Modify: `frontend/app.js`

- [ ] **Step 1: Créer la fonction `renderBudgetView` (Structure de base)**
Dans `frontend/app.js`, créer la fonction qui va générer l'affichage de l'onglet Budget.

```javascript
// À ajouter dans frontend/app.js
function renderBudgetView() {
    const container = document.getElementById("budget-view");
    if (!container) return;

    // 1. Extraction et regroupement des délibérations financières
    let totalBudget = 0;
    const thematicData = {}; // { "Culture": { total: 0, delibs: [] }, ... }
    let financialDelibsCount = 0;

    allCouncils.forEach(council => {
        (council.deliberations || []).forEach(d => {
            if (d.budget_impact && d.budget_impact > 0) {
                totalBudget += d.budget_impact;
                financialDelibsCount++;
                const theme = d.topic_tag || 'Administration Générale';
                
                if (!thematicData[theme]) {
                    thematicData[theme] = { total: 0, delibs: [] };
                }
                
                thematicData[theme].total += d.budget_impact;
                // On ajoute la date du conseil pour l'affichage chronologique
                thematicData[theme].delibs.push({
                    ...d,
                    council_date: council.date
                });
            }
        });
    });

    if (financialDelibsCount === 0) {
        container.innerHTML = `<div class="text-center py-20 text-slate-500">Aucune donnée budgétaire disponible pour le moment.</div>`;
        return;
    }

    // Tri des thématiques par montant décroissant
    const sortedThemes = Object.entries(thematicData).sort((a, b) => b[1].total - a[1].total);

    // La suite de l'affichage sera générée ici
    generateBudgetHTML(container, totalBudget, financialDelibsCount, sortedThemes);
}
```

### Task 3: Générer l'affichage du Dashboard et du flux classé par thématique

**Files:**
- Modify: `frontend/app.js`

- [ ] **Step 1: Créer la fonction d'affichage du Dashboard et du Flux**
Dans `frontend/app.js`, implémenter `generateBudgetHTML` pour créer le rendu complet. Cette fonction utilise les variables de la tâche 2 et la fonction `renderDeliberationRow` existante.

```javascript
// À ajouter dans frontend/app.js
function generateBudgetHTML(container, totalBudget, count, sortedThemes) {
    let html = `
        <div class="dashboard-container animate-fade-in mb-12">
            <div class="mb-8">
                <span class="dashboard-title flex items-center gap-2 text-[11px] font-black text-slate-400 uppercase tracking-[0.2em] mb-2">
                    💰 Synthèse des dépenses votées
                </span>
                <div class="flex flex-col md:flex-row md:items-end gap-4">
                    <div class="text-4xl md:text-5xl font-black text-slate-900 tracking-tight">${formatBudget(totalBudget)}</div>
                    <div class="text-sm font-semibold text-slate-500 mb-2">${count} délibérations financées</div>
                </div>
            </div>
        </div>
    `;

    // Parcours de chaque thématique pour créer les sections
    sortedThemes.forEach(([themeName, themeData]) => {
        const themeColor = COLORS[themeName] || COLORS['Autres'];
        const percentage = ((themeData.total / totalBudget) * 100).toFixed(1);
        
        // En-tête de la thématique
        html += `
            <div class="mb-10">
                <div class="flex items-center justify-between border-b border-slate-200 pb-3 mb-6">
                    <div class="flex items-center gap-3">
                        <div class="w-4 h-4 rounded" style="background-color: ${themeColor}"></div>
                        <h3 class="text-2xl font-bold text-slate-900">${themeName}</h3>
                        <span class="text-sm font-semibold text-slate-500 bg-slate-100 px-2 py-1 rounded">${percentage}%</span>
                    </div>
                    <div class="text-xl font-bold" style="color: ${themeColor}">${formatBudget(themeData.total)}</div>
                </div>
                <div class="bg-white rounded-[2rem] shadow-card overflow-hidden">
                    <div class="divide-y divide-slate-100/50">
        `;

        // Tri des délibérations par date (du plus récent au plus ancien)
        themeData.delibs.sort((a, b) => b.council_date.localeCompare(a.council_date));

        // Rendu de chaque délibération dans cette thématique
        themeData.delibs.forEach(delib => {
            // Utilisation de la fonction existante renderDeliberationRow
            // Note: renderDeliberationRow attend un objet delibération standard.
            // On s'assure d'inclure la date du conseil pour le contexte visuel (Optionnel, on pourrait injecter la date dans le titre ou sous-titre)
            
            // Pour marquer la date du conseil dans l'affichage, on modifie temporairement le topic_tag
            const originalTag = delib.topic_tag;
            delib.topic_tag = formatDate(delib.council_date);
            
            html += renderDeliberationRow(delib);
            
            // Restauration du tag (bonne pratique)
            delib.topic_tag = originalTag;
        });

        html += `
                    </div>
                </div>
            </div>
        `;
    });

    container.innerHTML = html;
}
```
