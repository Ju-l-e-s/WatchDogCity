# Stratégie de Tracking PostHog — WatchdogCity (Observatoire Citoyen de Bègles)

> **Document** : Cahier des charges tracking  
> **Date** : 29 juin 2026  
> **Site** : Site statique HTML/JS/Tailwind — `lobservatoiredebegles.fr`  
> **SDK** : `posthog-js` (snippet minimal, chargé en async)

---

## 1. Événements de tracking (Usage)

### P0 — CRITIQUES (à implémenter en premier)

| # | Nom PostHog | Déclencheur | Propriétés | Justification |
|---|-------------|-------------|------------|---------------|
| 1 | `page_viewed` | À chaque chargement de page (ou navigation SPA entre vues) | `view` (timeline/budget), `device_type` (mobile/desktop/tablet), `referrer`, `url`, `pathname`, `screen_width` | Mesure d'audience de base, segmentation device |
| 2 | `data_loaded` | Quand `data.json` est chargé avec succès | `council_count`, `deliberation_count`, `load_time_ms`, `data_version` (date du fichier) | Mesure la fraîcheur et la volumétrie des données |
| 3 | `search_performed` | Après le debounce de 300ms de `handleSearch()` | `query_length`, `results_count`, `empty` (booléen), `source` (navbar/mobile) | Usage de la recherche, taux de recherche sans résultat |
| 4 | `topic_filter_clicked` | Clic sur un chip de filtre thématique | `topic` (nom du thème), `was_active` (booléen si déjà sélectionné) | Quels thèmes intéressent le plus les citoyens |
| 5 | `deliberation_expanded` | Clic pour ouvrir le détail d'une délibération (`toggleDelib`) | `deliberation_id`, `topic_tag`, `has_vote` (booléen), `has_budget_impact` (booléen), `has_analysis` (booléen) | Mesure de l'engagement avec les contenus détaillés |
| 6 | `view_toggled` | Changement de vue timeline ↔ budget (`toggleView`) | `target_view` (timeline/budget), `previous_view` | Adoption de la vue Budget |

### P1 — IMPORTANTS

| # | Nom PostHog | Déclencheur | Propriétés | Justification |
|---|-------------|-------------|------------|---------------|
| 7 | `load_more_clicked` | Clic sur le bouton "Voir plus" | `currently_visible`, `remaining`, `total_filtered` | Engagement scroll, taux d'abandon avant load more |
| 8 | `newsletter_form_submitted` | Soumission du formulaire newsletter | `email_domain`, `success` (booléen), `error_type` si échec | Conversion newsletter, source d'emails |
| 9 | `contact_form_submitted` | Soumission du formulaire de contact | `success` (booléen), `error_type` si échec | Taux de conversion contact, détection pannes API |
| 10 | `modal_opened` | Ouverture d'une modale (About, Team, Contact) | `modal_name` (about/team/contact), `source` (navbar/mobile_menu) | Pages les plus consultées |
| 11 | `pdf_link_clicked` | Clic sur un lien PDF officiel | `deliberation_id`, `source_location` (delib_row/budget_detail/adjustment), `pdf_url_domain` | Trafic sortant vers les sources officielles |
| 12 | `external_link_clicked` | Clic sur lien mairie-begles.fr dans le footer | `link_url` | Trafic sortant |

### P2 — SECONDAIRES (utiles pour analyses avancées)

| # | Nom PostHog | Déclencheur | Propriétés | Justification |
|---|-------------|-------------|------------|---------------|
| 13 | `scroll_depth_reached` | Quand l'utilisateur atteint 25%, 50%, 75%, 100% de la page (throttled) | `depth_percent`, `view` (timeline/budget) | Engagement vertical |
| 14 | `vote_detail_viewed` | Quand la section "Vote" d'une délibération est visible | `deliberation_id`, `vote_climate` (unanimous/opposition/abstention), `pour`, `contre`, `abstention` | Intérêt pour le climat politique |
| 15 | `mobile_menu_toggled` | Ouverture/fermeture du menu mobile | `action` (open/close) | Usage mobile |

---

## 2. Signaux de dysfonctionnement (Error Tracking)

### P0 — CRITIQUES

| # | Nom PostHog | Déclencheur | Propriétés | Alerte à configurer |
|---|-------------|-------------|------------|---------------------|
| E1 | `data_load_failed` | `catch` dans `init()` si `fetch(data.json)` échoue | `http_status`, `error_message`, `load_time_ms` | **OUI** — le site est vide sans données |
| E2 | `data_empty` | Si `allCouncils.length === 0` après chargement réussi | `raw_data_length`, `councils_count` | **OUI** — pas de contenu affichable |
| E3 | `search_no_results` | Si `filteredCouncils.length === 0` après recherche | `query`, `active_topic`, `total_councils_available` | NON (info) — mais dashboard utile |
| E4 | `api_contact_failed` | Échec de l'appel API contact (réseau ou HTTP) | `error_type` (network/http), `http_status` | **OUI** — formulaire cassé = perte de leads |
| E5 | `api_newsletter_failed` | Échec de l'appel API newsletter | `error_type` (network/http), `http_status` | **OUI** — inscriptions perdues |

### P1 — IMPORTANTS

| # | Nom PostHog | Déclencheur | Propriétés | Alerte à configurer |
|---|-------------|-------------|------------|---------------------|
| E6 | `render_error` | Erreur JS non catchée (via `window.onerror`) | `error_message`, `stack_trace`, `file`, `line`, `column` | **OUI** — tout bug JS |
| E7 | `pdf_link_broken` | Clic sur un PDF qui retourne une erreur (nécessite un proxy ou un ping HEAD) | `pdf_url`, `deliberation_id` | NON — mais à tracker côté analytics |
| E8 | `slow_load` | Si `load_time_ms > 3000` pour data.json | `load_time_ms`, `file_size_approx`, `cache_status` | **OUI** — dégradation de performance |

### P2 — SECONDAIRES

| # | Nom PostHog | Déclencheur | Propriétés |
|---|-------------|-------------|------------|
| E9 | `council_with_no_deliberations` | Un conseil dans data.json a `deliberations: []` ou absent | `council_id`, `council_title` |
| E10 | `broken_image` | Une image ne se charge pas (via événement `error` sur `<img>`) | `img_src`, `img_alt` |
| E11 | `unexpected_data_shape` | Si une propriété attendue est manquante dans data.json | `missing_field`, `council_id` |

---

## 3. Implémentation technique

### 3.1 Snippet PostHog dans `index.html`

À insérer **juste avant la fermeture de `</head>`** (ligne 34 actuelle), **avant** le chargement de `style.css` :

```html
<!-- PostHog Snippet (RGPD-friendly : opt-in cookie banner requis) -->
<script>
  // —— Cookie Consent Gate ——
  // Tant que l'utilisateur n'a pas donné son consentement explicite,
  // PostHog est chargé en mode "memory-only" (pas de cookie persistant).
  // Cela permet le tracking anonymisé sans stockage durable.
  !function(t,e){var o,n,p,r;e.__SV||(window.posthog=e,e._i=[],e.init=function(i,s,a){function g(t,e){var o=e.split(".");2==o.length&&(t=t[o[0]],e=o[1]),t[e]=function(){t.push([e].concat(Array.prototype.slice.call(arguments,0)))}}(p=t.createElement("script")).type="text/javascript",p.crossOrigin="anonymous",p.async=!0,p.src=s.api_host.replace(".i.posthog.com","-assets.i.posthog.com")+"/static/array.js",(r=t.getElementsByTagName("script")[0]).parentNode.insertBefore(p,r);var u=e;for(void 0!==a?u=e[a]=[]:a="posthog",u.people=u.people||[],u.toString=function(t){var e="posthog";return"posthog"!==a&&(e+="."+a),t||(e+=" (stub)"),e},u.people.toString=function(){return u.toString(1)+".people (stub)"},o="init capture register register_once register_for_session unregister unregister_for_session getFeatureFlag getFeatureFlagPayload isFeatureEnabled reloadFeatureFlags updateEarlyAccessFeatureEnrollment getEarlyAccessFeatures on onFeatureFlags onSessionId getSurveys getActiveMatchingSurveys renderSurvey canRenderSurvey getNextSurveyStep identify setPersonProperties group resetGroups setPersonPropertiesForFlags resetPersonPropertiesForFlags setGroupPropertiesForFlags resetGroupPropertiesForFlags reset getProperty getGroup setGroup getGroups getAllGroups hasGroup removeGroup removeAllGroups alias".split(" "),n=0;n<o.length;n++)g(u,o[n]);e._i.push([i,s,a])},e.__SV=1)}(document,window.posthog||[]);

  // Mode "opt-in" : on initialise en mémoire uniquement (pas de persistence)
  // Le cookie `ph_consent` sera géré par le bandeau RGPD.
  posthog.init('PHC_YOUR_PROJECT_API_KEY', {
    api_host: 'https://eu.i.posthog.com',  // 🇪🇺 Hébergement UE (RGPD)
    persistence: 'memory',                  // Pas de cookie avant consentement
    autocapture: false,                     // On désactive l'autocapture
    disable_session_recording: true,        // Pas de recording sans consentement
    loaded: function(posthog_instance) {
      // Détection device
      const ua = navigator.userAgent;
      const isMobile = /Mobi|Android|iPhone/i.test(ua);
      const isTablet = /iPad|Tablet|PlayBook/i.test(ua) || (isMobile && window.innerWidth >= 768);
      const deviceType = isTablet ? 'tablet' : (isMobile ? 'mobile' : 'desktop');

      // Page view initial
      posthog.capture('page_viewed', {
        view: 'timeline',
        device_type: deviceType,
        referrer: document.referrer || 'direct',
        url: window.location.href,
        pathname: window.location.pathname,
        screen_width: window.innerWidth,
        $current_url: window.location.href
      });
    }
  });
</script>
```

### 3.2 Bandeau cookie RGPD (à ajouter dans `index.html`)

```html
<!-- Cookie Consent Banner -->
<div id="cookie-banner" class="hidden fixed bottom-6 left-1/2 -translate-x-1/2 z-[200] w-[calc(100%-2rem)] max-w-lg bg-white rounded-2xl shadow-2xl border border-slate-200 p-6 animate-slide-up">
  <p class="text-sm text-slate-700 mb-4 leading-relaxed">
    🔭 Nous utilisons un outil de mesure d'audience <strong>anonyme</strong> pour comprendre comment ce site est utilisé et l'améliorer.
    <br><span class="text-xs text-slate-500">Aucune donnée personnelle n'est collectée sans votre accord.</span>
  </p>
  <div class="flex gap-3">
    <button id="cookie-accept" class="flex-1 bg-slate-900 text-white text-sm font-bold py-3 rounded-xl hover:bg-slate-800 transition-all">
      Accepter
    </button>
    <button id="cookie-refuse" class="flex-1 bg-slate-100 text-slate-600 text-sm font-medium py-3 rounded-xl hover:bg-slate-200 transition-all">
      Refuser
    </button>
  </div>
</div>

<script>
(function() {
  // Vérifier si le consentement a déjà été donné
  const consent = localStorage.getItem('ph_consent');
  if (consent === 'granted' || consent === 'denied') return;

  // Afficher le bandeau
  const banner = document.getElementById('cookie-banner');
  if (!banner) return;
  banner.classList.remove('hidden');

  document.getElementById('cookie-accept').addEventListener('click', function() {
    localStorage.setItem('ph_consent', 'granted');
    posthog.set_config({ persistence: 'localStorage+cookie' });
    posthog.capture('cookie_consent_granted');
    banner.classList.add('hidden');
  });

  document.getElementById('cookie-refuse').addEventListener('click', function() {
    localStorage.setItem('ph_consent', 'denied');
    posthog.set_config({ persistence: 'memory' });
    banner.classList.add('hidden');
    // Optionnel : désactiver complètement
    // posthog.opt_out_capturing();
  });
})();
</script>
```

### 3.3 Appels `posthog.capture()` dans `app.js`

Voici les modifications précises à apporter à `app.js`. Chaque appel est **wrappé dans un helper `safeCapture`** pour éviter les crashs si PostHog n'est pas chargé.

**Ajouter en tête du fichier (après `console.log` ligne 1) :**

```javascript
// ── PostHog Safe Capture Helper ──
function phCapture(eventName, properties = {}) {
  try {
    if (typeof posthog !== 'undefined' && posthog.capture) {
      posthog.capture(eventName, properties);
    }
  } catch (e) {
    // Silencieux — ne jamais casser le site à cause du tracking
  }
}
```

| Ligne actuelle | Code à insérer après | Événement |
|----------------|----------------------|-----------|
| Après ligne 54 (`console.log("✅ Données reçues:", t)`) | `phCapture('data_loaded', { council_count: (t.councils || []).length, deliberation_count: (t.councils || []).reduce((s,c) => s + (c.deliberations||[]).length, 0), load_time_ms: performance.now(), data_version: t.generated_at || 'unknown' });` | `data_loaded` |
| Dans le `catch` ligne 71 (`console.error("❌ Erreur Init:", e)`) | `phCapture('data_load_failed', { error_message: e.message, http_status: e.status || 'network', load_time_ms: performance.now() });` | `data_load_failed` |
| Après le `if (!e.ok)` ligne 52 | (voir ci-dessous — à intercaler dans le bloc try) | `data_load_failed` |
| Dans `render()`, après le calcul de `filteredCouncils` (ligne 299) | Détection `filteredCouncils.length === 0` pour `search_no_results` (cf. section dédiée) | `search_no_results` |
| Dans `handleSearch()` (ligne 224), après le `setTimeout` | `phCapture('search_performed', { query_length: searchQuery.length, source: e.target.id === 'mobile-search-input' ? 'mobile' : 'navbar' });` | `search_performed` — attention, ne pas capturer le terme lui-même (RGPD) |
| Dans `handleTopicClick()` (ligne 278) | `phCapture('topic_filter_clicked', { topic: tag, was_active: selectedTopic === tag });` | `topic_filter_clicked` |
| Dans `toggleDelib()` (ligne 532), quand `!a` (ouverture) | Extraire les data depuis le DOM | `deliberation_expanded` |
| Dans `loadMore()` (ligne 225) | `phCapture('load_more_clicked', { currently_visible: visibleCouncilsCount - 2, remaining: allCouncils.length - visibleCouncilsCount + 2 });` | `load_more_clicked` |
| Dans `toggleView()` (ligne 544) | `phCapture('view_toggled', { target_view: viewName, previous_view: viewName === 'budget' ? 'timeline' : 'budget' });` | `view_toggled` |
| Dans le handler submit newsletter (ligne 749) | Dans le `then` : `phCapture('newsletter_form_submitted', { success: true, email_domain: email.split('@')[1] || 'unknown' });` — Dans le `catch` : `phCapture('newsletter_form_submitted', { success: false, error_type: 'network' });` et `phCapture('api_newsletter_failed', { error_type: 'network' });` | `newsletter_form_submitted` + `api_newsletter_failed` |
| Dans le handler submit contact (ligne 745) | Même logique | `contact_form_submitted` + `api_contact_failed` |
| Dans `toggleAboutModal()`, `toggleTeamModal()`, `toggleContactModal()` (lignes 40-42) | Quand `e === true` : `phCapture('modal_opened', { modal_name: 'about', source: 'navbar' });` etc. | `modal_opened` |
| Dans le handler clic PDF (liens `<a>` externes) | Intercepter les clics sur `a[href$=".pdf"]` | `pdf_link_clicked` |
| Dans `toggleMobileMenu()` (ligne 43) | `phCapture('mobile_menu_toggled', { action: e ? 'open' : 'close' });` | `mobile_menu_toggled` |

### 3.4 Code complet des appels à copier-coller

```javascript
// ═══════════════════════════════════════════════════════════
// TRACKING POSTHOG — À INSÉRER DANS app.js
// ═══════════════════════════════════════════════════════════

// >>> 1. AJOUTER EN TÊTE DE FICHIER (après la ligne 1) <<<
function phCapture(eventName, properties = {}) {
  try {
    if (typeof posthog !== 'undefined' && posthog.capture) {
      posthog.capture(eventName, properties);
    }
  } catch (e) {}
}

// >>> 2. DANS init(), remplacer le bloc try/catch par : <<<
async function init() {
    console.log("🔍 Chargement des données...");
    const startTime = performance.now();
    try {
        const e = await fetch(`./data.json?v=${new Date().getTime()}`);
        if (!e.ok) {
            phCapture('data_load_failed', {
                http_status: e.status,
                error_message: `HTTP ${e.status}`,
                load_time_ms: performance.now() - startTime
            });
            throw new Error(`HTTP Error: ${e.status}`);
        }
        const t = await e.json();
        console.log("✅ Données reçues:", t);
        phCapture('data_loaded', {
            council_count: (t.councils || []).length,
            deliberation_count: (t.councils || []).reduce((s,c) => s + (c.deliberations||[]).length, 0),
            load_time_ms: Math.round(performance.now() - startTime),
            data_version: t.generated_at || 'unknown'
        });

        if (t.next_council_date) { /* ... inchangé ... */ }

        allCouncils = (t.councils || []).filter(c => !c.category || c.category === "Conseil municipal").sort((e, t) => t.date.localeCompare(e.date));

        // Détection conseils vides
        if (allCouncils.length === 0) {
            phCapture('data_empty', {
                raw_data_length: (t.councils || []).length,
                councils_count: 0
            });
        }

        console.log("📊 Nombre de conseils:", allCouncils.length);
        updateStats();
        renderTopicChips();
        render();
    } catch (e) {
        console.error("❌ Erreur Init:", e);
        phCapture('data_load_failed', {
            error_message: e.message || 'Unknown',
            load_time_ms: Math.round(performance.now() - startTime)
        });
    } finally { /* ... inchangé ... */ }
}

// >>> 3. Dans handleSearch(), après le setTimeout (ligne 224) : <<<
function handleSearch(e) {
    /* ... début inchangé ... */
    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => {
        searchQuery = e.toLowerCase().trim();
        visibleCouncilsCount = (searchQuery || selectedTopic) ? 5 : 2;
        render();

        // ── PostHog: recherche effectuée ──
        // On capture APRÈS le render pour avoir le résultat
        const resultsCount = document.querySelectorAll('.council-group').length;
        phCapture('search_performed', {
            query_length: searchQuery.length,
            results_count: resultsCount,
            empty: resultsCount === 0,
            source: e.currentTarget?.id === 'mobile-search-input' ? 'mobile' : e.target?.id === 'mobile-search-input' ? 'mobile' : 'navbar'
        });
        if (searchQuery.length > 0 && resultsCount === 0) {
            phCapture('search_no_results', {
                query_length: searchQuery.length,
                active_topic: selectedTopic || 'none',
                total_councils_available: allCouncils.length
            });
        }
    }, 300);
}

// >>> 4. Dans handleTopicClick() (ligne 278) : <<<
function handleTopicClick(tag) {
    phCapture('topic_filter_clicked', {
        topic: tag || 'Tous',
        was_active: selectedTopic === tag
    });
    selectedTopic = tag;
    visibleCouncilsCount = (searchQuery || selectedTopic) ? 5 : 2;
    renderTopicChips();
    render();
}

// >>> 5. Dans toggleDelib() (ligne 532), quand on OUVRE : <<<
function toggleDelib(e) {
    const t = document.getElementById(`content-${e}`),
          n = document.getElementById(`icon-${e}`),
          s = document.getElementById(`btn-${e}`);
    if (!t || !n || !s) return;
    const a = t.classList.contains("is-open");
    t.classList.toggle("is-open"), n.classList.toggle("rotate-180"), s.setAttribute("aria-expanded", a ? "false" : "true");

    // ── PostHog: ouverture délibération ──
    if (!a) {
        const container = document.querySelector(`[data-delib-id="${e}"]`);
        const topicEl = container?.querySelector('.text-slate-600.uppercase');
        const hasBudget = container?.querySelector('[class*="bg-slate-100"][class*="text-slate-700"]') !== null;
        phCapture('deliberation_expanded', {
            deliberation_id: e,
            topic_tag: topicEl?.textContent?.trim() || 'unknown',
            has_vote: container?.querySelector('.bg-emerald-50, .bg-rose-50') !== null,
            has_budget_impact: hasBudget,
            has_analysis: container?.querySelector('.bg-brand-50') !== null
        });
    }
}

// >>> 6. Dans loadMore() (ligne 225) : <<<
function loadMore() {
    phCapture('load_more_clicked', {
        currently_visible: visibleCouncilsCount,
        remaining: allCouncils.length - visibleCouncilsCount,
        total_filtered: visibleCouncilsCount
    });
    visibleCouncilsCount += 2;
    render();
}

// >>> 7. Dans toggleView() (ligne 544) : <<<
function toggleView(viewName) {
    const prevView = document.getElementById('budget-view')?.classList.contains('hidden') ? 'timeline' : 'budget';
    phCapture('view_toggled', { target_view: viewName, previous_view: prevView });
    /* ... reste inchangé ... */
}

// >>> 8. Newsletter submit (remplacer le bloc ligne 749) : <<<
newsletterForm && newsletterForm.addEventListener("submit", async e => {
    e.preventDefault();
    const email = newsletterEmail.value;
    try {
        e.submitter && (e.submitter.disabled = !0);
        const n = await fetch("https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/subscribe", {
            method: "POST", headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ email })
        });
        const s = await n.json().catch(() => ({}));
        if (n.ok) {
            phCapture('newsletter_form_submitted', { success: true, email_domain: email.split('@')[1] || 'unknown' });
            newsletterForm.classList.add("hidden");
            newsletterConfirmEmail.textContent = email;
            newsletterConfirm.classList.remove("hidden");
        } else {
            phCapture('newsletter_form_submitted', { success: false, error_type: 'http', http_status: n.status });
            phCapture('api_newsletter_failed', { error_type: 'http', http_status: n.status });
            newsletterStatus.textContent = s.error || "Oups ! Une erreur est survenue lors de l'inscription...";
            newsletterStatus.className = "text-sm font-medium text-center py-3 px-4 rounded-xl text-rose-400 bg-rose-400/10 border border-rose-400/20 block";
        }
    } catch (err) {
        phCapture('newsletter_form_submitted', { success: false, error_type: 'network' });
        phCapture('api_newsletter_failed', { error_type: 'network' });
        newsletterStatus.textContent = "Nous n'avons pas réussi à vous inscrire...";
        newsletterStatus.className = "text-sm font-medium text-center py-3 px-4 rounded-xl text-rose-400 bg-rose-400/10 border border-rose-400/20 block";
    } finally {
        e.submitter && (e.submitter.disabled = !1);
    }
});

// >>> 9. Contact submit (remplacer le bloc ligne 745) : <<<
contactForm && contactForm.addEventListener("submit", async e => {
    e.preventDefault();
    const t = {
        name: document.getElementById("contact-name").value,
        email_sender: document.getElementById("contact-email").value,
        message: document.getElementById("contact-message").value
    };
    try {
        e.submitter && (e.submitter.disabled = !0);
        const n = await fetch("https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/contact", {
            method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(t)
        });
        const s = await n.json().catch(() => ({}));
        if (n.ok) {
            phCapture('contact_form_submitted', { success: true });
            contactStatus.textContent = "Message envoyé avec succès.";
            contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-green-600 bg-green-50 block";
            contactForm.reset();
        } else {
            phCapture('contact_form_submitted', { success: false, error_type: 'http', http_status: n.status });
            phCapture('api_contact_failed', { error_type: 'http', http_status: n.status });
            contactStatus.textContent = s.error || "Erreur lors de l'envoi.";
            contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
        }
    } catch (err) {
        phCapture('contact_form_submitted', { success: false, error_type: 'network' });
        phCapture('api_contact_failed', { error_type: 'network' });
        contactStatus.textContent = "Erreur réseau. Impossible de contacter le serveur.";
        contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
    } finally {
        e.submitter && (e.submitter.disabled = !1);
    }
});

// >>> 10. Modales (remplacer toggleAboutModal, toggleTeamModal, toggleContactModal) : <<<
function toggleAboutModal(e) {
    if (e) lastFocusedElement = document.activeElement;
    const t = document.getElementById("about-modal"),
          n = document.getElementById("about-modal-body"),
          s = document.getElementById("about-scroll-indicator");
    t.classList.toggle("hidden", !e);
    document.body.classList.toggle("modal-open", e);
    if (e) {
        phCapture('modal_opened', { modal_name: 'about', source: lastFocusedElement?.closest('#mobile-menu') ? 'mobile_menu' : 'navbar' });
        n && s && (n.scrollTop = 0, s.style.opacity = "1", n.dataset.hasScrollListener || (n.addEventListener("scroll", () => {
            if (n.scrollTop > 10) { s.style.opacity = "0"; s.style.pointerEvents = "none"; }
            else { s.style.opacity = "1"; s.style.pointerEvents = "auto"; }
        }, { passive: true }), n.dataset.hasScrollListener = "true"));
    }
    if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; }
}

function toggleTeamModal(e) {
    if (e) lastFocusedElement = document.activeElement;
    const t = document.getElementById("team-modal"),
          n = document.getElementById("team-modal-body"),
          s = document.getElementById("team-scroll-indicator");
    t.classList.toggle("hidden", !e);
    document.body.classList.toggle("modal-open", e);
    if (e) {
        phCapture('modal_opened', { modal_name: 'team', source: lastFocusedElement?.closest('#mobile-menu') ? 'mobile_menu' : 'navbar' });
        n && s && (n.scrollTop = 0, s.style.opacity = "1", n.dataset.hasScrollListener || (n.addEventListener("scroll", () => {
            n.scrollTop > 50 ? s.style.opacity = "0" : s.style.opacity = "1"
        }), n.dataset.hasScrollListener = "true"));
    }
    if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; }
}

function toggleContactModal(e) {
    if (e) lastFocusedElement = document.activeElement;
    document.getElementById("contact-modal").classList.toggle("hidden", !e);
    document.body.classList.toggle("modal-open", e);
    if (e) {
        phCapture('modal_opened', { modal_name: 'contact', source: lastFocusedElement?.closest('#mobile-menu') ? 'mobile_menu' : 'navbar' });
    }
    if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; }
}

// >>> 11. Menu mobile : remplacer toggleMobileMenu : <<<
function toggleMobileMenu(e) {
    if (e) lastFocusedElement = document.activeElement;
    document.getElementById("mobile-menu").classList.toggle("hidden", !e);
    document.body.classList.toggle("modal-open", e);
    phCapture('mobile_menu_toggled', { action: e ? 'open' : 'close' });
    e ? setTimeout(() => { document.getElementById("mobile-search-input")?.focus() }, 100)
      : (document.getElementById("mobile-search-input")?.blur(),
         lastFocusedElement && (lastFocusedElement.focus(), lastFocusedElement = null));
}

// >>> 12. Clic sur PDF : ajouter un event listener global dans init() : <<<
// (À la fin de la fonction init(), juste avant le finally)
document.addEventListener('click', function(ev) {
    const link = ev.target.closest('a[href$=".pdf"]');
    if (link) {
        const delibContainer = link.closest('[data-delib-id]');
        phCapture('pdf_link_clicked', {
            deliberation_id: delibContainer?.dataset?.delibId || 'unknown',
            source_location: link.closest('#budget-view') ? 'budget_detail' :
                             link.closest('.bg-amber-50') ? 'adjustment' : 'delib_row',
            pdf_url_domain: new URL(link.href).hostname
        });
    }
    const extLink = ev.target.closest('a[href*="mairie-begles.fr"]');
    if (extLink && !extLink.href.endsWith('.pdf')) {
        phCapture('external_link_clicked', { link_url: extLink.href });
    }
});

// >>> 13. Scroll depth (dans init(), après le chargement) : <<<
(function trackScrollDepth() {
    const depths = [25, 50, 75, 100];
    const fired = new Set();
    let ticking = false;

    window.addEventListener('scroll', () => {
        if (!ticking) {
            window.requestAnimationFrame(() => {
                const scrollPercent = Math.round(
                    (window.scrollY + window.innerHeight) / document.documentElement.scrollHeight * 100
                );
                depths.forEach(d => {
                    if (scrollPercent >= d && !fired.has(d)) {
                        fired.add(d);
                        const currentView = document.getElementById('budget-view')?.classList.contains('hidden') ? 'timeline' : 'budget';
                        phCapture('scroll_depth_reached', { depth_percent: d, view: currentView });
                    }
                });
                ticking = false;
            });
            ticking = true;
        }
    }, { passive: true });
})();

// >>> 14. Global error handler (à la fin de app.js) : <<<
window.addEventListener('error', function(e) {
    if (e.filename && e.filename.includes('posthog')) return; // Ignorer les erreurs de PostHog lui-même
    phCapture('render_error', {
        error_message: e.message,
        stack_trace: e.error?.stack?.substring(0, 500) || 'none',
        file: e.filename?.split('/').pop() || 'unknown',
        line: e.lineno,
        column: e.colno
    });
});

// >>> 15. Détection slow load (ajouter dans le bloc data_loaded) : <<<
// Déjà géré via load_time_ms dans data_loaded — un dashboard PostHog peut
// créer une alerte si load_time_ms > 3000
```

---

## 4. Dashboard PostHog recommandé

### Dashboard "Vue d'ensemble" (P0)
- **KPIs** : Visiteurs uniques, pages vues, durée moyenne de session
- **Funnel** : Page vue → Filtre thématique → Délibération ouverte → PDF cliqué
- **Répartition** : Par device, par referrer, par vue (timeline vs budget)

### Dashboard "Contenu" (P1)
- Top 10 thèmes cliqués (topic_filter_clicked)
- Top 20 délibérations ouvertes
- Taux de recherche avec/sans résultat
- Taux de clic PDF par délibération

### Dashboard "Conversions" (P1)
- Taux de soumission newsletter
- Taux de soumission formulaire contact
- Funnel: Page vue → Scroll 50% → Newsletter

### Dashboard "Santé technique" (P0)
- Nombre de `data_load_failed` (alerte si > 0)
- Nombre de `data_empty` (alerte si > 0)
- Nombre de `api_contact_failed` / `api_newsletter_failed` (alerte si > 0)
- Nombre de `render_error` (alerte si > 0)
- Percentile 95 du `load_time_ms`

---

## 5. Synthèse des priorités

| Priorité | Événements Usage | Événements Dysfonctionnement | Total |
|----------|-----------------|------------------------------|-------|
| **P0** | `page_viewed`, `data_loaded`, `search_performed`, `topic_filter_clicked`, `deliberation_expanded`, `view_toggled` | `data_load_failed`, `data_empty`, `api_contact_failed`, `api_newsletter_failed` | **10** |
| **P1** | `load_more_clicked`, `newsletter_form_submitted`, `contact_form_submitted`, `modal_opened`, `pdf_link_clicked` | `render_error`, `slow_load`, `api_contact_failed` (déjà P0) | **8** |
| **P2** | `scroll_depth_reached`, `vote_detail_viewed`, `mobile_menu_toggled`, `external_link_clicked` | `pdf_link_broken`, `council_with_no_deliberations`, `broken_image`, `unexpected_data_shape` | **8** |
| **TOTAL** | **15 événements** | **8 événements** | **23** |

---

## 6. Checklist de mise en œuvre

- [ ] Créer un projet PostHog sur `eu.i.posthog.com` (🇪🇺 RGPD)
- [ ] Récupérer la clé API `PHC_***`
- [ ] Insérer le snippet dans `index.html` (avant `</head>`)
- [ ] Ajouter le bandeau cookie RGPD dans `index.html` (avant `</body>`)
- [ ] Ajouter le helper `phCapture()` en tête de `app.js`
- [ ] Instrumenter les 6 événements P0 usage
- [ ] Instrumenter les 4 événements P0 dysfonctionnement
- [ ] Instrumenter les événements P1
- [ ] Instrumenter les événements P2 (optionnel, sprint suivant)
- [ ] Configurer les alertes dans PostHog sur les événements marqués "OUI"
- [ ] Tester en local : ouvrir la console, vérifier que les appels `posthog.capture()` partent bien
- [ ] Déployer en production
- [ ] Vérifier les dashboards après 48h de données réelles
