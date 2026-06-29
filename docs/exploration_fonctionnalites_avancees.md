# 🔭 WatchdogCity — Exploration Technique : Alertes, Comparaison, Audio

> **Date** : 29 juin 2026  
> **Contexte** : Analyse de faisabilité détaillée pour 3 fonctionnalités avancées.  
> **Base de code** : Architecture serverless AWS (Go/ARM64 Lambda + DynamoDB + S3 + Brevo), frontend statique (HTML/JS vanilla + data.json).

---

## 1. 🔔 ALERTES THÉMATIQUES SANS COMPTE

### Résumé

L'utilisateur choisit des thèmes (Éducation, Environnement, Mobilité…) et reçoit un email **uniquement** quand une délibération correspond. Pas de création de compte : un « lien magique » envoyé par email permet de modifier ses préférences à tout moment. Combine la newsletter existante (Brevo) avec un nouveau endpoint API Gateway + Lambda + DynamoDB.

---

### Faisabilité Technique Détaillée

#### 🧱 Backend — Nouvelle Lambda `PreferenceManager` (Go)

**API Gateway — Nouveaux endpoints :**

| Méthode | Chemin | Rôle |
|---------|--------|------|
| `POST` | `/preferences` | Inscription : reçoit `{email, topics: ["Éducation", "Environnement"]}`, génère un token magique, enregistre dans DynamoDB, envoie l'email de lien magique via Brevo |
| `GET` | `/preferences?token=xxx` | Consultation : retourne les préférences actuelles de l'utilisateur (page de gestion) |
| `PUT` | `/preferences` | Mise à jour : modifie les topics (token dans le body), pas besoin d'email |

**DynamoDB — Nouvelle table `watchdog-topic-preferences` :**

```
PK: email (S)
Attributes:
  - topics (SS)            → ["Éducation", "Environnement", "Mobilité"]
  - magic_token (S)        → token opaque (SHA-256 de email + secret)
  - status (S)             → "ACTIVE" | "UNSUBSCRIBED"
  - created_at (S)         → ISO 8601
  - updated_at (S)         → ISO 8601
  - confirmed_at (S)       → ISO 8601 (null tant que lien magique non cliqué)
```

**Pourquoi une table séparée plutôt que d'enrichir `watchdog-subscribers` ?**
- La table `watchdog-subscribers` est pilotée par le flux Brevo (email → status → CONFIRMED).
- Les préférences thématiques sont un concept distinct, découplé du cycle de vie d'abonnement Brevo.
- Un utilisateur peut avoir des préférences thématiques sans être abonné à la newsletter globale.
- Évite les conflits de mise à jour concurrente entre le Subscribe Lambda et le PreferenceManager.

**Token magique — Flux :**
1. L'utilisateur saisit email + coche des thèmes sur le frontend.
2. `POST /preferences` → Lambda génère `magic_token = HMAC-SHA256(email, SECRET)`.
3. Lambda envoie un email transactionnel Brevo contenant `https://lobservatoiredebegles.fr?token=xxx`.
4. L'utilisateur clique → le frontend détecte le paramètre `?token=xxx` → `GET /preferences?token=xxx` → affiche la page de gestion.
5. L'utilisateur modifie ses thèmes → `PUT /preferences` avec token → sauvegarde.

**Règle de sécurité** : Le `magic_token` n'est jamais stocké en clair dans l'URL de manière persistante. La page de gestion ne s'affiche que si le token est valide. Un token expiré renvoie 401.

---

#### 🧱 Backend — Modification du Notifier Lambda

Le Notifier actuel envoie une campagne Brevo unique à toute la liste après chaque conseil.

**Nouveau comportement (V2) :**
1. Le Notifier génère la newsletter standard pour la liste globale (inchangé).
2. **Nouvelle étape** : Le Notifier scanne la table `watchdog-topic-preferences` pour les entrées `status=ACTIVE`.
3. Pour chaque abonné thématique, il vérifie si au moins une délibération du conseil correspond à l'un de ses topics.
4. Si oui, il envoie un **email transactionnel personnalisé** via l'API Brevo `/smtp/email` (ou `/v3/smtp/email`) contenant :
   - Objet : `[🔔 Alerte {{topic}}] Nouvelle délibération au conseil du {{date}}`
   - Corps : résumé des délibérations filtrées par thème, avec lien vers le site.

**Pourquoi l'API transactionnelle plutôt qu'une campagne par thème ?**
- Brevo facture par email, pas par campagne. Le coût est identique.
- Les campagnes sont conçues pour des envois de masse identiques. L'API transactionnelle (`/smtp/email`) permet un contenu personnalisé par destinataire.
- Pas besoin de créer N listes Brevo (une par thème) — complexe à maintenir et sujet aux désynchronisations.

**Optimisation de coût** : Si un conseil n'a aucune délibération sur le thème "Sport" et qu'aucun abonné n'a coché "Sport", le Notifier ne scanne même pas cette combinaison. Le scan est `O(nb_abonnés × nb_topics_du_conseil)`, trivial pour < 1000 abonnés.

---

#### 🎨 Frontend

**Section "Alertes Thématiques" (à ajouter sous la section newsletter existante dans index.html) :**

```html
<section id="topic-alerts" class="bg-slate-50 py-16 mb-10 text-center">
  <h3>🔔 Recevez uniquement ce qui vous intéresse</h3>
  <p>Choisissez vos thèmes — soyez alerté quand ça concerne votre quotidien.</p>
  <form id="alerts-form">
    <input type="email" id="alerts-email" required placeholder="votre@email.com">
    <div id="topic-checkboxes">
      <!-- Généré dynamiquement depuis COLORS / TopicTags -->
      <label><input type="checkbox" value="Éducation"> 🎓 Éducation</label>
      <label><input type="checkbox" value="Environnement"> 🌱 Environnement</label>
      ...
    </div>
    <button type="submit">Activer mes alertes</button>
  </form>
  <div id="alerts-confirm" class="hidden">
    📬 Vérifiez votre boîte mail pour gérer vos préférences.
  </div>
</section>
```

**Gestion des préférences (page accessible via `?token=xxx`) :**
- Le JavaScript détecte le paramètre `token` dans l'URL au chargement.
- Appelle `GET /preferences?token=xxx` → affiche les checkboxes pré-cochées.
- L'utilisateur modifie → `PUT /preferences` → confirmation.

**Integration avec le code existant :**
- Réutilisation des constantes `COLORS` et des noms de thèmes déjà dans `app.js` (ligne 23-35).
- Réutilisation de la fonction `getAvailableTopics()` (ligne 235-243).
- Le endpoint API Gateway sera sur le même domaine que `/subscribe` et `/contact`.

---

### Estimation d'Effort

| Composant | Heures |
|-----------|--------|
| DynamoDB : nouvelle table `watchdog-topic-preferences` (CDK) | 2h |
| Lambda `PreferenceManager` (Go, 3 endpoints, logique token) | 8h |
| Tests unitaires PreferenceManager | 3h |
| Modification Notifier Lambda (scan topic-preferences + envoi transactionnel Brevo) | 6h |
| Tests Notifier (mock Brevo, mock DDB) | 3h |
| Frontend : UI inscription + checkboxes + validation | 5h |
| Frontend : page gestion préférences (token magique) | 4h |
| Template email Brevo pour alerte thématique | 2h |
| Intégration, déploiement CDK, tests E2E | 4h |
| **Total** | **~37h** |

**Classification** : **Effort Moyen-Élevé** (semaine de travail complète). Plus lourd que l'estimation initiale dans INNOVATIONS.md car l'implémentation correcte du lien magique + API transactionnelle Brevo + séparation en table dédiée ajoute de la robustesse mais aussi de la complexité.

---

### Valeur Ajoutée

- ⭐⭐⭐⭐ **Personnalisation sans friction** : Pas de mot de passe, juste un email. Le taux de conversion sera bien supérieur à un système avec compte.
- ⭐⭐⭐⭐ **Rétention** : La newsletter globale a un taux d'ouverture correct mais dilué. L'alerte thématique est ultra-pertinente → taux d'ouverture très élevé.
- ⭐⭐⭐ **Différenciation** : Aucun autre observatoire citoyen ne propose cela. Positionne WatchdogCity comme un outil proactif, pas juste un site à visiter.
- ⭐⭐⭐ **Vertu budgétaire** : Coût marginal quasi nul (Brevo transactional 0€ jusqu'à un certain volume, DynamoDB pay-per-request).

### Risques et Limites

| Risque | Mitigation |
|--------|------------|
| **Délivrabilité** : Les emails transactionnels peuvent finir en spam si le domaine est nouveau | DKIM/SPF déjà configuré sur le domaine Brevo ; le warming progressif de l'IP n'est pas nécessaire pour du transactionnel |
| **Token magique partagé** : Un utilisateur forward son email de gestion → quelqu'un d'autre modifie ses préférences | Accepter ce risque : c'est du volontaire (l'utilisateur forwarde son propre email). Ajouter un bouton "Se désabonner" dans chaque alerte. |
| **Surcharge Notifier** : Si 500 abonnés × 5 topics, le scan est rapide mais les envois transactionnels Brevo sont sérialisés | L'API Brevo accepte des appels parallèles. Utiliser des goroutines avec un semaphore (concurrency=10) pour envoyer en parallèle sans saturer le rate limit Brevo. |
| **Désynchronisation Brevo <> DynamoDB** : Un utilisateur se désabonne via Brevo (lien unsubscribe global) mais reste dans `watchdog-topic-preferences` | Le Notifier doit vérifier le statut du contact dans Brevo avant envoi (GET /contacts/{email}) — ou accepter que le webhook Brevo nettoie la table (complexe). Mitigation simple : inclure un lien de désabonnement dans chaque email transactionnel. |
| **Coût Brevo transactionnel** : Si 200 abonnés thématiques recoivent 1 email par conseil (2/mois) = 400 emails/mois | Brevo offre 300 emails/jour gratuits en transactionnel. Largement suffisant. |

---

## 2. ⚖️ MODE COMPARAISON DE CONSEILS

### Résumé

Sélectionner 2 conseils municipaux et les comparer côte à côte sur plusieurs indicateurs : nombre de délibérations, budget total, répartition thématique, climat des votes, taux de délibérations substantielles. Utile pour analyser l'évolution de l'activité municipale dans le temps.

---

### Faisabilité Technique Détaillée

#### ⚡ Zéro Backend — 100% Client-Side

C'est le point clé : **toutes les données nécessaires sont déjà dans `data.json`**. Aucune Lambda, aucun endpoint API Gateway, aucune modification DynamoDB n'est nécessaire.

Le `data.json` contient déjà :
```json
{
  "councils": [{
    "id": "...",
    "date": "2025-07-03",
    "title": "...",
    "analysis": {
      "budget_impact": 0,
      "vote_climat": "...",
      "vote_summary": "..."
    },
    "deliberations": [{
      "topic_tag": "Environnement",
      "budget_impact": 100000,
      "budget_type": "DÉPENSE",
      "vote": { "pour": 30, "contre": 5, "abstention": 2 },
      "is_substantial": true,
      ...
    }]
  }]
}
```

Tout le travail est dans l'UI.

#### 🎨 Frontend — Architecture de la Feature

**1. Sélecteur de conseils**

Ajouter un bouton "⚖️ Comparer" dans la navbar (à côté de "Actualités" et "Budget").

```javascript
function toggleComparisonMode() {
  // Active le mode comparaison : chaque carte de conseil affiche une checkbox
  // L'utilisateur coche exactement 2 conseils
  // Un bouton "Comparer les 2 conseils sélectionnés" apparaît en sticky bottom bar
}
```

**2. Vue de comparaison**

Une fois 2 conseils sélectionnés, une slide-up modal ou une nouvelle section affiche :

```
┌─────────────────────────────────────────────────────┐
│  ⚖️ Comparaison                                      │
│  Conseil A : 3 juillet 2025  vs  Conseil B : 17 déc 2025 │
├────────────────────┬────────────────────────────────┤
│                    │     A          B        Δ      │
│  Délibérations     │    20         15       -5     │
│  Budget total      │  2.3M€      1.8M€   -500k€   │
│  Thème dominant    │  Social   Urbanisme           │
│  Votes contestés   │    3          2        -1     │
│  % Unanimité       │   85%        87%      +2%     │
│  Délib. subst.     │   12          8        -4     │
├────────────────────┴────────────────────────────────┤
│  📊 Répartition thématique                           │
│  [Barres côte à côte par thème]                      │
│                                                      │
│  📊 Budget par thème                                 │
│  [Barres groupées A/B par thème]                     │
│                                                      │
│  📊 Climat des votes (camembert A vs B)               │
└─────────────────────────────────────────────────────┘
```

**3. Métriques calculées (JavaScript pur, dans `app.js`)**

```javascript
function compareCouncils(councilA, councilB) {
  const delibsA = councilA.deliberations || [];
  const delibsB = councilB.deliberations || [];

  // Nombre de délibérations
  const countA = delibsA.length;
  const countB = delibsB.length;

  // Budget total
  const budgetA = delibsA.reduce((s, d) => s + (d.budget_impact || 0), 0);
  const budgetB = delibsB.reduce((s, d) => s + (d.budget_impact || 0), 0);

  // Répartition thématique
  const themesA = {};
  const themesB = {};
  delibsA.forEach(d => {
    const t = d.topic_tag || 'Autres';
    themesA[t] = (themesA[t] || 0) + 1;
  });
  delibsB.forEach(d => {
    const t = d.topic_tag || 'Autres';
    themesB[t] = (themesB[t] || 0) + 1;
  });

  // Budget par thème
  const budgetByThemeA = {};
  const budgetByThemeB = {};
  delibsA.forEach(d => {
    if (d.budget_impact > 0) {
      const t = d.topic_tag || 'Autres';
      budgetByThemeA[t] = (budgetByThemeA[t] || 0) + d.budget_impact;
    }
  });
  delibsB.forEach(d => {
    if (d.budget_impact > 0) {
      const t = d.topic_tag || 'Autres';
      budgetByThemeB[t] = (budgetByThemeB[t] || 0) + d.budget_impact;
    }
  });

  // Votes contestés
  const contestedA = delibsA.filter(d => d.vote && (d.vote.contre || 0) > 0).length;
  const contestedB = delibsB.filter(d => d.vote && (d.vote.contre || 0) > 0).length;

  // Taux d'unanimité
  const votedA = delibsA.filter(d => d.vote && (d.vote.pour || d.vote.contre || d.vote.abstention));
  const unanimousA = votedA.filter(d => (d.vote.contre || 0) === 0).length;
  const unanimityRateA = votedA.length > 0 ? (unanimousA / votedA.length * 100) : 100;

  // Même calcul pour B...

  // Délibérations substantielles
  const substantialA = delibsA.filter(d => d.is_substantial).length;
  const substantialB = delibsB.filter(d => d.is_substantial).length;

  return {
    counts: { a: countA, b: countB, delta: countB - countA },
    budgets: { a: budgetA, b: budgetB, delta: budgetB - budgetA },
    themes: { a: themesA, b: themesB },
    budgetByTheme: { a: budgetByThemeA, b: budgetByThemeB },
    contested: { a: contestedA, b: contestedB },
    unanimityRates: { a: unanimityRateA, b: unanimityRateB },
    substantial: { a: substantialA, b: substantialB }
  };
}
```

**4. Visualisations**

Pour les graphiques comparatifs, deux options :

| Option | Librairie | Poids | Avantage |
|--------|-----------|-------|----------|
| A | Chart.js (CDN) | ~60 KB gzippé | Robuste, responsive, bien documenté |
| B | CSS pur (barres horizontales) | 0 KB | Zéro dépendance, cohérent avec le design actuel |

**Recommandation : Option B (CSS pur)** — Le site est déjà 100% vanilla JS sans dépendance lourde. Les barres de progression CSS sont déjà utilisées pour les rubans budgétaires (app.js, lignes 169-173). L'approche CSS-only avec des `div` colorées reste dans l'esprit du projet et évite d'ajouter une dépendance.

**5. Delta et surbrillance**

Les variations sont mises en évidence :
- Delta positif → vert (`text-emerald-600`)
- Delta négatif → rouge/rose (`text-rose-600`)
- Delta nul → gris (`text-slate-400`)

---

### Estimation d'Effort

| Composant | Heures |
|-----------|--------|
| Fonction `compareCouncils()` dans app.js | 3h |
| UI : mode sélection (checkboxes sur les conseils) | 4h |
| UI : panneau de comparaison (tableau + barres CSS) | 6h |
| UI : graphiques thématiques comparatifs (barres groupées CSS) | 5h |
| UI : responsive design (mobile : stacked au lieu de side-by-side) | 3h |
| Tests navigateur (Chrome, Firefox, Safari, mobile) | 3h |
| Accessibilité (labels ARIA, navigation clavier) | 2h |
| **Total** | **~26h** |

**Classification** : **Effort Faible-Moyen**. Aucun backend. Tout est dans le JavaScript existant. La complexité est purement UI/UX.

---

### Valeur Ajoutée

- ⭐⭐⭐⭐⭐ **Transparence temporelle** : Le citoyen voit l'évolution de l'activité municipale, pas juste un instantané. C'est la feature qui transforme le site d'un « album photo » en un « film ».
- ⭐⭐⭐⭐ **Journalistes et analystes** : Usage par des journalistes locaux ou des oppositions municipales pour argumenter (« Le budget Environnement a baissé de 40% entre juillet et décembre »).
- ⭐⭐⭐⭐ **Engagement** : La comparaison crée de la curiosité. Le visiteur reste plus longtemps sur le site.
- ⭐⭐⭐ **Zéro coût marginal** : Aucune infrastructure supplémentaire.

### Risques et Limites

| Risque | Mitigation |
|--------|------------|
| **Comparaison trompeuse** : Comparer un conseil de juillet (20 délibérations, budget primitif) avec un conseil de septembre (5 délibérations, routine administrative) n'a pas de sens | Ajouter une note contextuelle : « La comparaison est indicative. Les conseils n'ont pas tous le même ordre du jour. Le budget primitif (généralement en décembre/janvier) concentre les décisions financières majeures. » |
| **data.json volumineux** : Le fichier actuel fait ~130 KB. Si le nombre de conseils double, il pourrait atteindre 300-500 KB | Acceptable pour du statique (c'est chargé une fois, mis en cache par CloudFront et le navigateur). La comparaison ne nécessite pas de données supplémentaires. |
| **UX de sélection sur mobile** : Cocher 2 conseils sur un petit écran peut être fastidieux | Utiliser un sélecteur de type "dropdown" plutôt que des checkboxes sur mobile. Ou un swipe left/right pour sélectionner. |
| **Pas de comparaison possible avec un seul conseil** | Message d'encouragement : « Ajoutez un deuxième conseil pour activer la comparaison » avec un bouton « Charger plus de conseils ». |

---

## 3. 🎙️ VERSION AUDIO / PODCAST

### Résumé

Synthèse vocale automatique du résumé de chaque conseil municipal. Accessibilité pour les malvoyants et commodité (écouter en mobilité, en faisant autre chose). Deux approches : Web Speech API (client-side, gratuit, immédiat) et Amazon Polly (backend, qualité studio, flux RSS podcast).

---

### Faisabilité Technique Détaillée

#### Approche A : Web Speech API (Client-Side, MVP)

**Principe** : L'API `window.speechSynthesis` est disponible dans tous les navigateurs modernes. Elle permet de lire du texte avec une voix synthétique dans la langue du navigateur.

**Implémentation (dans app.js) :**

```javascript
// Ajouter un bouton "🔊 Écouter" sur chaque carte de conseil
function renderAudioButton(council) {
  const text = buildAudioScript(council);
  return `<button 
    onclick="toggleSpeech('${council.id}', this)" 
    class="audio-btn flex items-center gap-2 px-3 py-1.5 rounded-full bg-slate-100 hover:bg-slate-200"
    aria-label="Écouter le résumé du conseil"
  >
    <svg>...</svg> Écouter
  </button>`;
}

function buildAudioScript(council) {
  // Construit un script textuel à partir du résumé du conseil
  let script = `Conseil municipal du ${formatDate(council.date)}. `;
  
  // Résumé global
  if (council.analysis?.vote_summary) {
    script += council.analysis.vote_summary + ". ";
  }
  
  // Délibérations clés
  const delibs = council.deliberations || [];
  const keyDelibs = delibs.filter(d => d.is_substantial || d.budget_impact > 0);
  
  if (keyDelibs.length > 0) {
    script += `${keyDelibs.length} décision${keyDelibs.length > 1 ? 's' : ''} majeure${keyDelibs.length > 1 ? 's' : ''}. `;
    keyDelibs.slice(0, 5).forEach((d, i) => {
      script += `${i+1}. ${d.title}. ${d.summary?.substring(0, 200)}. `;
    });
  }
  
  // Climat des votes
  if (council.analysis?.vote_climat) {
    script += `Climat des votes : ${council.analysis.vote_climat}. `;
  }
  
  return script;
}

let currentSpeech = null;

function toggleSpeech(councilId, button) {
  if (currentSpeech) {
    window.speechSynthesis.cancel();
    currentSpeech = null;
    resetAllAudioButtons();
    return;
  }
  
  const council = allCouncils.find(c => c.id === councilId);
  if (!council) return;
  
  const text = buildAudioScript(council);
  const utterance = new SpeechSynthesisUtterance(text);
  utterance.lang = 'fr-FR';
  utterance.rate = 0.9;  // Légèrement ralenti pour la clarté
  
  // Essayer d'utiliser une voix française
  const voices = window.speechSynthesis.getVoices();
  const frenchVoice = voices.find(v => v.lang.startsWith('fr'));
  if (frenchVoice) utterance.voice = frenchVoice;
  
  utterance.onend = () => {
    currentSpeech = null;
    button.classList.remove('playing');
  };
  
  currentSpeech = councilId;
  button.classList.add('playing');
  window.speechSynthesis.speak(utterance);
}
```

**Limites de la Web Speech API :**
- Qualité vocale variable selon le navigateur et l'OS (bonne sur Chrome/Edge, médiocre sur Firefox).
- Pas de contrôle fin sur la prosodie.
- Pas de possibilité d'exporter en MP3 → pas de podcast RSS.
- L'utilisateur doit rester sur la page (pas de lecture en arrière-plan sur mobile).

---

#### Approche B : Amazon Polly (Backend, Qualité Studio)

**Principe** : Après la validation d'un conseil (statut APPROVED), une nouvelle étape dans le pipeline génère un fichier audio MP3 via Amazon Polly (voix neuronale française "Léa" ou "Mathieu") et le stocke sur S3.

**Pipeline modifié :**

```
Validator Lambda (statut APPROVED)
  └─► Publisher Lambda (génère data.json)
        └─► NOUVEAU : AudioGenerator Lambda
              ├─ Construit un script audio formaté (SSML pour Polly)
              ├─ Appelle Polly SynthesizeSpeech (voix "Léa", format mp3)
              ├─ Upload le MP3 dans S3 : podcast/YYYY-MM-DD-conseil.mp3
              └─ Met à jour DynamoDB : council.audio_url = URL CloudFront
```

**Pourquoi une Lambda séparée plutôt que dans le Publisher ?**
- Polly `SynthesizeSpeech` peut prendre 10-30 secondes pour un script de 3-5 minutes. Le temps d'exécution du Publisher est actuellement < 3 secondes.
- Isoler permet un retry indépendant (si Polly échoue, le site reste fonctionnel).
- La Lambda AudioGenerator peut être déclenchée de manière asynchrone (via EventBridge ou SQS) sans bloquer la publication.

**Format du script audio (SSML — Speech Synthesis Markup Language) :**

```xml
<speak>
  <amazon:domain name="news">
    <prosody rate="medium" pitch="medium">
      <p>Bonjour, voici le résumé vocal du conseil municipal de Bègles du 3 juillet 2025.</p>
      <break time="500ms"/>
      <p>Ce conseil a traité 20 délibérations, pour un budget total de 2,3 millions d'euros.</p>
      <break time="300ms"/>
      <p>Le climat des votes était majoritairement consensuel, avec seulement 3 délibérations ayant suscité une opposition.</p>
      <break time="500ms"/>
      <p>Première décision majeure : Acquisition du Parc Paty...</p>
    </prosody>
  </amazon:domain>
</speak>
```

**Génération du script SSML (dans AudioGenerator Lambda, Go) :**

```go
func buildSSML(council *Council, delibs []Deliberation) string {
    var b strings.Builder
    b.WriteString(`<speak><amazon:domain name="news"><prosody rate="medium">`)
    
    fmt.Fprintf(&b, `<p>Bonjour, voici le résumé du conseil municipal du %s.</p><break time="400ms"/>`, 
        formatDateFR(council.Date))
    
    fmt.Fprintf(&b, `<p>%d délibérations ont été examinées.</p><break time="300ms"/>`, 
        len(delibs))
    
    if council.Analysis.VoteClimat != "" {
        fmt.Fprintf(&b, `<p>Climat des votes : %s.</p><break time="300ms"/>`, 
            council.Analysis.VoteClimat)
    }
    
    // Décisions clés
    for _, d := range keyDelibs {
        fmt.Fprintf(&b, `<p>%s. %s.</p><break time="300ms"/>`, 
            escapeXML(d.Title), escapeXML(d.Summary[:min(300, len(d.Summary))]))
    }
    
    b.WriteString(`</prosody></amazon:domain></speak>`)
    return b.String()
}
```

**Stockage S3 et distribution :**
- Bucket : `watchdogcity-podcasts` (public, CloudFront)
- Chemin : `podcasts/YYYY/MM/DD-conseil.mp3`
- URL dans data.json : champ `audio_url` ajouté par le Publisher

**Flux RSS Podcast :**
- Fichier `podcast.xml` généré dans S3 listant les 20 derniers épisodes.
- Format compatible Apple Podcasts, Spotify, Google Podcasts.
- Généré par le Publisher Lambda en même temps que data.json.

```xml
<rss xmlns:itunes="http://www.itunes.com/dtds/podcast-1.0.dtd" version="2.0">
  <channel>
    <title>L'Observatoire de Bègles - Podcast</title>
    <description>Résumés audio des conseils municipaux de Bègles</description>
    <item>
      <title>Conseil municipal du 3 juillet 2025</title>
      <enclosure url="https://cdn.lobservatoiredebegles.fr/podcasts/2025-07-03-conseil.mp3" 
                 length="3145728" type="audio/mpeg"/>
      <pubDate>Thu, 03 Jul 2025 18:00:00 GMT</pubDate>
      <duration>05:30</duration>
    </item>
  </channel>
</rss>
```

---

### Approche Hybride Recommandée

| Phase | Approche | Effort | Délai |
|-------|----------|--------|-------|
| **Phase 1 (MVP)** | Web Speech API — bouton "Écouter" sur chaque conseil | 4-6h | 1 jour |
| **Phase 2 (Podcast)** | Amazon Polly + Lambda AudioGenerator + RSS | 10-14h | 3-4 jours |
| **Total** | Les deux phases combinées | **14-20h** | 1 semaine |

La Phase 1 apporte une valeur immédiate (accessibilité, commodité) sans infrastructure. La Phase 2 apporte la qualité studio et la distribution podcast pour une audience élargie.

---

### Estimation d'Effort Détaillée

| Composant | Heures |
|-----------|--------|
| **Phase 1 : Web Speech API** | |
| Fonction `buildAudioScript()` | 2h |
| Fonction `toggleSpeech()` avec gestion d'état | 2h |
| UI : bouton "Écouter" + animation playing/pause | 1.5h |
| Tests cross-browser (Chrome, Safari, Firefox, mobile) | 1.5h |
| Accessibilité (WCAG, labels ARIA) | 1h |
| **Sous-total Phase 1** | **8h** |
| **Phase 2 : Amazon Polly** | |
| Lambda `AudioGenerator` (Go, ~200 lignes) | 5h |
| Génération SSML (structuré, pauses, emphase) | 2h |
| Intégration Polly SDK (`SynthesizeSpeech`) | 2h |
| Upload S3 + URL CloudFront dans DynamoDB | 1h |
| Modification `Publisher Lambda` : ajout champ `audio_url` dans data.json | 1h |
| Génération `podcast.xml` (RSS feed) | 3h |
| Tests unitaires AudioGenerator | 2h |
| Déploiement CDK (nouvelle Lambda, bucket S3, permissions IAM) | 2h |
| Frontend : remplacement bouton Web Speech par lecteur HTML5 `<audio>` | 1.5h |
| **Sous-total Phase 2** | **19.5h** |
| **Total Combiné** | **~27.5h** |

**Classification** : **Effort Moyen** (Phase 1 seule = Faible, Phase 1+2 = Moyen). L'approche hybride est recommandée car elle permet de livrer rapidement puis d'itérer.

---

### Valeur Ajoutée

- ⭐⭐⭐⭐⭐ **Accessibilité** : Les malvoyants et non-voyants peuvent enfin accéder au contenu. C'est un enjeu légal (RGAA en France) et éthique. La Web Speech API seule suffit pour cet usage.
- ⭐⭐⭐⭐ **Convenience** : Écouter le résumé d'un conseil en 5 minutes dans les transports ou en cuisinant. Élargit l'audience au-delà des lecteurs.
- ⭐⭐⭐⭐ **Distribution podcast** : Être référencé sur Apple Podcasts et Spotify donne une visibilité nationale au projet. Un auditeur de Paris pourrait suivre Bègles par curiosité civique.
- ⭐⭐⭐ **Image de marque** : Un observatoire citoyen qui propose un podcast, c'est moderne et sérieux. Renforce la crédibilité.
- ⭐⭐ **Monétisation indirecte** : Le podcast peut mentionner les services de développement de Jules (le créateur) en fin d'épisode — « Ce podcast est produit par l'Observatoire Citoyen de Bègles, un projet bénévole de Jules Laconfourque, développeur cloud. »

### Risques et Limites

| Risque | Mitigation |
|--------|------------|
| **Qualité Polly « Léa »** : La voix neuronale est excellente mais peut sembler trop « parfaite » / artificielle sur des noms propres béglais | Le SSML permet d'ajouter des balises `<phoneme>` pour les noms propres difficiles. Ex : `<phoneme alphabet="ipa" ph="bɛɡl">Bègles</phoneme>`. |
| **Coût Polly** : ~4 $ par million de caractères en neural. Un conseil de 20 délibérations → script d'environ 3000-5000 caractères → 0.012-0.02 $ par conseil. À 2 conseils/mois = 0.40 $/an | Négligeable. |
| **Coût S3 + CloudFront** : Un MP3 de 5 minutes en 64 kbps ≈ 2.5 Mo. Avec 100 écoutes/mois → 250 Mo de bande passante → ~0.02 $ | Négligeable. |
| **Web Speech API inconsistante** : Firefox a des voix limitées, Safari iOS peut couper la lecture en arrière-plan | Feature detection : si `speechSynthesis` n'est pas bien supporté, afficher un message « Votre navigateur ne supporte pas la lecture audio. Essayez Chrome ou utilisez le lecteur MP3. » Le lecteur MP3 (Phase 2) devient le fallback. |
| **RSS Feed : validation iTunes** | Apple Podcasts a des exigences strictes (cover art 1400x1400, catégories iTunes, balise `<itunes:author>`). Utiliser un validateur RSS avant déploiement. |
| **Dépendance à Amazon Polly** | Si AWS change les prix ou déprécie la voix "Léa", la Web Speech API reste le fallback. |

---

## 📋 Synthèse Globale

### Tableau Comparatif

| Critère | 🔔 Alertes Thématiques | ⚖️ Comparaison | 🎙️ Audio/Podcast |
|---------|----------------------|----------------|-------------------|
| **Backend requis** | Oui (Lambda + DynamoDB + API Gateway) | Non (100% client-side) | Phase 1: Non / Phase 2: Oui (Lambda + S3) |
| **Effort total** | ~37h (Moyen-Élevé) | ~26h (Faible-Moyen) | ~27.5h (Moyen) |
| **Valeur citoyenne** | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ |
| **Coût marginal** | Très faible | Zéro | Très faible |
| **Risque technique** | Moyen (intégration Brevo transactionnel) | Faible (pur JS) | Faible (Phase 1) / Moyen (Phase 2 : Polly+RSS) |
| **Dépendances externes** | Brevo API transactionnelle | Aucune | Web Speech API (navigateur) / AWS Polly |
| **Délai de mise en prod** | 1.5-2 semaines | 1 semaine | Phase 1: 1 jour / Phase 2: 1 semaine |
| **Maintenabilité** | Bonne (table séparée) | Excellente (pas de backend) | Bonne (Lambda isolée) |

---

### Recommandation de Priorité

1. **⚖️ Comparaison de Conseils** — *À faire en premier.* Zéro backend, fort impact citoyen, valorise les données déjà disponibles. Effet immédiat sur l'engagement du site.

2. **🎙️ Audio/Podcast (Phase 1 Web Speech API)** — *Quick win en 1 jour.* Accessibilité immédiate, différenciation. La Phase 2 (Polly + RSS) peut suivre dans la foulée ou être planifiée plus tard.

3. **🔔 Alertes Thématiques** — *Le plus transformateur mais le plus lourd.* Nécessite du backend, des tests d'intégration Brevo, et une réflexion sur l'UX du lien magique. À planifier quand le socle est stable.

---

### Points d'Attention Transverses

- **Cohérence de l'expérience** : Les 3 features doivent utiliser les mêmes constantes de thèmes (`COLORS`, `TopicTags`) déjà dans `app.js`. Ne pas dupliquer la logique de thèmes.
- **Performance du data.json** : Le fichier fait actuellement ~130 KB. La comparaison n'ajoute pas de données. Les alertes n'impactent pas le frontend. L'audio ajoute une URL (100 octets par conseil). Le fichier reste léger.
- **Accessibilité (a11y)** : Le site est déjà bien conçu (labels ARIA, navigation clavier). Les 3 features doivent maintenir ce standard : la comparaison doit être navigable au clavier, les alertes doivent avoir des labels explicites, le lecteur audio doit avoir des contrôles accessibles.
- **Tests** : La comparaison est testable unitairement en pur JavaScript (fonction `compareCouncils()`). Les alertes nécessitent des tests d'intégration Brevo (mode bac à sable). L'audio Polly nécessite un test de bout en bout sur un conseil réel.
