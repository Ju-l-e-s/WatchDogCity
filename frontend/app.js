let allCouncils = [];
let visibleCouncilsCount = 1; 
let searchTimeout;
let searchQuery = '';

const GLOBAL_GLOSSARY = {
    "CCAS": "Centre Communal d'Action Sociale",
    "ZAC": "Zone d'Aménagement Concerté",
    "PLU": "Plan Local d'Urbanisme",
    "EPCI": "Établissement Public de Coopération Intercommunale",
    "DSP": "Délégation de Service Public",
    "AFL": "Agence France Locale",
    "CREPAQ": "Centre de Ressources d'Écologie Pédagogique en Nouvelle-Aquitaine",
    "BM": "Bordeaux Métropole",
    "ALSH": "Accueil de Loisirs Sans Hébergement",
    "PADD": "Projet d'Aménagement et de Développement Durable",
    "SCOT": "Schéma de Cohérence Territoriale",
    "CLSPD": "Conseil Local de Sécurité et de Prévention de la Délinquance",
    "ERP": "Établissement Recevant du Public",
    "QPV": "Quartier Prioritaire de la Politique de la Ville",
    "AAP": "Appel à Projets",
    "AMI": "Appel à Manifestation d'Intérêt",
    "SIAE": "Structure de l'Insertion par l'Activité Économique",
    "SIAL": "Système d'Information pour l'Accueil et le Logement"
};

function escapeHTML(str) {
    if (!str) return "";
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

function toggleAboutModal(show) {
    const modal = document.getElementById('about-modal');
    modal.classList.toggle('hidden', !show);
    document.body.classList.toggle('modal-open', show);
}

function toggleTeamModal(show) {
    const modal = document.getElementById('team-modal');
    const body = document.getElementById('team-modal-body');
    const indicator = document.getElementById('team-scroll-indicator');
    
    modal.classList.toggle('hidden', !show);
    document.body.classList.toggle('modal-open', show);
    
    if (show && body && indicator) {
        body.scrollTop = 0;
        indicator.style.opacity = '1';
        
        if (!body.dataset.hasScrollListener) {
            body.addEventListener('scroll', () => {
                if (body.scrollTop > 50) {
                    indicator.style.opacity = '0';
                } else {
                    indicator.style.opacity = '1';
                }
            });
            body.dataset.hasScrollListener = 'true';
        }
    }
}

function toggleContactModal(show) {
    const modal = document.getElementById('contact-modal');
    modal.classList.toggle('hidden', !show);
    document.body.classList.toggle('modal-open', show);
}

function toggleMobileMenu(show) {
    const menu = document.getElementById('mobile-menu');
    menu.classList.toggle('hidden', !show);
    document.body.classList.toggle('modal-open', show);
    if (!show) document.getElementById('mobile-search-input')?.blur();
}

function smartCapitalize(str) {
    if (!str) return "";
    const words = str.split(' ');
    return words.map((word, index) => {
        if (word.length > 1 && word === word.toUpperCase() && !/^[0-9]+$/.test(word)) return word;
        const low = word.toLowerCase();
        if (index === 0) return low.charAt(0).toUpperCase() + low.slice(1);
        return low;
    }).join(' ');
}

function applyAcronyms(text, localAcronyms) {
    const acronymsMap = { ...GLOBAL_GLOSSARY, ...(localAcronyms || {}) };
    if (Object.keys(acronymsMap).length === 0) return text;
    let result = text;
    const keys = Object.keys(acronymsMap).sort((a, b) => b.length - a.length);

    keys.forEach(key => {
        const definition = escapeHTML(acronymsMap[key]);
        const regex = new RegExp(`\\b${key}\\b`, 'g');
        result = result.replace(regex, `<abbr title="${definition}" class="cursor-help border-b border-dotted border-brand-300 decoration-brand-300 decoration-2 underline-offset-4" tabindex="0">${key}</abbr>`);
    });
    return result;
}


function highlightText(text, query, acronymsMap = null) {
    let processedText = escapeHTML(text);
    if (acronymsMap) processedText = applyAcronyms(processedText, acronymsMap);
    if (!query) return processedText;
    const escapedQuery = escapeHTML(query);
    const regex = new RegExp(`(${escapedQuery})`, 'gi');
    const parts = processedText.split(/(<[^>]+>)/g);
    return parts.map(part => {
        if (part.startsWith('<')) return part;
        return part.replace(regex, `<mark class="bg-brand-100 text-brand-700 font-bold px-0.5 rounded">$1</mark>`);
    }).join('');
}

async function init() {
    try {
        const resp = await fetch('./data.json');
        if (!resp.ok) throw new Error('Not found');
        const data = await resp.json();
        if (data.next_council_date) {
            const dateEl = document.getElementById('next-council-date');
            if (dateEl) dateEl.textContent = data.next_council_date;
            const blockEl = document.getElementById('next-council-block');
            if (blockEl) blockEl.classList.remove('hidden');
        }
        allCouncils = (data.councils || []).sort((a, b) => b.date.localeCompare(a.date));
        updateStats();
        render();
    } catch (e) {
        console.error(e);
        const timelineEl = document.getElementById('timeline');
        if (timelineEl) timelineEl.innerHTML = `<p class="text-center text-red-500 py-20 font-medium">Erreur lors du chargement des données. Veuillez réessayer plus tard.</p>`;
    }
}

function handleSearch(val) {
    clearTimeout(searchTimeout);
    searchTimeout = setTimeout(() => {
        searchQuery = val.toLowerCase().trim();
        visibleCouncilsCount = searchQuery ? 5 : 1;
        render();
    }, 300);
}

function loadMore() {
    visibleCouncilsCount += 2;
    render();
}

function updateStats() {
    const councilsEl = document.getElementById('stat-councils');
    const delibsEl = document.getElementById('stat-deliberations');
    if (councilsEl) councilsEl.textContent = allCouncils.length;
    if (delibsEl) delibsEl.textContent = allCouncils.reduce((sum, c) => sum + (c.deliberations?.length || 0), 0);
}

function formatDate(iso) {
    if (!iso) return "Date inconnue";
    try {
        const date = new Date(iso);
        if (isNaN(date.getTime())) return iso;
        return date.toLocaleDateString('fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
    } catch (e) { return iso; }
}

function render() {
    const container = document.getElementById('timeline');
    if (!container) return;

    let totalResults = 0;
    const filtered = allCouncils.map(council => {
        const filteredDelibs = (council.deliberations || []).filter(d => 
            d.title.toLowerCase().includes(searchQuery) || 
            d.summary.toLowerCase().includes(searchQuery) ||
            (d.topic_tag && d.topic_tag.toLowerCase().includes(searchQuery))
        );
        totalResults += filteredDelibs.length;
        return filteredDelibs.length > 0 ? { ...council, deliberations: filteredDelibs } : null;
    }).filter(c => c !== null);

    const councilsToDisplay = filtered.slice(0, visibleCouncilsCount);
    container.innerHTML = '';

    if (searchQuery) {
        const searchHeader = document.createElement('div');
        searchHeader.className = 'mb-12 animate-fade-in flex items-center justify-between bg-white px-6 py-4 rounded-2xl shadow-micro';
        const escapedQuery = escapeHTML(searchQuery);
        searchHeader.innerHTML = `<div class="flex items-center gap-3"><div class="w-8 h-8 bg-brand-50 rounded-lg flex items-center justify-center text-brand-600"><svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path></svg></div><p class="text-sm text-slate-500"><span class="font-semibold text-slate-900">${totalResults}</span> résultat${totalResults > 1 ? 's' : ''} pour <span class="text-brand-600 font-semibold">"${escapedQuery}"</span></p></div><button onclick="handleSearch(''); document.getElementById('nav-search-input').value=''; document.getElementById('mobile-search-input').value='';" class="text-[11px] font-medium text-slate-400 hover:text-rose-500 transition-colors min-h-[44px] px-2" aria-label="Effacer la recherche">Effacer</button>`;
        container.appendChild(searchHeader);
    }

    if (filtered.length === 0) {
        const escapedQuery = escapeHTML(searchQuery);
        container.innerHTML += `<div class="text-center py-24 animate-fade-in"><div class="w-20 h-20 bg-slate-100 rounded-full flex items-center justify-center mx-auto mb-6 text-slate-400"><svg class="w-10 h-10" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M21 21l-6-6m2-5a7 7 0 11-14 0 7 7 0 0114 0z"></path></svg></div><h3 class="text-xl font-bold text-slate-900 mb-2">Aucun résultat</h3><p class="text-slate-500">Aucune délibération ne correspond à votre recherche "${escapedQuery}".</p></div>`;
        return;
    }

    councilsToDisplay.forEach(council => {
        const councilSection = document.createElement('section');
        councilSection.className = 'council-group animate-slide-up mb-20';
        const dateStr = formatDate(council.date);
        const delibsCount = council.deliberations?.length || 0;
        const escapedTitle = escapeHTML(council.title);

        let rawSummary = (council.summary || "").replace(/\s+/g, ' ').trim();
        let displaySummary = rawSummary;
        if (!rawSummary || rawSummary === council.title || rawSummary.length < 10) {
            displaySummary = `Ce conseil a traité ${delibsCount} délibérations.`;
        }
        const escapedSummary = escapeHTML(displaySummary);

        councilSection.innerHTML = `
            <div class="flex items-center gap-6 mb-8">
                <div class="h-px flex-1 bg-slate-200/60"></div>
                <h2 class="text-[13px] font-semibold text-slate-500 uppercase tracking-[0.15em] bg-white px-6 py-2.5 rounded-full shadow-micro">${dateStr}</h2>
                <div class="h-px flex-1 bg-slate-200/60"></div>
            </div>
            <div class="bg-white rounded-[2rem] shadow-card overflow-hidden mt-5 md:mt-0">
                <div class="px-4 pt-5 pb-[10px] md:px-12 md:py-10 border-b border-slate-100/60">
                    <h3 class="text-2xl md:text-3xl font-bold text-slate-900 pt-4 md:pt-0 mb-4 tracking-tight">${escapedTitle}</h3>
                    <p class="text-slate-400 leading-relaxed max-w-3xl text-base font-light">${escapedSummary}</p>
                </div>
                <div class="divide-y divide-slate-100/50">${council.deliberations.map(d => renderDeliberationRow(d)).join('')}</div>
            </div>`;
        container.appendChild(councilSection);
    });

    if (filtered.length > visibleCouncilsCount) {
        const remaining = filtered.length - visibleCouncilsCount;
        const loadMoreBtn = document.createElement('button');
        loadMoreBtn.onclick = loadMore;
        loadMoreBtn.className = 'w-full py-5 bg-white rounded-2xl text-slate-400 hover:text-brand-600 font-medium text-sm tracking-wide transition-all shadow-micro group active:scale-[0.98] min-h-[44px] mb-12';
        loadMoreBtn.innerHTML = `<div class="flex items-center justify-center gap-2"><span>Charger les archives</span><span class="text-xs text-slate-300 font-normal">(${remaining} restants)</span><svg class="w-4 h-4 opacity-40 group-hover:translate-y-0.5 transition-transform" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg></div>`;
        container.appendChild(loadMoreBtn);
    }
}

function renderDeliberationRow(d) {
    const delibId = `delib-${Math.random().toString(36).substr(2, 9)}`;
    const hasVote = d.vote && (d.vote.has_vote || d.vote.pour != null || d.vote.contre != null || d.vote.abstention != null);
    const total = hasVote ? ((d.vote.pour || 0) + (d.vote.contre || 0) + (d.vote.abstention || 0)) : 0;
    const pourPct = (hasVote && total > 0) ? (d.vote.pour / total * 100) : 0;
    const isUnanimous = hasVote && total > 0 && (d.vote.contre || 0) === 0 && (d.vote.abstention || 0) === 0;
    const capitalizedTitle = smartCapitalize(d.title);
    const highlightedTitle = highlightText(capitalizedTitle, searchQuery, d.acronyms);
    const highlightedSummary = highlightText(d.summary, searchQuery, d.acronyms);
    const highlightedTag = highlightText(d.topic_tag || 'Délibération', searchQuery);

    let analysisHTML = '';
    if (d.analysis_data) {
        const sections = [
            { label: 'Contexte', content: d.analysis_data.contexte },
            { label: 'Décision prise', content: d.analysis_data.decision },
            { label: 'Impacts concrets', content: d.analysis_data.impacts },
            { label: 'Points de controverse', content: d.analysis_data.points_debattus }
        ];
        const content = sections.filter(s => s.content).map(s =>
            `<div class="mb-6 last:mb-0"><p class="text-[11px] font-semibold text-brand-600 uppercase tracking-widest mb-1.5">${escapeHTML(s.label)}</p><p class="text-[15px] text-slate-500 leading-relaxed">${highlightText(s.content, searchQuery, d.acronyms)}</p></div>`
        ).join('');
        if (content) {
            analysisHTML = `<div class="bg-brand-50/50 rounded-xl px-3 py-5 md:px-4 border border-brand-100/60 mb-8"><h5 class="text-[11px] font-semibold text-brand-700 uppercase tracking-widest mb-5 flex items-center gap-2"><svg class="w-3.5 h-3.5" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9.663 17h4.673M12 3v1m6.364 1.636l-.707.707M21 12h-1M4 12H3m3.343-5.657l-.707-.707m2.828 9.9a5 5 0 117.072 0l-.548.547A3.374 3.374 0 0014 18.469V19a2 2 0 11-4 0v-.531c0-.895-.356-1.754-.988-2.386l-.548-.547z"></path></svg>Éclairage</h5>${content}</div>`;
        }
    }

    const unanimityBadge = isUnanimous ? `<span class="inline-flex items-center gap-1 text-[11px] font-semibold text-emerald-600 bg-emerald-50 border border-emerald-100 px-2 py-0.5 rounded-full ml-2">Unanimité</span>` : '';

    const voteWidget = hasVote
        ? `<div class="bg-white rounded-2xl p-6 shadow-micro border border-slate-100/80">
            <div class="flex items-center justify-between mb-5">
                <h5 class="text-[11px] font-semibold text-slate-900 uppercase tracking-widest">Vote</h5>
                ${unanimityBadge}
            </div>
            ${isUnanimous
                ? `<div class="flex items-center gap-3"><div class="h-1.5 flex-1 bg-emerald-500 rounded-full"></div><span class="text-sm font-semibold text-emerald-600">${d.vote.pour} pour</span></div>`
                : `<div class="space-y-3">${renderVoteBar('Pour', d.vote.pour, total, 'bg-emerald-500')}${renderVoteBar('Contre', d.vote.contre, total, 'bg-rose-400')}${renderVoteBar('Abstention', d.vote.abstention, total, 'bg-slate-300')}</div>`
            }
           </div>`
        : `<div class="bg-slate-50 rounded-2xl p-6 border border-slate-100/50">
            <p class="text-[11px] font-medium text-slate-400 uppercase tracking-widest text-center">Pas de vote enregistré</p>
           </div>`;

    return `<div class="group/item">
        <button onclick="toggleDelib('${delibId}')" id="btn-${delibId}" aria-expanded="false" aria-controls="content-${delibId}" class="delib-trigger w-full text-left px-4 py-4 md:px-8 md:py-6 flex items-center justify-between gap-4 min-h-[64px]">
            <div class="flex-1 min-w-0">
                <div class="flex items-center gap-2.5 mb-2">
                    <span class="w-1.5 h-1.5 rounded-full ${pourPct > 50 || isUnanimous ? 'bg-emerald-400' : (hasVote ? 'bg-amber-400' : 'bg-slate-300')} shrink-0"></span>
                    <span class="text-[11px] font-medium text-slate-400 uppercase tracking-widest">${highlightedTag}</span>
                    ${isUnanimous ? '<span class="text-[10px] font-semibold text-emerald-500 bg-emerald-50 px-1.5 py-0.5 rounded">Unanime</span>' : ''}
                </div>
                <h4 class="text-base md:text-base font-semibold text-slate-800 group-hover/item:text-brand-600 transition-colors leading-snug">${highlightedTitle}</h4>
            </div>
            <div class="w-8 h-8 rounded-full bg-slate-50 flex items-center justify-center text-slate-300 group-hover/item:text-brand-600 group-hover/item:bg-brand-50 transition-all shrink-0">
                <svg id="icon-${delibId}" class="w-4 h-4 transition-transform duration-300" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7"></path></svg>
            </div>
        </button>
        <div id="content-${delibId}" class="delib-panel" role="region" aria-labelledby="btn-${delibId}">
            <div class="delib-panel-inner">
                <div class="border-t border-slate-100/60 bg-slate-50/30">
                    <div class="px-4 pt-5 pb-5 md:px-10 md:py-8">
                        <div class="grid grid-cols-1 lg:grid-cols-12 gap-8">
                            <div class="lg:col-span-7">
                                <p class="text-slate-500 leading-relaxed text-base mb-8">${highlightedSummary}</p>
                                ${analysisHTML}
                                ${d.disagreements ? `<div class="border-l-2 border-slate-200 pl-5 py-1 mb-8"><h5 class="text-[11px] font-semibold text-slate-400 uppercase tracking-widest mb-2">Opposition</h5><p class="text-[15px] text-slate-400 italic leading-relaxed">${highlightText(d.disagreements, searchQuery, d.acronyms)}</p></div>` : ''}
                                <div class="pt-5 border-t border-slate-100">
                                    <a href="${escapeHTML(d.pdf_url)}" target="_blank" rel="noopener" class="inline-flex items-center gap-2 text-[11px] font-medium text-slate-500 hover:text-brand-600 transition-colors uppercase tracking-widest min-h-[44px]" aria-label="Télécharger le document PDF : ${escapeHTML(d.title)}">
                                        <svg class="w-4 h-4" fill="none" stroke="currentColor" viewBox="0 0 24 24"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M12 10v6m0 0l-3-3m3 3l3-3m2 8H7a2 2 0 01-2-2V5a2 2 0 012-2h5.586a1 1 0 01.707.293l5.414 5.414a1 1 0 01.293.707V19a2 2 0 01-2 2z"></path></svg>
                                        Document source (PDF)
                                    </a>
                                </div>
                            </div>
                            <div class="lg:col-span-5">${voteWidget}</div>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>`;
}

function renderVoteBar(l, v, t, c) {
    if (v === null || v === undefined) return `<div class="flex justify-between text-[11px] font-light text-slate-300 tracking-wide"><span>${l}</span><span>—</span></div>`;
    const p = t > 0 ? (v / t * 100) : 0;
    return `<div><div class="flex justify-between text-[11px] font-medium mb-1"><span class="text-slate-400">${l}</span><span class="text-slate-600 font-semibold">${v}</span></div><div class="h-1.5 bg-slate-100 rounded-full overflow-hidden"><div class="${c} h-full rounded-full transition-all duration-500" style="width:${p}%"></div></div></div>`;
}

function toggleDelib(id) {
    const panel = document.getElementById(`content-${id}`);
    const icon = document.getElementById(`icon-${id}`);
    const btn = document.getElementById(`btn-${id}`);
    if (!panel || !icon || !btn) return;
    const isOpen = panel.classList.contains('is-open');
    panel.classList.toggle('is-open');
    icon.classList.toggle('rotate-180');
    btn.setAttribute('aria-expanded', isOpen ? 'false' : 'true');
}

init();

// Contact form
const contactForm = document.getElementById('contact-form');
const contactStatus = document.getElementById('contact-status'); // Utilisation de ta div

if (contactForm) {
    contactForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        
        const payload = {
            name: document.getElementById('contact-name').value,
            email_sender: document.getElementById('contact-email').value,
            message: document.getElementById('contact-message').value,
        };

        const API_URL = 'https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/contact';

        try {
            if (e.submitter) e.submitter.disabled = true;
            const response = await fetch(API_URL, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            // Récupération du JSON de la Lambda Go
            const data = await response.json().catch(() => ({}));

            if (response.ok) {
                contactStatus.textContent = "Message envoyé avec succès.";
                contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-green-600 bg-green-50 block";
                contactForm.reset();
            } else {
                contactStatus.textContent = data.error || "Erreur lors de l'envoi.";
                contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
            }
        } catch (error) {
            contactStatus.textContent = "Erreur réseau. Impossible de contacter le serveur.";
            contactStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
            turnstile.reset(contactWidgetId);
        } finally {
            if (e.submitter) e.submitter.disabled = false;
        }
    });
}

// Newsletter form
const newsletterForm = document.getElementById('newsletter-form');
const newsletterStatus = document.getElementById('newsletter-status');

if (newsletterForm) {
    newsletterForm.addEventListener('submit', async (e) => {
        e.preventDefault();
        
        const payload = {
            email: document.getElementById('newsletter-email').value,
        };

        const API_URL = 'https://zq7qfmhra1.execute-api.eu-west-3.amazonaws.com/prod/subscribe';

        try {
            if (e.submitter) e.submitter.disabled = true;
            const response = await fetch(API_URL, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(payload)
            });

            const data = await response.json().catch(() => ({}));

            if (response.ok) {
                newsletterStatus.innerHTML = "Vérifiez votre boîte mail pour confirmer.<br><span class='text-[11px] opacity-80'>Pensez aux spams. Si c'est le cas, marquez-le comme 'non-spam'.</span>";
                newsletterStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-green-700 bg-green-50 block leading-tight";
                newsletterForm.reset();
            } else {
                newsletterStatus.textContent = data.error || "Erreur lors de l'inscription.";
                newsletterStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
            }
        } catch (error) {
            newsletterStatus.textContent = "Erreur réseau. Impossible de contacter le serveur.";
            newsletterStatus.className = "text-sm font-medium text-center py-2 rounded-xl text-red-600 bg-red-50 block";
            turnstile.reset(newsletterWidgetId);
        } finally {
            if (e.submitter) e.submitter.disabled = false;
        }
    });
}
