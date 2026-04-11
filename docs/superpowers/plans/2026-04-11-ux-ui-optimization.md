# 2026-04-11-ux-ui-optimization.md Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Optimiser l'expérience utilisateur (UX) et l'interface (UI) pour un rendu "nickel" sur desktop et mobile, incluant la correction de bugs de recherche, la gestion des modals et le SEO.

**Architecture:** Approche chirurgicale sur les fichiers frontend existants (`index.html`, `app.js`, `input.css`). Amélioration de la robustesse de la recherche via l'échappement des expressions régulières et synchronisation des entrées.

**Tech Stack:** HTML5, Tailwind CSS v4, Vanilla JavaScript.

---

### Task 1: Corrections CSS & Gestion des Modals

**Files:**
- Modify: `frontend/input.css`

- [ ] **Step 1: Ajouter la classe .modal-open et améliorer le squelette**

```css
/* ... dans @layer base ... */
body.modal-open {
  overflow: hidden;
}

/* ... dans @layer components ... */
.skeleton {
  @apply relative overflow-hidden bg-slate-100 rounded-xl;
}
/* ... */
```

- [ ] **Step 2: Vérifier le rendu des ombres et transitions**

S'assurer que `--shadow-micro` et les animations sont bien définies pour un rendu fluide.

---

### Task 2: Robustesse de la Recherche (Regex Escaping)

**Files:**
- Modify: `frontend/app.js`

- [ ] **Step 1: Ajouter la fonction d'échappement Regex**

```javascript
function escapeRegExp(string) {
    return string.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
```

- [ ] **Step 2: Intégrer l'échappement dans highlightText**

```javascript
function highlightText(text, query, acronymsMap = null) {
    let processedText = escapeHTML(text);
    if (acronymsMap) processedText = applyAcronyms(processedText, acronymsMap);
    if (!query) return processedText;
    
    const escapedQuery = escapeHTML(query);
    // On échappe les caractères spéciaux de la query AVANT de créer la Regex
    const safeQuery = escapeRegExp(escapedQuery);
    const regex = new RegExp(`(${safeQuery})`, 'gi');
    
    const parts = processedText.split(/(<[^>]+>)/g);
    return parts.map(part => {
        if (part.startsWith('<')) return part;
        return part.replace(regex, `<mark class="bg-brand-100 text-brand-700 font-bold px-0.5 rounded">$1</mark>`);
    }).join('');
}
```

---

### Task 3: Synchronisation des Inputs de Recherche & Focus Mobile

**Files:**
- Modify: `frontend/app.js`

- [ ] **Step 1: Synchroniser les valeurs des inputs**

```javascript
function handleSearch(val) {
    // Sync inputs
    const navInput = document.getElementById('nav-search-input');
    const mobileInput = document.getElementById('mobile-search-input');
    if (navInput && navInput.value !== val) navInput.value = val;
    if (mobileInput && mobileInput.value !== val) mobileInput.value = val;

    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => {
        searchQuery = val.toLowerCase().trim();
        visibleCouncilsCount = searchQuery ? 5 : 1;
        render();
    }, 300);
}
```

- [ ] **Step 2: Auto-focus sur mobile lors de l'ouverture**

```javascript
function toggleMobileMenu(show) {
    const menu = document.getElementById('mobile-menu');
    menu.classList.toggle('hidden', !show);
    document.body.classList.toggle('modal-open', show);
    
    if (show) {
        setTimeout(() => {
            document.getElementById('mobile-search-input')?.focus();
        }, 100);
    } else {
        document.getElementById('mobile-search-input')?.blur();
    }
}
```

---

### Task 4: Nettoyage SEO & Metadata

**Files:**
- Modify: `frontend/index.html`

- [ ] **Step 1: Mettre à jour les URLs et extensions d'images**

Remplacer `cloudfront.net` par `lobservatoiredebegles.fr` et `.png` par `.webp` pour le logo.

```html
  <meta property="og:url" content="https://www.lobservatoiredebegles.fr/">
  <meta property="og:image" content="https://www.lobservatoiredebegles.fr/logo-begles.webp">
```

- [ ] **Step 2: Ajouter aria-hidden sur les overlays de modal**

```html
<div class="modal-overlay absolute inset-0" onclick="toggleAboutModal(false)" aria-hidden="true"></div>
```

---

### Task 5: Validation Finale

- [ ] **Step 1: Re-build le CSS**

Run: `cd frontend && npx tailwindcss -i input.css -o style.css`

- [ ] **Step 2: Tester la recherche avec des caractères spéciaux**

Saisir `(` ou `.` dans la barre de recherche.
Attendu : Pas d'erreur console, surbrillance correcte.

- [ ] **Step 3: Vérifier le blocage du scroll**

Ouvrir un modal sur mobile (inspecteur Chrome) et tenter de scroller le fond.
Attendu : Le fond reste fixe.
