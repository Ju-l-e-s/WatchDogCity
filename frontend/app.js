console.log("🚀 L'Observatoire Citoyen : Initialisation...");
let searchTimeout, allCouncils = [], visibleCouncilsCount = 2, searchQuery = "", selectedTopic = null;
const GLOBAL_GLOSSARY = {
    CCAS: "Centre Communal d'Action Sociale",
    ZAC: "Zone d'Aménagement Concerté",
    PLU: "Plan Local d'Urbanisme",
    EPCI: "Établissement Public de Coopération Intercommunale",
    DSP: "Délégation de Service Public",
    AFL: "Agence France Locale",
    CREPAQ: "Centre de Ressources d'Écologie Pédagogique en Nouvelle-Aquitaine",
    BM: "Bordeaux Métropole",
    ALSH: "Accueil de Loisirs Sans Hébergement",
    PADD: "Projet d'Aménagement et de Développement Durable",
    SCOT: "Schéma de Cohérence Territoriale",
    CLSPD: "Conseil Local de Sécurité et de Prévention de la Délinquance",
    ERP: "Établissement Recevant du Public",
    QPV: "Quartier Prioritaire de la Politique de la Ville",
    AAP: "Appel à Projets",
    AMI: "Appel à Manifestation d'Intérêt",
    SIAE: "Structure de l'Insertion par l'Activité Économique",
    SIAL: "Système d'Information pour l'Accueil et le Logement"
};
const COLORS = {
    'Éducation': '#3b82f6',     // Blue 500
    'Culture': '#f59e0b',       // Amber 500
    'Administration': '#94a3b8', // Slate 400
    'Social': '#6366f1',        // Indigo 500
    'Budget': '#8b5cf6',        // Violet 500
    'Environnement': '#10b981',  // Emerald 500
    'Urbanisme': '#f97316',     // Orange 500
    'Sécurité': '#ef4444',      // Red 500
    'Sport': '#ec4899',         // Pink 500
    'Mobilité': '#06b6d4',      // Cyan 500
    'Autres': '#cbd5e1'
};
const loadStartTime = performance.now();
const scrollDepthTracked = {25: false, 50: false, 75: false, 100: false};

function phCapture(eventName, props = {}) {
  try {
    if (window.phReady && window.ph && !localStorage.getItem('watchdogcity-cookies-refused')) {
      window.ph.capture(eventName, props);
    }
  } catch(e) { /* fail silently */ }
}

function escapeRegExp(e) { return e.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") }
function escapeHTML(e) { if (!e) return ""; const t = document.createElement("div"); return t.textContent = e, t.innerHTML }
let lastFocusedElement = null;
function trapFocus(e){const t='a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])',n=e.querySelectorAll(t),s=n[0],a=n[n.length-1];s&&a&&e.addEventListener('keydown',function(e){if(e.key==='Tab'){if(e.shiftKey){if(document.activeElement===s){e.preventDefault();a.focus()}}else{if(document.activeElement===a){e.preventDefault();s.focus()}}}})}
function toggleAboutModal(e) { if (e) { lastFocusedElement = document.activeElement; phCapture('modal_opened', {modal: 'about'}); } const t = document.getElementById("about-modal"), n = document.getElementById("about-modal-body"), s = document.getElementById("about-scroll-indicator"); t.classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e&&trapFocus(t), e && n && s && (n.scrollTop = 0, s.style.opacity = "1", n.dataset.hasScrollListener || (n.addEventListener("scroll", () => { console.log("Scroll About:", n.scrollTop); if (n.scrollTop > 10) { s.style.opacity = "0"; s.style.pointerEvents = "none"; } else { s.style.opacity = "1"; s.style.pointerEvents = "auto"; } }, { passive: true }), n.dataset.hasScrollListener = "true")); if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; } }
function toggleTeamModal(e) { if (e) { lastFocusedElement = document.activeElement; phCapture('modal_opened', {modal: 'team'}); } const t = document.getElementById("team-modal"), n = document.getElementById("team-modal-body"), s = document.getElementById("team-scroll-indicator"); t.classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e&&trapFocus(t), e && n && s && (n.scrollTop = 0, s.style.opacity = "1", n.dataset.hasScrollListener || (n.addEventListener("scroll", () => { n.scrollTop > 50 ? s.style.opacity = "0" : s.style.opacity = "1" }), n.dataset.hasScrollListener = "true")); if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; } }
function toggleContactModal(e) { if (e) { lastFocusedElement = document.activeElement; phCapture('modal_opened', {modal: 'contact'}); } const t=document.getElementById("contact-modal"); t.classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e&&trapFocus(t); if (!e && lastFocusedElement) { lastFocusedElement.focus(); lastFocusedElement = null; } }
function toggleMobileMenu(e) { if (e) lastFocusedElement = document.activeElement; document.getElementById("mobile-menu").classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e ? setTimeout(() => { document.getElementById("mobile-search-input")?.focus() }, 100) : (document.getElementById("mobile-search-input")?.blur(), lastFocusedElement && (lastFocusedElement.focus(), lastFocusedElement = null)) }
function smartCapitalize(e) { if (!e) return ""; return e.split(" ").map((e, t) => { if (e.length > 1 && e === e.toUpperCase() && !/^[0-9]+$/.test(e)) return e; const n = e.toLowerCase(); return 0 === t ? n.charAt(0).toUpperCase() + n.slice(1) : n }).join(" ") }
function applyAcronyms(e, t) { const n = { ...GLOBAL_GLOSSARY, ...t || {} }; if (0 === Object.keys(n).length) return e; let s = e; return Object.keys(n).sort((e, t) => t.length - e.length).forEach(e => { const t = escapeHTML(n[e]), a = new RegExp(`\\b${e}\\b`, "g"); s = s.replace(a, `<abbr title="${t}" class="cursor-help border-b border-dotted border-brand-300 decoration-brand-300 decoration-2 underline-offset-4" tabindex="0">${e}</abbr>`) }), s }
function highlightText(e, t, n = null) { let s = escapeHTML(e); if (n && (s = applyAcronyms(s, n)), !t) return s; const a = escapeRegExp(escapeHTML(t)), r = new RegExp(`(${a})`, "gi"); return s.split(/(<[^>]+>)/g).map(e => e.startsWith("<") ? e : e.replace(r, '<mark class="bg-brand-100 text-brand-700 font-bold px-0.5 rounded">$1</mark>')).join("") }

async function init() {
    console.log("🔍 Chargement des données...");
    try {
        const e = await fetch(`./data.json?v=${new Date().getTime()}`);
        if (!e.ok) { throw new Error(`HTTP Error: ${e.status}`) }
        const t = await e.json();
        console.log("✅ Données reçues:", t);

        if (t.next_council_date) {
            const e = document.getElementById("next-council-date");
            if(e) e.textContent = t.next_council_date;
            const n = document.getElementById("next-council-block");
            if(n) n.classList.remove("hidden");
        }

        allCouncils = (t.councils || []).filter(c => !c.category || c.category === "Conseil municipal").sort((e, t) => t.date.localeCompare(e.date));

        console.log("📊 Nombre de conseils:", allCouncils.length);

        phCapture('data_loaded', {
          council_count: allCouncils.length,
          deliberation_count: allCouncils.reduce((a, c) => a + (c.deliberations?.length || 0), 0),
          load_time_ms: Math.round(performance.now() - loadStartTime)
        });
        phCapture('page_viewed', {view: 'timeline'});

        updateStats();
        renderTopicChips();
        render();
        if (window.location.hash && window.location.hash.startsWith('#delib-')) {
            const id = window.location.hash.substring(7);
            if (!document.querySelector(`[data-delib-id="${id}"]`)) {
                visibleCouncilsCount = 999;
                render();
            }
            setTimeout(() => scrollToAndOpenDelib(id), 100);
        }
    } catch (e) {
        console.error("❌ Erreur Init:", e);
        const httpStatus = e.message.startsWith('HTTP Error:') ? parseInt(e.message.split(':')[1].trim()) : 0;
        phCapture('data_load_failed', {error: e.message, status: httpStatus});
    } finally {
        const loader = document.getElementById("global-loader");
        if (loader) {
            loader.style.opacity = "0";
            loader.style.pointerEvents = "none";
            setTimeout(() => loader.remove(), 500);
        }
    }
}

function getBudgetData() {
    let primitifDelib = null;
    let maxBreakdownItems = 0;
    
    // 1. Trouver le Budget Primitif (délibération avec le plus grand breakdown thématique et contenant VOTE et BUDGET dans son titre)
    allCouncils.forEach(c => {
        (c.deliberations || []).forEach(d => {
            const titleUpper = (d.title || "").toUpperCase();
            const breakdown = d.budget_breakdown;
            if (breakdown && breakdown.length > maxBreakdownItems && titleUpper.includes("VOTE") && titleUpper.includes("BUDGET")) {
                primitifDelib = d;
                maxBreakdownItems = breakdown.length;
            }
        });
    });

    let totalBudget = 0;
    const categories = {};
    const otherFinancialDelibs = [];
    const adjustments = [];

    // 2. Traiter le Budget Primitif s'il existe
    if (primitifDelib) {
        (primitifDelib.budget_breakdown || []).forEach(item => {
            if (!item.amount || item.amount <= 0) return;
            totalBudget += item.amount;
            const theme = item.topic_tag || 'Administration';
            categories[theme] = (categories[theme] || 0) + item.amount;
        });
    }

    const ADJUSTMENT_KEYWORDS = [
        "SUPPLÉMENTAIRE", "AFFECTATION DES RÉSULTATS", "CFU", 
        "FINANCIER UNIQUE", "MUTUALISATION", "NIVEAUX DE SERVICES", "AP/CP"
    ];

    // 3. Classer les autres délibérations financières
    allCouncils.forEach(c => {
        (c.deliberations || []).forEach(d => {
            if (primitifDelib && d.id === primitifDelib.id) return;
            
            const titleUpper = (d.title || "").toUpperCase();
            const impact = d.budget_impact || 0;
            const hasBreakdown = d.budget_breakdown && d.budget_breakdown.length > 0;
            
            if (impact > 0 || hasBreakdown) {
                const isAdj = ADJUSTMENT_KEYWORDS.some(kw => titleUpper.includes(kw));
                if (isAdj) {
                    adjustments.push({
                        title: d.title,
                        amount: impact || d.budget_breakdown.reduce((sum, item) => sum + (item.amount || 0), 0),
                        date: c.date,
                        pdf: d.pdf_url
                    });
                } else {
                    otherFinancialDelibs.push({
                        ...d,
                        council_date: c.date
                    });
                }
            }
        });
    });

    return {
        primitifDelib,
        totalBudget,
        categories,
        otherFinancialDelibs,
        adjustments
    };
}

function renderDashboard() {
    const container = document.getElementById('global-dashboard');
    if (!container) return;

    const budgetData = getBudgetData();
    const totalBudget = budgetData.totalBudget;
    const categories = budgetData.categories;

    if (totalBudget === 0) { container.classList.add('hidden'); return; }
    container.classList.remove('hidden');

    const sortedCats = Object.entries(categories).sort((a, b) => b[1] - a[1]);
    
    let ribbonHtml = '<div class="ribbon-bar flex h-3 bg-slate-100 rounded-full overflow-hidden mb-4 shadow-inner" role="progressbar" aria-label="Répartition du budget">';
    sortedCats.forEach(([name, val]) => {
        const pct = totalBudget > 0 ? (val / totalBudget * 100).toFixed(1) : 0;
        const color = COLORS[name] || COLORS['Autres'];
        ribbonHtml += `<div class="ribbon-segment" style="width: ${pct}%; background: ${color}" title="${name}: ${pct}%"><span class="sr-only">${name} : ${pct}%</span></div>`;
    });
    ribbonHtml += '</div>';

    let legendHtml = '<div class="flex flex-wrap gap-6 mb-8 p-6 bg-white rounded-2xl border border-slate-100 shadow-micro">';
    sortedCats.forEach(([name, val]) => {
        const color = COLORS[name] || COLORS['Autres'];
        legendHtml += `
            <div class="flex items-center gap-2.5">
                <span style="width: 14px; height: 14px; border-radius: 4px; background: ${color}; display: block; box-shadow: 0 1px 2px rgba(0,0,0,0.1);"></span>
                <span class="text-xs font-bold text-slate-600 uppercase tracking-wider">${name}</span>
            </div>`;
    });
    legendHtml += '</div>';

    let gridHtml = '<div class="categories-grid grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-4">';
    sortedCats.forEach(([name, val]) => {
        const pct = totalBudget > 0 ? (val / totalBudget * 100).toFixed(1) : 0;
        const color = COLORS[name] || COLORS['Autres'];
        gridHtml += `
            <div class="category-row p-4 bg-white rounded-2xl border border-slate-100 hover:shadow-micro transition-all">
                <div class="category-info flex justify-between mb-2">
                    <span class="category-name text-xs font-semibold text-slate-700">${name}</span>
                    <span class="category-val font-bold text-xs" style="color: ${color}">${formatBudget(val)}</span>
                </div>
                <div class="h-1 bg-slate-50 rounded-full overflow-hidden">
                    <div style="width: ${pct}%; height: 100%; background: ${color}" class="rounded-full"></div>
                </div>
            </div>`;
    });
    gridHtml += '</div>';

    container.innerHTML = `
        <div class="dashboard-container animate-fade-in mb-16">
            <div class="mb-8">
                <span class="dashboard-title flex items-center gap-2 text-[11px] font-black text-slate-400 uppercase tracking-[0.2em] mb-2">
                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2m0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z"></path></svg>
                    Budget de Référence ${budgetData.primitifDelib ? '2026' : ''}
                </span>
                <div class="budget-total-val text-4xl md:text-5xl font-black text-slate-900 tracking-tight">${formatBudget(totalBudget)}</div>
                ${budgetData.primitifDelib ? `<p class="text-xs text-slate-500 mt-2">Basé sur le Budget Primitif voté le ${formatDate(budgetData.primitifDelib.council_date || '2025-12-18')}</p>` : ''}
            </div>
            
            <h4 class="text-[10px] font-black text-slate-400 uppercase tracking-widest mb-4">Répartition thématique du Budget de Référence</h4>
            ${ribbonHtml}
            ${legendHtml}
            ${gridHtml}
        </div>
    `;
    console.log("✅ Dashboard rendu avec succès.");
}

function handleSearch(e) { const t = document.getElementById("nav-search-input"), n = document.getElementById("mobile-search-input"); t && t.value !== e && (t.value = e), n && n.value !== e && (n.value = e), clearTimeout(searchTimeout), searchTimeout = setTimeout(() => { searchQuery = e.toLowerCase().trim(), visibleCouncilsCount = (searchQuery || selectedTopic) ? 5 : 2, render() }, 300) }
function loadMore() { visibleCouncilsCount += 2, render(); phCapture('load_more_clicked', {visible_count: visibleCouncilsCount}); }
function updateStats() { const e = document.getElementById("stat-councils"), t = document.getElementById("stat-deliberations"); e && (e.textContent = allCouncils.length), t && (t.textContent = allCouncils.reduce((e, t) => e + (t.deliberations?.length || 0), 0)) }
function formatDate(e) { if (!e) return "Date inconnue"; try { const t = new Date(e); return isNaN(t.getTime()) ? e : t.toLocaleDateString("fr-FR", { day: "numeric", month: "long", year: "numeric" }) } catch (t) { return e } }

function formatBudget(val) {
    return val.toLocaleString('fr-FR', { maximumFractionDigits: 0 }) + " €";
}

// ── Topic Filter Chips ──

function getAvailableTopics() {
    const topics = new Set();
    allCouncils.forEach(c => {
        (c.deliberations || []).forEach(d => {
            if (d.topic_tag) topics.add(d.topic_tag);
        });
    });
    return [...topics].sort((a, b) => a.localeCompare(b, 'fr'));
}

function renderTopicChips() {
    const container = document.getElementById('topic-chips');
    if (!container) return;

    const topics = getAvailableTopics();

    let html = '';

    // "Tous" chip
    const isAllActive = selectedTopic === null;
    html += `<button onclick="handleTopicClick(null)" aria-pressed="${isAllActive}" class="chip shrink-0 px-4 py-2 rounded-full text-sm font-medium transition-all duration-200 ${
        isAllActive
            ? 'bg-slate-900 text-white shadow-micro'
            : 'bg-white border border-slate-200 text-slate-600 hover:bg-slate-50 hover:border-slate-300'
    }">Tous</button>`;

    // Individual topic chips
    topics.forEach(topic => {
        const isActive = selectedTopic === topic;
        const color = COLORS[topic] || COLORS['Autres'];
        html += `<button onclick="handleTopicClick('${topic.replace(/'/g, "\\\\'")}')" aria-pressed="${isActive}" class="chip shrink-0 px-4 py-2 rounded-full text-sm font-medium transition-all duration-200 flex items-center gap-2 ${
            isActive
                ? 'bg-slate-900 text-white shadow-micro'
                : 'bg-white border border-slate-200 text-slate-600 hover:bg-slate-50 hover:border-slate-300'
        }">
            <span class="w-2 h-2 rounded-full shrink-0" style="background-color: ${color}"></span>
            ${topic}
        </button>`;
    });

    container.innerHTML = html;
}

function handleTopicClick(tag) {
    selectedTopic = tag;
    visibleCouncilsCount = (searchQuery || selectedTopic) ? 5 : 2;
    if (tag) phCapture('topic_filter_clicked', {topic: tag});
    renderTopicChips();
    render();
}

function render() {
    const container = document.getElementById("timeline");
    if (!container) return;

    const filteredCouncils = allCouncils.map(council => {
        const matchingDelibs = (council.deliberations || []).filter(d => {
            const matchesSearch = searchQuery === "" || 
                d.title.toLowerCase().includes(searchQuery) || 
                d.summary.toLowerCase().includes(searchQuery) || 
                (d.topic_tag && d.topic_tag.toLowerCase().includes(searchQuery));
            const matchesTopic = selectedTopic === null || d.topic_tag === selectedTopic;
            return matchesSearch && matchesTopic;
        });
        return matchingDelibs.length > 0 ? { ...council, deliberations: matchingDelibs } : null;
    }).filter(c => c !== null);

    // Event: council_empty for councils with 0 deliberations
    allCouncils.forEach(c => {
      if (!c.deliberations || c.deliberations.length === 0) {
        phCapture('council_empty', {council_id: c.id});
      }
    });

    // Event: search_performed
    if (searchQuery) {
      const resultCount = filteredCouncils.reduce((a, c) => a + c.deliberations.length, 0);
      phCapture('search_performed', {result_count: resultCount, query_length: searchQuery.length});
    }

    // Event: search_no_results
    if (filteredCouncils.length === 0) {
      phCapture('search_no_results', {query_length: searchQuery.length, active_topic: selectedTopic || 'none'});
    }

    const visibleCouncils = filteredCouncils.slice(0, visibleCouncilsCount);
    container.innerHTML = "";

    visibleCouncils.forEach((council, index) => {
        const section = document.createElement("section");
        section.className = "council-group animate-slide-up mb-20";
        section.id = `council-group-${index}`;
        const dateStr = formatDate(council.date), delibCount = council.deliberations?.length || 0, title = escapeHTML(council.title);
        let rawSummary = (council.summary || "").replace(/\s+/g, " ").trim();
        if (!rawSummary || rawSummary === council.title || rawSummary.length < 10) rawSummary = `Ce conseil a traité ${delibCount} délibérations.`;

        const agendaHtml = council.agenda ? `<div class="badge-agenda"><svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"></path></svg>${council.agenda}</div>` : "";
        
        let councilRibbonHtml = '', councilLegendHtml = '';
        const councilTotal = council.analysis ? council.analysis.budget_impact : 0;
        if (councilTotal > 0) {
            // Use the ORIGINAL (unfiltered) council deliberations for the budget ribbon,
            // so percentages reflect the full council, not just the filtered subset.
            const originalCouncil = allCouncils.find(c => c.id === council.id);
            const budgetDelibs = originalCouncil ? (originalCouncil.deliberations || []) : (council.deliberations || []);
            const cats = {};
            let councilThematicTotal = 0;
            budgetDelibs.forEach(d => {
                if (d.budget_impact > 0 && d.topic_tag !== 'Budget') {
                    const cat = d.topic_tag || 'Autres';
                    cats[cat] = (cats[cat] || 0) + d.budget_impact;
                    councilThematicTotal += d.budget_impact;
                }
            });
            if (councilThematicTotal > 0) {
                const sortedCats = Object.entries(cats).sort((a, b) => b[1] - a[1]);
                councilRibbonHtml = '<div class="flex rounded-full overflow-hidden mt-5 bg-slate-100" style="height: 10px;" role="progressbar" aria-label="Répartition du budget">';
                sortedCats.forEach(([name, val]) => {
                    const pct = (val / councilThematicTotal * 100).toFixed(1);
                    councilRibbonHtml += `<div style="width:${pct}%;background:${COLORS[name]||COLORS['Autres']}" title="${name}: ${pct}%"><span class="sr-only">${name} : ${pct}%</span></div>`;
                });
                councilRibbonHtml += '</div>';
                councilLegendHtml = '<div class="flex flex-wrap mt-4 text-xs font-bold text-slate-500 leading-none" style="gap:20px;row-gap:12px;">';
                sortedCats.forEach(([name, val]) => {
                    const pct = Math.round(val / councilThematicTotal * 100);
                    const color = COLORS[name] || COLORS['Autres'];
                    councilLegendHtml += `<div class="flex items-center gap-2"><span class="mr-1" style="width:10px;height:10px;border-radius:2px;background:${color}"></span><span>${pct}% ${name}</span></div>`;
                });
                councilLegendHtml += '</div>';
            }
        }

        // Calculate vote climate from individual deliberations
        const allDelibs = council.deliberations || [];
        const votedDelibs = allDelibs.filter(d => d.vote && (d.vote.has_vote || d.vote.pour != null || d.vote.contre != null));
        const unanimousDelibs = votedDelibs.filter(d => (d.vote.contre || 0) === 0 && (d.vote.abstention || 0) === 0);
        const abstentionDelibs = votedDelibs.filter(d => (d.vote.contre || 0) === 0 && (d.vote.abstention || 0) > 0);
        const oppositionDelibs = votedDelibs.filter(d => (d.vote.contre || 0) > 0);
        
        // Stricter logic: Tension if any opposition OR ratio > 10%
        let totalVotes = 0, totalOpposition = 0;
        votedDelibs.forEach(d => {
            totalVotes += (d.vote.pour || 0) + (d.vote.contre || 0) + (d.vote.abstention || 0);
            totalOpposition += (d.vote.contre || 0) + (d.vote.abstention || 0);
        });
        const ratio = totalVotes > 0 ? totalOpposition / totalVotes : 0;
        const isConsensus = oppositionDelibs.length === 0 && ratio <= 0.10;
        
        const hasVotesFromDelibs = votedDelibs.length > 0;

        let analysisHtml = "";
        if (council.analysis) {
            const hasFinancial = council.analysis.budget_impact > 0;
            if (hasFinancial || hasVotesFromDelibs) {
                analysisHtml = `<div class="analysis-grid grid grid-cols-1 gap-4 mt-6">`;
                if (hasFinancial) {
                    analysisHtml += `<div class="analysis-card bg-slate-50/50 border border-slate-100 rounded-2xl p-6"><span class="block text-xs font-bold text-slate-400 uppercase tracking-widest mb-3">💰 Impact Financier</span><div class="text-2xl font-black text-slate-900">${council.analysis.budget_impact.toLocaleString("fr-FR")} €</div>${councilRibbonHtml}${councilLegendHtml}</div>`;
                }
                if (hasVotesFromDelibs) {
                    const badgeClasses = isConsensus
                        ? "bg-emerald-50 text-emerald-700 border-emerald-200"
                        : "bg-rose-50 text-rose-700 border-rose-200";
                    const iconSvg = isConsensus
                        ? `<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z"></path></svg>`
                        : `<svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2.5" d="M13 10V3L4 14h7v7l9-11h-7z"></path></svg>`;
                    const labelText = isConsensus ? "CONSENSUS" : "VOTES PARTAGÉS";

                    // Summary stats line
                    let summaryParts = [`<span class="font-bold text-slate-700">${votedDelibs.length}</span> délibération${votedDelibs.length > 1 ? "s" : ""} votée${votedDelibs.length > 1 ? "s" : ""}`];
                    if (unanimousDelibs.length > 0) summaryParts.push(`<span class="font-semibold text-emerald-600">${unanimousDelibs.length} unanime${unanimousDelibs.length > 1 ? "s" : ""}</span>`);
                    if (abstentionDelibs.length > 0) summaryParts.push(`<span class="font-semibold text-amber-500">${abstentionDelibs.length} avec abstention${abstentionDelibs.length > 1 ? "s" : ""}</span>`);
                    if (oppositionDelibs.length > 0) summaryParts.push(`<span class="font-semibold text-rose-600">${oppositionDelibs.length} avec opposition</span>`);
                    const summaryLine = summaryParts.join(' · ');

                    // Opposition detail rows
                    let oppositionRows = "";
                    if (oppositionDelibs.length > 0) {
                        const rows = oppositionDelibs.map(d => {
                            const totalD = (d.vote.pour || 0) + (d.vote.contre || 0) + (d.vote.abstention || 0);
                            const absText = (d.vote.abstention || 0) > 0 ? ` · <span class="text-amber-500">${d.vote.abstention} abstention${d.vote.abstention > 1 ? "s" : ""}</span>` : "";
                            const shortTitle = (d.title || "").length > 100 ? (d.title || "").substring(0, 100) + "…" : (d.title || "");
                            return `<div class="flex flex-col gap-0.5 py-2 border-t border-slate-100/80 first:border-t-0"><div class="flex items-center gap-2 flex-wrap"><span class="text-[10px] font-bold uppercase tracking-wider text-slate-400">${d.topic_tag || "Délibération"}</span><span class="text-[11px] font-bold text-rose-600">${d.vote.contre} contre</span><span class="text-[11px] text-slate-400">sur ${totalD}</span>${absText}</div><p class="text-[11px] text-slate-500 leading-snug">${shortTitle} <button onclick="scrollToAndOpenDelib('${d.id}')" class="text-brand-600 hover:text-brand-700 hover:underline font-semibold ml-1 cursor-pointer">voir la délibération →</button></p></div>`;
                        }).join("");
                        oppositionRows = `<div class="mt-3 pt-1">${rows}</div>`;
                    }

                    analysisHtml += `<div class="analysis-card bg-slate-50/50 border border-slate-100 rounded-2xl p-6"><span class="block text-[10px] font-black text-slate-400 uppercase tracking-widest mb-4">⚖️ Climat des Votes</span><div class="flex items-center gap-3 mb-4"><div class="inline-flex items-center gap-1.5 px-3 py-1.5 rounded-lg border font-bold text-xs tracking-wide uppercase shadow-sm ${badgeClasses}">${iconSvg} ${labelText}</div><span class="text-[11px] font-medium text-slate-400">Tendance du conseil</span></div><p class="text-[11px] text-slate-500 leading-relaxed">${summaryLine}</p>${oppositionRows}</div>`;
                }
                analysisHtml += `</div>`;
            }
        }
        
        const enjeuCleHtml = council.analysis && council.analysis.vote_summary ? 
            `<p class="text-slate-600 leading-relaxed max-w-4xl text-xl font-light mt-8"><span class="font-bold text-slate-900 mr-2">Enjeu Clé :</span>${council.analysis.vote_summary.replace("Enjeu Clé :", "").replace("Enjeu Clé", "")}</p>` : "";

        section.innerHTML = `
            <div class="flex items-center gap-6 mb-8">
                <div class="h-px flex-1 bg-slate-200/60"></div>
                <h2 class="text-[13px] font-semibold text-slate-500 uppercase tracking-[0.15em] bg-white px-6 py-2.5 rounded-full shadow-micro">${dateStr}</h2>
                <div class="h-px flex-1 bg-slate-200/60"></div>
            </div>
            <div class="bg-white rounded-[2rem] shadow-card overflow-hidden mt-5 md:mt-0">
                <div class="px-4 pt-5 pb-[10px] md:px-12 md:py-10 border-b border-slate-100/60">
                    ${agendaHtml}
                    <h3 class="text-2xl md:text-3xl font-bold text-slate-900 pt-4 md:pt-0 mb-4 tracking-tight">${title}</h3>
                    ${analysisHtml}
                    ${enjeuCleHtml}
                </div>
                <div class="divide-y divide-slate-100/50">${council.deliberations.map(d => renderDeliberationRow(d)).join("")}</div>
            </div>`;
        container.appendChild(section);
    });

    if (filteredCouncils.length > visibleCouncilsCount) {
        const remaining = filteredCouncils.length - visibleCouncilsCount;
        const loadMoreBtn = document.createElement("button");
        loadMoreBtn.id = "btn-load-more";
        loadMoreBtn.onclick = () => {
            const oldCount = visibleCouncilsCount;
            loadMore();
            const firstNewCouncil = document.getElementById(`council-group-${oldCount}`);
            if (firstNewCouncil) {
                firstNewCouncil.setAttribute("tabindex", "-1");
                firstNewCouncil.focus({ preventScroll: true });
            }
        };
        loadMoreBtn.className = "w-full py-5 bg-white rounded-2xl text-slate-600 hover:text-brand-600 font-medium text-sm tracking-wide transition-all shadow-micro group active:scale-[0.98] min-h-[44px] mb-12";
        loadMoreBtn.innerHTML = `<div class="flex items-center justify-center gap-2"><span>Voir plus</span><span class="text-xs text-slate-500 font-normal">(${remaining} restants)</span><svg class="w-4 h-4 opacity-40 group-hover:translate-y-0.5 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg></div>`;
        container.appendChild(loadMoreBtn);
    }
}

function renderDeliberationRow(e) {
    const t = `delib-${Math.random().toString(36).substr(2, 9)}`, 
          n = e.vote && (e.vote.has_vote || null != e.vote.pour || null != e.vote.contre || null != e.vote.abstention), 
          s = n ? (e.vote.pour || 0) + (e.vote.contre || 0) + (e.vote.abstention || 0) : 0, 
          a = n && s > 0 ? e.vote.pour / s * 100 : 0, 
          r = n && s > 0 && 0 === (e.vote.contre || 0) && 0 === (e.vote.abstention || 0), 
          o = highlightText(smartCapitalize(e.title), searchQuery, e.acronyms), 
          l = highlightText(e.summary, searchQuery, e.acronyms), 
          i = highlightText(e.topic_tag || "Délibération", searchQuery);
    
    // Badge budgétaire pour Option C
    let budgetBadge = '';
    if (e.budget_impact > 0) {
        let badgeClasses = 'bg-slate-100 text-slate-700 border-slate-200';
        let icon = '💰';
        let prefix = '';
        if (e.budget_type === 'DÉPENSE') { badgeClasses = 'bg-slate-50 text-slate-600 border-slate-200'; icon = '💰'; prefix = ''; }
        else if (e.budget_type === 'RECETTE') { badgeClasses = 'bg-emerald-50 text-emerald-700 border-emerald-200'; icon = '🟢'; prefix = '+'; }
        else if (e.budget_type === 'CAUTION') { badgeClasses = 'bg-amber-50 text-amber-700 border-amber-200'; icon = '🟠'; }
        budgetBadge = `<span class="inline-flex items-center whitespace-nowrap shrink-0 px-2 py-0.5 rounded text-[10px] font-bold ${badgeClasses} border ml-2 shadow-sm">${icon} ${prefix}${e.budget_impact.toLocaleString('fr-FR')} €</span>`;
    }
    let c = "";
    if (e.analysis_data) {
        // Détail budgétaire dans Éclairage pour Option C
        let budgetDetail = '';
        if (e.budget_impact > 0) {
            let badgeClasses = 'text-slate-600 bg-slate-100 border-slate-200'; // Fallback
            let icon = '💰';
            let typeLabel = e.budget_type || '';
        
            switch (e.budget_type) {
                case 'DÉPENSE':
                    badgeClasses = 'text-slate-600 bg-slate-50 border-slate-200';
                    icon = '💰';
                    break;
                case 'RECETTE':
                    badgeClasses = 'text-emerald-700 bg-emerald-50 border-emerald-200';
                    icon = '🟢';
                    break;
                case 'CAUTION':
                    badgeClasses = 'text-amber-700 bg-amber-50 border-amber-200';
                    icon = '🟠';
                    break;
            }
        
            const typeHtml = typeLabel ? `<span class="opacity-75 ml-1 hidden sm:inline">(${typeLabel.toLowerCase()})</span>` : '';
            
            budgetDetail = `
                <div class="mb-6">
                    <p class="text-[11px] font-semibold text-brand-600 uppercase tracking-widest mb-1.5">Impact Financier</p>
                    <div class="inline-flex items-center whitespace-nowrap shrink-0 gap-1.5 px-2.5 py-1 rounded border ${badgeClasses} text-[13px] font-bold shadow-sm">
                        <span>${icon}</span>
                        <span>${e.budget_impact.toLocaleString('fr-FR')} €</span>
                        ${typeHtml}
                    </div>
                </div>
            `;
        }

        let impactsHtml = '';
        // On masque silencieusement si l'IA a retourné "Néant"
        if (e.analysis_data.impacts && e.analysis_data.impacts.toLowerCase() !== 'néant' && e.analysis_data.impacts !== 'null') {
            impactsHtml = `<div class="mb-6 last:mb-0"><p class="text-[11px] font-semibold text-brand-600 uppercase tracking-widest mb-1.5">Impacts concrets</p><p class="text-[15px] text-slate-500 leading-relaxed">${highlightText(e.analysis_data.impacts, searchQuery, e.acronyms)}</p></div>`;
        }

        const otherSections = [
            { label: "Contexte", content: e.analysis_data.contexte }, 
            { label: "Décision prise", content: e.analysis_data.decision }, 
            { label: "Points de controverse", content: e.analysis_data.points_debattus }
        ].filter(s => s.content && "null" !== s.content).map(t => `<div class="mb-6 last:mb-0"><p class="text-[11px] font-semibold text-brand-600 uppercase tracking-widest mb-1.5">${escapeHTML(t.label)}</p><p class="text-[15px] text-slate-500 leading-relaxed">${highlightText(t.content, searchQuery, e.acronyms)}</p></div>`).join("");

        const sections = otherSections + impactsHtml;
        
        if (budgetDetail || sections) {
            c = `<div class="bg-brand-50/50 rounded-xl px-3 py-5 md:px-4 border border-brand-100/60 mb-8"><h5 class="text-[11px] font-semibold text-brand-700 uppercase tracking-widest mb-5 flex items-center gap-2"><svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"></path></svg>Éclairage</h5>${budgetDetail}${sections}</div>`;
        }
    }
    const d = n ? `<div class="bg-white rounded-2xl p-6 shadow-micro border border-slate-100/80">\n            <div class="flex items-center justify-between mb-5">\n                <h5 class="text-[11px] font-semibold text-slate-900 uppercase tracking-widest">Vote</h5>\n                ${r ? '<span class="inline-flex items-center gap-1 text-[11px] font-semibold text-emerald-700 bg-emerald-50 border border-emerald-100 px-2 py-0.5 rounded-full ml-2">Unanimité</span>' : ""}\n            </div>\n            ${r ? `<div class="flex items-center gap-3"><div class="h-1.5 flex-1 bg-emerald-500 rounded-full"></div><span class="text-sm font-semibold text-emerald-700">${e.vote.pour} pour</span></div>` : `<div class="space-y-3">${renderVoteBar("Pour", e.vote.pour, s, "bg-emerald-500")}${renderVoteBar("Contre", e.vote.contre, s, "bg-rose-400")}${renderVoteBar("Abstention", e.vote.abstention, s, "bg-slate-300")}</div>`}\n           </div>` : '<div class="bg-slate-50 rounded-2xl p-6 border border-slate-100/50">\n            <p class="text-[11px] font-medium text-slate-600 uppercase tracking-widest text-center">Pas de vote enregistré</p>\n           </div>';
    
    return `<div class="group/item" data-delib-id="${e.id}">
        <button onclick="toggleDelib('${t}')" id="btn-${t}" aria-expanded="false" aria-controls="content-${t}" class="delib-trigger w-full text-left px-4 py-4 md:px-8 md:py-6 flex items-center justify-between gap-4 min-h-[64px]">\n            <div class="flex-1 min-w-0">\n                <div class="flex items-center gap-2.5 mb-2">\n                    <span class="w-1.5 h-1.5 rounded-full ${a > 50 || r ? "bg-emerald-400" : n ? "bg-amber-400" : "bg-slate-300"} shrink-0"></span>\n                    <span class="text-[11px] font-medium text-slate-600 uppercase tracking-widest">${i}</span>\n                    ${budgetBadge}\n                    ${r ? '<span class="text-[10px] font-semibold text-emerald-700 bg-emerald-50 px-1.5 py-0.5 rounded">Unanime</span>' : ""}\n                </div>\n                <h4 class="text-base md:text-base font-semibold text-slate-800 group-hover/item:text-brand-600 transition-colors leading-snug">${o}<button class="opacity-0 group-hover/item:opacity-100 transition-opacity duration-200 ml-2 text-slate-400 hover:text-brand-600 align-middle inline-flex items-center" title="Copier le lien de partage" aria-label="Partager cette délibération" onclick="event.stopPropagation(); copyShareLink('${e.id}')"><svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M13.828 10.172a4 4 0 00-5.656 0l-4 4a4 4 0 105.656 5.656l1.102-1.101m-.758-4.899a4 4 0 005.656 0l4-4a4 4 0 00-5.656-5.656l-1.1 1.1"></path></svg></button></h4>\n            </div>\n            <div class="w-8 h-8 rounded-full bg-slate-50 flex items-center justify-center text-slate-300 group-hover/item:text-brand-600 group-hover/item:bg-brand-50 transition-all shrink-0">\n                <svg id="icon-${t}" class="w-4 h-4 transition-transform duration-300" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg>\n            </div>\n        </button>\n        <div id="content-${t}" class="delib-panel" role="region" aria-labelledby="btn-${t}">\n            <div class="delib-panel-inner">\n                <div class="border-t border-slate-100/60 bg-slate-50/30">\n                    <div class="px-4 pt-5 pb-5 md:px-10 md:py-8">\n                        <div class="grid grid-cols-1 lg:grid-cols-12 gap-8">\n                            <div class="lg:col-span-7">\n                                <p class="text-slate-500 leading-relaxed text-base mb-8">${l}</p>\n                                ${c}\n                                <div class="mt-8 pt-8 border-t border-slate-100/60 flex flex-wrap gap-4">\n                                    <a href="${e.pdf_url}" target="_blank" class="inline-flex items-center gap-2 text-xs font-bold text-brand-600 hover:text-brand-700 bg-brand-50 px-4 py-2 rounded-lg transition-all border border-brand-100 hover:shadow-sm">\n                                        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>\n                                        Consulter le PDF officiel\n                                    </a>\n                                </div>\n                            </div>\n                            <div class="lg:col-span-5">\n                                ${d}\n                            </div>\n                        </div>\n                    </div>\n                </div>\n            </div>\n        </div>\n    </div>`;
}

function renderVoteBar(e, t, n, s) { if (null == t) return `<div class="flex justify-between text-[11px] font-light text-slate-300 tracking-wide"><span>${e}</span><span>—</span></div>`; return `<div><div class="flex justify-between text-[11px] font-medium mb-1"><span class="text-slate-600">${e}</span><span class="text-slate-600 font-semibold">${t}</span></div><div class="h-1.5 bg-slate-100 rounded-full overflow-hidden"><div class="${s} h-full rounded-full transition-all duration-500" style="width:${n > 0 ? t / n * 100 : 0}%"></div></div></div>` }
function toggleDelib(e) { const t = document.getElementById(`content-${e}`), n = document.getElementById(`icon-${e}`), s = document.getElementById(`btn-${e}`); if (!t || !n || !s) return; const a = t.classList.contains("is-open"); t.classList.toggle("is-open"), n.classList.toggle("rotate-180"), s.setAttribute("aria-expanded", a ? "false" : "true"); if (!a) { const r = s.closest('[data-delib-id]'); if (r) { const d = r.getAttribute('data-delib-id'); let g = 'unknown'; s.querySelectorAll('span').forEach(x => { if (x.className.includes('tracking-widest') && x.className.includes('uppercase')) g = x.textContent.trim() }); phCapture('deliberation_expanded', {deliberation_id: d, topic_tag: g}) } } }
function scrollToAndOpenDelib(delibId) {
    history.replaceState(null, '', '#delib-' + delibId);
    const container = document.querySelector(`[data-delib-id="${delibId}"]`);
    if (!container) return;
    container.scrollIntoView({ behavior: 'smooth', block: 'center' });
    const button = container.querySelector('.delib-trigger');
    const panel = container.querySelector('.delib-panel');
    if (button && panel && !panel.classList.contains('is-open')) {
        button.click();
    }
}

function copyShareLink(delibId) {
    const url = window.location.origin + window.location.pathname + '#delib-' + delibId;
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(url)
            .then(() => showToast("Lien copié !"))
            .catch(() => prompt("Copier le lien de partage :", url));
    } else {
        prompt("Copier le lien de partage :", url);
    }
}

function showToast(message) {
    const toast = document.createElement('div');
    toast.className = 'fixed bottom-6 left-1/2 -translate-x-1/2 bg-slate-900 text-white px-5 py-3 rounded-2xl shadow-xl z-[200] transition-opacity duration-300';
    toast.textContent = message;
    document.body.appendChild(toast);
    setTimeout(() => {
        toast.style.opacity = '0';
        setTimeout(() => toast.remove(), 300);
    }, 2000);
}

function toggleView(viewName) {
    phCapture('view_toggled', {view: viewName});
    const timelineView = document.getElementById("timeline");
    const budgetView = document.getElementById("budget-view");
    const chipsBar = document.getElementById("topic-chips-bar");
    const navBudgetBtn = document.getElementById("nav-budget-btn");
    const navTimelineBtn = document.getElementById("nav-timeline-btn");

    if (viewName === 'budget') {
        if(timelineView) timelineView.classList.add("hidden");
        if(budgetView) budgetView.classList.remove("hidden");
        if(chipsBar) chipsBar.classList.add("hidden");

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
        if(budgetView) budgetView.classList.add("hidden");
        if(chipsBar) chipsBar.classList.remove("hidden");

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

function renderBudgetView() {
    renderDashboard();
    const container = document.getElementById("budget-detail");
    if (!container) return;

    const budgetData = getBudgetData();

    // 1. Générer le HTML du Budget Primitif thématique
    const thematicData = {};
    if (budgetData.primitifDelib) {
        const d = budgetData.primitifDelib;
        (d.budget_breakdown || []).forEach(item => {
            if (!item.amount || item.amount <= 0) return;
            const theme = item.topic_tag || 'Administration';
            if (!thematicData[theme]) thematicData[theme] = { total: 0, delibs: [], breakdowns: [] };
            thematicData[theme].total += item.amount;
            thematicData[theme].breakdowns.push({
                label: item.label || item.topic_tag,
                amount: item.amount,
                council_date: budgetData.primitifDelib.council_date || '2025-12-18',
                source_title: d.title,
                source_pdf: d.pdf_url,
            });
        });
    }

    const sortedThemes = Object.entries(thematicData).sort((a, b) => b[1].total - a[1].total);
    let primHtml = '';

    sortedThemes.forEach(([themeName, themeData]) => {
        const themeColor = COLORS[themeName] || COLORS['Autres'];
        const percentage = budgetData.totalBudget > 0 ? ((themeData.total / budgetData.totalBudget) * 100).toFixed(1) : 0;
        
        primHtml += `
            <div class="mb-8">
                <div class="flex items-center justify-between border-b border-slate-200 pb-2 mb-4">
                    <div class="flex items-center gap-2.5">
                        <div class="w-3.5 h-3.5 rounded" style="background-color: ${themeColor}"></div>
                        <h4 class="text-xl font-bold text-slate-800">${themeName}</h4>
                        <span class="text-xs font-semibold text-slate-500 bg-slate-100 px-1.5 py-0.5 rounded">${percentage}%</span>
                    </div>
                    <div class="text-lg font-bold" style="color: ${themeColor}">${formatBudget(themeData.total)}</div>
                </div>
                <div class="bg-white rounded-2xl shadow-card overflow-hidden">
                    <div class="divide-y divide-slate-100/50">
        `;

        if (themeData.breakdowns && themeData.breakdowns.length > 0) {
            const breakdownsBySource = {};
            themeData.breakdowns.forEach(b => {
                const key = b.source_title + '|' + b.council_date;
                if (!breakdownsBySource[key]) breakdownsBySource[key] = { title: b.source_title, date: b.council_date, pdf: b.source_pdf, lines: [] };
                breakdownsBySource[key].lines.push(b);
            });
            Object.values(breakdownsBySource).forEach(src => {
                src.lines.sort((a, b) => b.amount - a.amount);
                const srcTotal = src.lines.reduce((s, l) => s + l.amount, 0);
                const linesHtml = src.lines.map(l => `
                    <div class="flex items-center justify-between py-2 border-b border-slate-100/60 last:border-0">
                        <span class="text-[13px] text-slate-600 leading-snug flex-1 pr-4">${escapeHTML(l.label)}</span>
                        <span class="text-[13px] font-semibold text-slate-800 shrink-0">${l.amount.toLocaleString('fr-FR')} €</span>
                    </div>`).join('');
                primHtml += `
                    <div class="px-4 py-5 md:px-6 md:py-5">
                        <div class="flex items-center gap-2 mb-2">
                            <span class="w-1.5 h-1.5 rounded-full bg-amber-400 shrink-0"></span>
                            <span class="text-[10px] font-semibold text-slate-500 uppercase tracking-widest">${formatDate(src.date)}</span>
                            <span class="inline-flex items-center whitespace-nowrap shrink-0 px-2 py-0.5 rounded text-[9px] font-bold bg-amber-50 text-amber-700 border border-amber-100 ml-2 shadow-sm">💰 ${srcTotal.toLocaleString('fr-FR')} €</span>
                        </div>
                        <h5 class="text-sm font-semibold text-slate-700 mb-3">${escapeHTML(src.title)}</h5>
                        <div class="bg-slate-50/60 rounded-xl px-4 py-1">${linesHtml}</div>
                        ${src.pdf ? `<div class="mt-3"><a href="${src.pdf}" target="_blank" class="inline-flex items-center gap-1.5 text-[11px] font-bold text-brand-600 hover:text-brand-700 bg-brand-50 px-3 py-1.5 rounded-lg transition-all border border-brand-100 hover:shadow-sm"><svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>Consulter le PDF officiel</a></div>` : ''}
                    </div>`;
            });
        }

        primHtml += `
                    </div>
                </div>
            </div>
        `;
    });

    // 2. Générer le HTML des Projets Complémentaires
    let projectsHtml = '';
    if (budgetData.otherFinancialDelibs && budgetData.otherFinancialDelibs.length > 0) {
        budgetData.otherFinancialDelibs.sort((a, b) => b.council_date.localeCompare(a.council_date));
        budgetData.otherFinancialDelibs.forEach(d => {
            projectsHtml += renderDeliberationRow(d);
        });
    } else {
        projectsHtml = `<div class="text-center py-8 text-slate-500 text-sm">Aucun autre projet budgétaire enregistré.</div>`;
    }

    // 3. Générer le HTML des Ajustements
    let adjustmentsHtml = '';
    if (budgetData.adjustments && budgetData.adjustments.length > 0) {
        budgetData.adjustments.sort((a, b) => b.date.localeCompare(a.date));
        budgetData.adjustments.forEach(adj => {
            adjustmentsHtml += `
                <div class="p-4 bg-slate-50/50 rounded-2xl border border-slate-100 hover:shadow-micro transition-all">
                    <div class="flex items-center gap-2 mb-1.5">
                        <span class="text-[9px] font-bold text-slate-400 uppercase tracking-widest">${formatDate(adj.date)}</span>
                        <span class="text-[10px] font-black text-slate-600 bg-slate-100 px-1.5 py-0.5 rounded ml-auto">${formatBudget(adj.amount)}</span>
                    </div>
                    <p class="text-xs font-semibold text-slate-800 leading-snug mb-2">${escapeHTML(adj.title)}</p>
                    ${adj.pdf ? `<a href="${adj.pdf}" target="_blank" class="text-[10px] font-bold text-brand-600 hover:underline inline-flex items-center gap-1"><svg class="w-2.5 h-2.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>PDF officiel</a>` : ''}
                </div>
            `;
        });
    } else {
        adjustmentsHtml = `<div class="text-center py-6 text-slate-400 text-xs">Aucun ajustement enregistré.</div>`;
    }

    // 4. Assembler le tout dans une grille responsive
    container.innerHTML = `
        <div class="grid grid-cols-1 lg:grid-cols-12 gap-8">
            <!-- Colonne gauche/principale : Budget Primitif et Projets Réels -->
            <div class="lg:col-span-8 space-y-12">
                <div>
                    <h3 class="text-2xl font-bold text-slate-900 mb-6 flex items-center gap-2.5">
                        📊 Détails du Budget de Référence (Budget Primitif)
                    </h3>
                    <div class="space-y-8">
                        ${primHtml}
                    </div>
                </div>
                
                <div>
                    <h3 class="text-2xl font-bold text-slate-900 mb-6 flex items-center gap-2.5">
                        ✨ Décisions & Investissements Complémentaires
                    </h3>
                    <p class="text-sm text-slate-500 leading-relaxed mb-6">
                        Ces délibérations concernent des projets précis et des aides financières votés individuellement au cours de l'année. Leurs montants ne sont pas additionnés au budget principal afin d'éviter les double-comptages.
                    </p>
                    <div class="bg-white rounded-[2rem] shadow-card overflow-hidden">
                        <div class="divide-y divide-slate-100/50">
                            ${projectsHtml}
                        </div>
                    </div>
                </div>
            </div>
            
            <!-- Colonne droite : Historique et Ajustements de Trésorerie -->
            <div class="lg:col-span-4 space-y-6">
                <h3 class="text-xl font-bold text-slate-900 flex items-center gap-2">
                    ⚙️ Ajustements & Trésorerie
                </h3>
                <div class="bg-white rounded-[2.5rem] shadow-card p-6 border border-slate-100/80 space-y-6">
                    <p class="text-xs text-slate-500 leading-relaxed border-l-2 border-brand-200 pl-3">
                        Ces votes concernent des écritures comptables d'ajustement ou d'affectation de surplus budgétaires de l'année précédente.
                    </p>
                    <div class="space-y-4">
                        ${adjustmentsHtml}
                    </div>
                </div>
            </div>
        </div>
    `;
}

init();
const contactForm = document.getElementById("contact-form"), contactStatus = document.getElementById("contact-status");
contactForm && contactForm.addEventListener("submit", async e => { e.preventDefault(); const t = { name: document.getElementById("contact-name").value, email_sender: document.getElementById("contact-email").value, message: document.getElementById("contact-message").value }; try { e.submitter && (e.submitter.disabled = !0); const n = await fetch("https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/contact", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(t) }), s = await n.json().catch(() => ({})); n.ok ? (phCapture('contact_form_submitted', {success: true}), contactStatus.textContent = "Message envoyé avec succès.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-green-600 bg-green-50 block", contactForm.reset()) : (phCapture('contact_form_submitted', {success: false}), contactStatus.textContent = s.error || "Erreur lors de l'envoi.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block") } catch (e) { contactStatus.textContent = "Erreur réseau. Impossible de contacter le serveur.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block", "undefined" != typeof turnstile && "undefined" != typeof contactWidgetId && turnstile.reset(contactWidgetId) } finally { e.submitter && (e.submitter.disabled = !1) } });

const newsletterForm = document.getElementById("newsletter-form"), newsletterStatus = document.getElementById("newsletter-status"), newsletterEmail = document.getElementById("newsletter-email"), newsletterCheckbox = document.getElementById("newsletter-checkbox"), newsletterSubmit = document.getElementById("newsletter-submit"), newsletterConfirm = document.getElementById("newsletter-confirm"), newsletterConfirmEmail = document.getElementById("newsletter-confirm-email");
newsletterCheckbox && newsletterCheckbox.addEventListener("change", () => { newsletterSubmit.disabled = !newsletterCheckbox.checked; });
newsletterForm && newsletterForm.addEventListener("submit", async e => { e.preventDefault(); const email = newsletterEmail.value; try { e.submitter && (e.submitter.disabled = !0); const n = await fetch("https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/subscribe", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email }) }), s = await n.json().catch(() => ({})); n.ok ? (phCapture('newsletter_form_submitted', {success: true}), newsletterForm.classList.add("hidden"), newsletterConfirmEmail.textContent = email, newsletterConfirm.classList.remove("hidden")) : (phCapture('newsletter_form_submitted', {success: false}), newsletterStatus.textContent = s.error || "Oups ! Une erreur est survenue lors de l'inscription. Merci de réessayer d'ici quelques instants.", newsletterStatus.className = "text-sm font-medium text-center py-3 px-4 rounded-xl text-rose-400 bg-rose-400/10 border border-rose-400/20 block") } catch (e) { newsletterStatus.textContent = "Nous n'avons pas réussi à vous inscrire. Merci de vérifier votre connexion ou de réessayer plus tard.", newsletterStatus.className = "text-sm font-medium text-center py-3 px-4 rounded-xl text-rose-400 bg-rose-400/10 border border-rose-400/20 block" } finally { e.submitter && (e.submitter.disabled = !1) } });
