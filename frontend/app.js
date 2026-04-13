console.log("🚀 L'Observatoire Citoyen : Initialisation...");
let searchTimeout, allCouncils = [], visibleCouncilsCount = 1, searchQuery = "";
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
    'Éducation & Jeunesse': '#3b82f6',
    'Transition Écologique': '#10b981',
    'Solidarité & Social': '#6366f1',
    'Sport & Culture': '#f59e0b',
    'Aménagement & Travaux': '#8b5cf6',
    'Administration': '#94a3b8',
    'Sécurité': '#ef4444',
    'Mobilité': '#06b6d4',
    'Urbanisme': '#f97316',
    'Autres': '#cbd5e1'
};

function escapeRegExp(e) { return e.replace(/[.*+?^${}()|[\]\\]/g, "\\$&") }
function escapeHTML(e) { if (!e) return ""; const t = document.createElement("div"); return t.textContent = e, t.innerHTML }
function toggleAboutModal(e) { document.getElementById("about-modal").classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e) }
function toggleTeamModal(e) { const t = document.getElementById("team-modal"), n = document.getElementById("team-modal-body"), s = document.getElementById("team-scroll-indicator"); t.classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e && n && s && (n.scrollTop = 0, s.style.opacity = "1", n.dataset.hasScrollListener || (n.addEventListener("scroll", () => { n.scrollTop > 50 ? s.style.opacity = "0" : s.style.opacity = "1" }), n.dataset.hasScrollListener = "true")) }
function toggleContactModal(e) { document.getElementById("contact-modal").classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e) }
function toggleMobileMenu(e) { document.getElementById("mobile-menu").classList.toggle("hidden", !e), document.body.classList.toggle("modal-open", e), e ? setTimeout(() => { document.getElementById("mobile-search-input")?.focus() }, 100) : document.getElementById("mobile-search-input")?.blur() }
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
        
        allCouncils = (t.councils || []).sort((e, t) => t.date.localeCompare(e.date));
        console.log("📊 Nombre de conseils:", allCouncils.length);
        
        updateStats();
        renderDashboard();
        render();
    } catch (e) {
        console.error("❌ Erreur Init:", e);
    }
}

function renderDashboard() {
    console.log("🎨 Rendu du Dashboard...");
    const container = document.getElementById('global-dashboard');
    if (!container) { console.warn("⚠️ Conteneur #global-dashboard introuvable"); return; }
    
    // Collect all deliberations with a verified budget_impact from 2026 councils
    const delibs2026 = [];
    allCouncils.filter(c => c.date && c.date.startsWith('2026')).forEach(c => {
        (c.deliberations || []).forEach(d => { if (d.budget_impact > 0) delibs2026.push(d); });
    });

    if (delibs2026.length === 0) {
        console.log("ℹ️ Pas encore de montants vérifiés 2026, dashboard masqué.");
        container.classList.add('hidden');
        return;
    }

    container.classList.remove('hidden');
    let totalBudget = 0;
    const categories = {};
    delibs2026.forEach(d => {
        totalBudget += d.budget_impact;
        const cat = d.topic_tag || 'Autres';
        categories[cat] = (categories[cat] || 0) + d.budget_impact;
    });

    const sortedCats = Object.entries(categories).sort((a, b) => b[1] - a[1]);
    
    let ribbonHtml = '<div class="ribbon-bar flex h-3 bg-slate-100 rounded-full overflow-hidden mb-4 shadow-inner">';
    sortedCats.forEach(([name, val]) => {
        const pct = totalBudget > 0 ? (val / totalBudget * 100).toFixed(1) : 0;
        const color = COLORS[name] || COLORS['Autres'];
        ribbonHtml += `<div class="ribbon-segment" style="width: ${pct}%; background: ${color}" title="${name}: ${pct}%"></div>`;
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
                    <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 19v-6a2 2 0 00-2-2H5a2 2 0 00-2 2v6a2 2 0 002 2h2a2 2 0 002-2zm0 0V9a2 2 0 012-2h2a2 2 0 012 2v10m-6 0a2 2 0 002 2h2a2 2 0 002-2m0 0V5a2 2 0 012-2h2a2 2 0 012 2v14a2 2 0 01-2 2h-2a2 2 0 01-2-2z"></path></svg>
                    Analyse Budgétaire 2026
                </span>
                <div class="budget-total-val text-4xl md:text-5xl font-black text-slate-900 tracking-tight">${formatBudget(totalBudget)}</div>
            </div>
            
            <h4 class="text-[10px] font-black text-slate-400 uppercase tracking-widest mb-4">Répartition thématique (Cumul Annuel)</h4>
            ${ribbonHtml}
            ${legendHtml}
            ${gridHtml}
        </div>
    `;
    console.log("✅ Dashboard rendu avec succès.");
}

function handleSearch(e) { const t = document.getElementById("nav-search-input"), n = document.getElementById("mobile-search-input"); t && t.value !== e && (t.value = e), n && n.value !== e && (n.value = e), clearTimeout(searchTimeout), searchTimeout = setTimeout(() => { searchQuery = e.toLowerCase().trim(), visibleCouncilsCount = searchQuery ? 5 : 1, render() }, 300) }
function loadMore() { visibleCouncilsCount += 2, render() }
function updateStats() { const e = document.getElementById("stat-councils"), t = document.getElementById("stat-deliberations"); e && (e.textContent = allCouncils.length), t && (t.textContent = allCouncils.reduce((e, t) => e + (t.deliberations?.length || 0), 0)) }
function formatDate(e) { if (!e) return "Date inconnue"; try { const t = new Date(e); return isNaN(t.getTime()) ? e : t.toLocaleDateString("fr-FR", { day: "numeric", month: "long", year: "numeric" }) } catch (t) { return e } }

function formatBudget(val) {
    return val.toLocaleString('fr-FR', { maximumFractionDigits: 0 }) + " €";
}

function render() {
    const container = document.getElementById("timeline");
    if (!container) return;

    const filteredCouncils = allCouncils.map(council => {
        const matchingDelibs = (council.deliberations || []).filter(d => 
            d.title.toLowerCase().includes(searchQuery) || 
            d.summary.toLowerCase().includes(searchQuery) || 
            (d.topic_tag && d.topic_tag.toLowerCase().includes(searchQuery))
        );
        return matchingDelibs.length > 0 || searchQuery === "" ? { ...council, deliberations: matchingDelibs } : null;
    }).filter(c => c !== null);

    const visibleCouncils = filteredCouncils.slice(0, visibleCouncilsCount);
    container.innerHTML = "";

    visibleCouncils.forEach(council => {
        const section = document.createElement("section");
        section.className = "council-group animate-slide-up mb-20";
        const dateStr = formatDate(council.date), delibCount = council.deliberations?.length || 0, title = escapeHTML(council.title);
        let rawSummary = (council.summary || "").replace(/\s+/g, " ").trim();
        if (!rawSummary || rawSummary === council.title || rawSummary.length < 10) rawSummary = `Ce conseil a traité ${delibCount} délibérations.`;

        const agendaHtml = council.agenda ? `<div class="badge-agenda"><svg class="w-3 h-3" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 7V3m8 4V3m-9 8h10M5 21h14a2 2 0 002-2V7a2 2 0 00-2-2H5a2 2 0 00-2 2v12a2 2 0 002 2z"></path></svg>${council.agenda}</div>` : "";
        
        let councilRibbonHtml = '', councilLegendHtml = '';
        const councilTotal = council.analysis ? council.analysis.budget_impact : 0;
        if (councilTotal > 0) {
            const cats = {};
            (council.deliberations || []).forEach(d => {
                if (d.budget_impact > 0) { const cat = d.topic_tag || 'Autres'; cats[cat] = (cats[cat] || 0) + d.budget_impact; }
            });
            const sortedCats = Object.entries(cats).sort((a, b) => b[1] - a[1]);
            councilRibbonHtml = '<div class="flex rounded-full overflow-hidden mt-5 bg-slate-100" style="height: 10px;">';
            sortedCats.forEach(([name, val]) => {
                const pct = (val / councilTotal * 100).toFixed(1);
                councilRibbonHtml += `<div style="width:${pct}%;background:${COLORS[name]||COLORS['Autres']}"></div>`;
            });
            councilRibbonHtml += '</div>';
            councilLegendHtml = '<div class="flex flex-wrap mt-4 text-xs font-bold text-slate-500 leading-none" style="gap:20px;row-gap:12px;">';
            sortedCats.forEach(([name, val]) => {
                const pct = Math.round(val / councilTotal * 100);
                const color = COLORS[name] || COLORS['Autres'];
                councilLegendHtml += `<div class="flex items-center gap-2"><span class="mr-1" style="width:10px;height:10px;border-radius:2px;background:${color}"></span><span>${pct}% ${name}</span></div>`;
            });
            councilLegendHtml += '</div>';
        }

        const totalVotes = council.analysis ? (council.analysis.votes_pour + council.analysis.votes_contre) : 0;
        const pourPct = totalVotes > 0 ? (council.analysis.votes_pour / totalVotes * 100).toFixed(0) : 0;
        const contrePct = totalVotes > 0 ? (council.analysis.votes_contre / totalVotes * 100).toFixed(0) : 0;

        let analysisHtml = "";
        if (council.analysis) {
            const hasFinancial = council.analysis.budget_impact > 0, hasVotes = totalVotes > 0;
            if (hasFinancial || hasVotes) {
                analysisHtml = `<div class="analysis-grid grid grid-cols-1 gap-4 mt-6">`;
                if (hasFinancial) {
                    analysisHtml += `<div class="analysis-card bg-slate-50/50 border border-slate-100 rounded-2xl p-6"><span class="block text-xs font-bold text-slate-400 uppercase tracking-widest mb-3">💰 Impact Financier</span><div class="text-2xl font-black text-slate-900">${council.analysis.budget_impact.toLocaleString("fr-FR")} €</div>${councilRibbonHtml}${councilLegendHtml}</div>`;
                }
                if (hasVotes) {
                    analysisHtml += `<div class="analysis-card bg-slate-50/50 border border-slate-100 rounded-2xl p-6"><span class="block text-xs font-bold text-slate-400 uppercase tracking-widest mb-3">⚖️ Climat des Votes</span><div class="vote-climat ${council.analysis.vote_climat === "consensus" ? "climat-consensus" : "climat-tensions"}">${council.analysis.vote_climat.toUpperCase()}</div><div class="mt-4 flex flex-col gap-2"><div class="flex justify-between text-[11px] font-bold text-slate-500"><span>Pour: ${council.analysis.votes_pour}</span><span>Contre: ${council.analysis.votes_contre}</span></div><div class="h-1.5 w-full bg-slate-100 rounded-full flex overflow-hidden"><div style="width: ${pourPct}%; background: #10b981;"></div><div style="width: ${contrePct}%; background: #ef4444;"></div></div></div></div>`;
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
        loadMoreBtn.onclick = loadMore;
        loadMoreBtn.className = "w-full py-5 bg-white rounded-2xl text-slate-600 hover:text-brand-600 font-medium text-sm tracking-wide transition-all shadow-micro group active:scale-[0.98] min-h-[44px] mb-12";
        loadMoreBtn.innerHTML = `<div class="flex items-center justify-center gap-2"><span>Charger les archives</span><span class="text-xs text-slate-500 font-normal">(${remaining} restants)</span><svg class="w-4 h-4 opacity-40 group-hover:translate-y-0.5 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg></div>`;
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
    const budgetBadge = e.budget_impact > 0 
        ? `<span class="inline-flex items-center px-2 py-0.5 rounded text-[10px] font-bold bg-amber-50 text-amber-700 border border-amber-100 ml-2 shadow-sm">💰 ${e.budget_impact.toLocaleString('fr-FR')} €</span>` 
        : "";

    let c = "";
    if (e.analysis_data) {
        // Détail budgétaire dans Éclairage pour Option C
        const budgetDetail = e.budget_impact > 0 
            ? `<div class="mb-6"><p class="text-[11px] font-semibold text-amber-600 uppercase tracking-widest mb-1.5">Impact Financier</p><p class="text-[15px] text-slate-600 font-bold">Le montant alloué pour cette délibération est de ${e.budget_impact.toLocaleString('fr-FR')} € HT.</p></div>`
            : "";

        const sections = [{ label: "Contexte", content: e.analysis_data.contexte }, { label: "Décision prise", content: e.analysis_data.decision }, { label: "Impacts concrets", content: e.analysis_data.impacts }, { label: "Points de controverse", content: e.analysis_data.points_debattus }].filter(e => e.content && "null" !== e.content).map(t => `<div class="mb-6 last:mb-0"><p class="text-[11px] font-semibold text-brand-600 uppercase tracking-widest mb-1.5">${escapeHTML(t.label)}</p><p class="text-[15px] text-slate-500 leading-relaxed">${highlightText(t.content, searchQuery, e.acronyms)}</p></div>`).join("");
        
        if (budgetDetail || sections) {
            c = `<div class="bg-brand-50/50 rounded-xl px-3 py-5 md:px-4 border border-brand-100/60 mb-8"><h5 class="text-[11px] font-semibold text-brand-700 uppercase tracking-widest mb-5 flex items-center gap-2"><svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"></path></svg>Éclairage</h5>${budgetDetail}${sections}</div>`;
        }
    }
    const d = n ? `<div class="bg-white rounded-2xl p-6 shadow-micro border border-slate-100/80">\n            <div class="flex items-center justify-between mb-5">\n                <h5 class="text-[11px] font-semibold text-slate-900 uppercase tracking-widest">Vote</h5>\n                ${r ? '<span class="inline-flex items-center gap-1 text-[11px] font-semibold text-emerald-700 bg-emerald-50 border border-emerald-100 px-2 py-0.5 rounded-full ml-2">Unanimité</span>' : ""}\n            </div>\n            ${r ? `<div class="flex items-center gap-3"><div class="h-1.5 flex-1 bg-emerald-500 rounded-full"></div><span class="text-sm font-semibold text-emerald-700">${e.vote.pour} pour</span></div>` : `<div class="space-y-3">${renderVoteBar("Pour", e.vote.pour, s, "bg-emerald-500")}${renderVoteBar("Contre", e.vote.contre, s, "bg-rose-400")}${renderVoteBar("Abstention", e.vote.abstention, s, "bg-slate-300")}</div>`}\n           </div>` : '<div class="bg-slate-50 rounded-2xl p-6 border border-slate-100/50">\n            <p class="text-[11px] font-medium text-slate-600 uppercase tracking-widest text-center">Pas de vote enregistré</p>\n           </div>';
    
    return `<div class="group/item">\n        <button onclick="toggleDelib('${t}')" id="btn-${t}" aria-expanded="false" aria-controls="content-${t}" class="delib-trigger w-full text-left px-4 py-4 md:px-8 md:py-6 flex items-center justify-between gap-4 min-h-[64px]">\n            <div class="flex-1 min-w-0">\n                <div class="flex items-center gap-2.5 mb-2">\n                    <span class="w-1.5 h-1.5 rounded-full ${a > 50 || r ? "bg-emerald-400" : n ? "bg-amber-400" : "bg-slate-300"} shrink-0"></span>\n                    <span class="text-[11px] font-medium text-slate-600 uppercase tracking-widest">${i}</span>\n                    ${budgetBadge}\n                    ${r ? '<span class="text-[10px] font-semibold text-emerald-700 bg-emerald-50 px-1.5 py-0.5 rounded">Unanime</span>' : ""}\n                </div>\n                <h4 class="text-base md:text-base font-semibold text-slate-800 group-hover/item:text-brand-600 transition-colors leading-snug">${o}</h4>\n            </div>\n            <div class="w-8 h-8 rounded-full bg-slate-50 flex items-center justify-center text-slate-300 group-hover/item:text-brand-600 group-hover/item:bg-brand-50 transition-all shrink-0">\n                <svg id="icon-${t}" class="w-4 h-4 transition-transform duration-300" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg>\n            </div>\n        </button>\n        <div id="content-${t}" class="delib-panel" role="region" aria-labelledby="btn-${t}">\n            <div class="delib-panel-inner">\n                <div class="border-t border-slate-100/60 bg-slate-50/30">\n                    <div class="px-4 pt-5 pb-5 md:px-10 md:py-8">\n                        <div class="grid grid-cols-1 lg:grid-cols-12 gap-8">\n                            <div class="lg:col-span-7">\n                                <p class="text-slate-500 leading-relaxed text-base mb-8">${l}</p>\n                                ${c}\n                                <div class="mt-8 pt-8 border-t border-slate-100/60 flex flex-wrap gap-4">\n                                    <a href="${e.pdf_url}" target="_blank" class="inline-flex items-center gap-2 text-xs font-bold text-brand-600 hover:text-brand-700 bg-brand-50 px-4 py-2 rounded-lg transition-all border border-brand-100 hover:shadow-sm">\n                                        <svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>\n                                        Consulter le PDF officiel\n                                    </a>\n                                </div>\n                            </div>\n                            <div class="lg:col-span-5">\n                                ${d}\n                            </div>\n                        </div>\n                    </div>\n                </div>\n            </div>\n        </div>\n    </div>`;
}

function renderVoteBar(e, t, n, s) { if (null == t) return `<div class="flex justify-between text-[11px] font-light text-slate-300 tracking-wide"><span>${e}</span><span>—</span></div>`; return `<div><div class="flex justify-between text-[11px] font-medium mb-1"><span class="text-slate-600">${e}</span><span class="text-slate-600 font-semibold">${t}</span></div><div class="h-1.5 bg-slate-100 rounded-full overflow-hidden"><div class="${s} h-full rounded-full transition-all duration-500" style="width:${n > 0 ? t / n * 100 : 0}%"></div></div></div>` }
function toggleDelib(e) { const t = document.getElementById(`content-${e}`), n = document.getElementById(`icon-${e}`), s = document.getElementById(`btn-${e}`); if (!t || !n || !s) return; const a = t.classList.contains("is-open"); t.classList.toggle("is-open"), n.classList.toggle("rotate-180"), s.setAttribute("aria-expanded", a ? "false" : "true") }

init();
const contactForm = document.getElementById("contact-form"), contactStatus = document.getElementById("contact-status");
contactForm && contactForm.addEventListener("submit", async e => { e.preventDefault(); const t = { name: document.getElementById("contact-name").value, email_sender: document.getElementById("contact-email").value, message: document.getElementById("contact-message").value }; try { e.submitter && (e.submitter.disabled = !0); const n = await fetch("https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/contact", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(t) }), s = await n.json().catch(() => ({})); n.ok ? (contactStatus.textContent = "Message envoyé avec succès.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-green-600 bg-green-50 block", contactForm.reset()) : (contactStatus.textContent = s.error || "Erreur lors de l'envoi.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block") } catch (e) { contactStatus.textContent = "Erreur réseau. Impossible de contacter le serveur.", contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block", "undefined" != typeof turnstile && "undefined" != typeof contactWidgetId && turnstile.reset(contactWidgetId) } finally { e.submitter && (e.submitter.disabled = !1) } });
