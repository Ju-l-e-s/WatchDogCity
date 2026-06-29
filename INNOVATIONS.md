# 🔭 WatchdogCity — Propositions de Fonctionnalités Innovantes

> **Contexte** : Observatoire citoyen des conseils municipaux de Bègles (site statique HTML/JS, données dans `data.json`, backend serverless Lambda Go, analyse par IA des PDFs officiels). Public : citoyens non-experts. Principe : neutralité, transparence, accessibilité.

---

## 1. 📍 Cartographie des Délibérations (Carte Interactive)

**Valeur pour le citoyen** :
Les Béglais visualisent instantanément où les décisions du conseil municipal impactent leur quartier. Une délibération sur un parc, une école, une rue, un équipement public — tout apparaît sur une carte interactive de la commune. Plus besoin de lire des adresses dans le texte : un coup d'œil suffit pour savoir si « ça concerne mon quartier ». Cela rend la politique municipale concrète et géolocalisée.

**Faisabilité technique** :
- **Pipeline** : Ajouter un champ `geo_entities` (array d'objets `{name, type, lat, lng}`) extrait par le Worker Lambda via Gemini (prompt : « Liste les lieux mentionnés avec leurs coordonnées si disponibles »).
- **data.json** : Ajouter `geo_entities` dans chaque délibération.
- **Frontend** : Intégrer Leaflet.js (libre, léger, pas de clé API). Ajouter un onglet « Carte » dans la navbar. Afficher les marqueurs clusterisés par conseil avec popup résumant la délibération.
- **Validator** : Ajouter règle D11 — cohérence géographique (un lieu hors Bègles → WARN).

**Effort estimé** : **Moyen** — Pipeline + frontend conséquent, mais Leaflet simplifie le rendu.

---

## 2. 🔔 Alertes Thématiques Personnalisables (Sans Compte)

**Valeur pour le citoyen** :
Chaque citoyen a ses centres d'intérêt : un parent suit les délibérations « Éducation », un cycliste suit « Mobilité », un riverain du parc Paty suit « Environnement ». L'utilisateur choisit ses thèmes et recoit une alerte email uniquement quand une délibération correspond. Sans création de compte lourd — juste un lien magique de gestion des préférences envoyé par email. C'est la newsletter, mais personnalisée.

**Faisabilité technique** :
- **Frontend** : Ajouter un sélecteur de thèmes (checkboxes) sur la section newsletter existante.
- **API Gateway + Lambda** : Nouveau endpoint `POST /preferences` (token magique dans l'URL) et `PUT /preferences` pour modifier.
- **DynamoDB** : Ajouter un champ `topic_preferences` (array de strings) dans la table `watchdog-subscribers`.
- **Pipeline (Notifier)** : Modifier le Notifier pour filtrer les délibérations par thème avant envoi. Utiliser les `topic_tag` déjà disponibles.
- **Brevo** : Un seul template d'email, mais le contenu est dynamique côté Lambda.

**Effort estimé** : **Moyen** — Nouvelle Lambda, modification Notifier, UI additionnelle.

---

## 3. 📊 Suivi des Promesses — « Ils avaient dit... »

**Valeur pour le citoyen** :
Les conseils municipaux votent des décisions dont la mise en œuvre s'étale sur des mois ou des années. « Ils ont voté la rénovation du gymnase en mars 2025, où en est-on en juin 2026 ? » Cette fonctionnalité identifie les délibérations qui constituent des engagements (ex : lancement d'un projet, promesse de livraison) et permet de suivre leur état d'avancement. Un compteur visuel « Engagements tenus / En attente » responsabilise l'équipe municipale.

**Faisabilité technique** :
- **Pipeline (Worker)** : Ajouter au prompt Gemini l'extraction d'un champ `commitment` : `{is_commitment: bool, expected_completion: string|null, milestone: string|null}`.
- **data.json** : Ajouter `commitment` dans les délibérations.
- **Pipeline (Orchestrator)** : Nouveau mode « re-scrape » qui vérifie si des documents récents mentionnent l'aboutissement d'un engagement antérieur (ex : une nouvelle délibération qui référence un numéro de délibération antérieure comme « réalisée »).
- **Frontend** : Nouvel onglet « Engagements » avec timeline des promesses, statut (en cours/achevé/sans nouvelle), et jauge globale.
- **DynamoDB** : Table `watchdog-commitments` avec liens vers délibérations source et résolution.

**Effort estimé** : **Élevé** — Nouvelle table, logique de matching inter-conseils, UI dédiée. Mais très fort impact citoyen.

---

## 4. 💬 Explication Pédagogique — « Comme si j'avais 12 ans »

**Valeur pour le citoyen** :
Une délibération technique sur un PLU, une DSP ou un budget primitif peut être illisible pour un non-expert. Cette fonctionnalité propose, en un clic, un résumé ultra-simplifié (niveau collège, ~3 phrases) généré par IA, distinct du résumé factuel actuel. Exemple : *« Le PLU, c'est le règlement qui décide où on peut construire des maisons ou des immeubles dans la ville. Aujourd'hui, le conseil a modifié une règle pour permettre plus de logements près de la gare. »* Accompagné d'un mini-glossaire contextuel des sigles.

**Faisabilité technique** :
- **Pipeline (Validator, phase sensory deprivation)** : Ajouter une étape qui génère un champ `eli5_summary` via Gemini, avec prompt contraint : « Explique cette décision comme à un enfant de 12 ans, en 3 phrases maximum. Remplace tous les sigles. »
- **data.json** : Ajouter `eli5_summary` (string).
- **Frontend** : Ajouter un bouton « Simplifier » ou « 🧒 Version simple » sur chaque carte de délibération qui bascule entre le résumé standard et le résumé ELI5. Stocké côté client (pas d'appel API — déjà dans data.json).
- **Validator** : Nouvelle règle D12 : `eli5_summary` contient des sigles → WARN.

**Effort estimé** : **Faible** — Un champ supplémentaire dans le pipeline LLM existant, un toggle UI simple.

---

## 5. 🗳️ Comparateur de Votes — « Comment votent vos élus ? »

**Valeur pour le citoyen** :
Transparence ultime : pour chaque délibération avec vote nominatif (quand disponible dans les PDFs), afficher qui a voté pour, contre, ou s'est abstenu. Sur la durée, produire un « profil de vote » par élu : taux de présence, % de votes avec la majorité, % d'abstention, thèmes sur lesquels il/elle vote contre. Le citoyen peut ainsi évaluer la cohérence de ses élus.

**Faisabilité technique** :
- **Pipeline (Worker)** : Ajouter au prompt Gemini l'extraction des votes nominatifs quand le PDF les contient : `named_votes: [{name: string, vote: "pour"|"contre"|"abstention"}]`.
- **data.json** : Ajouter `named_votes` dans les délibérations.
- **Frontend** :
  - Onglet « Élus » existant → l'enrichir avec un tableau de bord par élu (taux de présence, % votes majorité, % opposition).
  - Sur chaque délibération : afficher les noms si disponibles.
  - Graphique radar ou barres comparant les élus sur les grands thèmes.
- **Validator** : Règle D13 — un élu apparaît avec >1 vote sur la même délibération → HIGH.

**Effort estimé** : **Élevé** — Dépend de la disponibilité des votes nominatifs dans les PDFs (pas toujours présents). Si présents, l'extraction est fiable. L'UI de comparaison est le plus gros morceau.

---

## 6. 🔍 Moteur de Recherche Sémantique (Vector Search)

**Valeur pour le citoyen** :
La recherche actuelle est textuelle (mot-clé exact). Une recherche sémantique permet de taper « espaces verts », « sécurité routière » ou « cantine scolaire » et de trouver les délibérations pertinentes même si ces mots exacts n'apparaissent pas dans le titre. Par exemple, « aire de jeux » trouvera une délibération intitulée « Réaménagement square Jean Moulin » parce que le résumé mentionne une aire de jeux.

**Faisabilité technique** :
- **Pipeline (Aggregator)** : Après validation, générer des embeddings vectoriels (via un modèle léger comme `all-MiniLM-L6-v2` ou via l'API Gemini embedding) pour chaque délibération (titre + summary concaténés).
- **data.json** : Ajouter `embedding` (array de floats, ~384 dimensions). Alternative : fichier séparé `embeddings.json` (plusieurs Mo) chargé à la demande.
- **Frontend** : Utiliser une bibliothèque légère de vector search côté client (ex : `vectra` en WASM, ou une implémentation simple de cosine similarity en vanilla JS sur <500 délibérations — totalement faisable).
- **Fallback** : Si le fichier d'embeddings est trop lourd (>1 Mo), déporter la recherche vers une Lambda via API Gateway (endpoint `GET /search?q=...`).

**Effort estimé** : **Moyen** — Calcul d'embeddings côté pipeline (coût API modeste), recherche côté client faisable sans backend. Alternative full-client si le volume reste <1000 délibérations.

---

## 7. 📈 Tableau de Bord Climat — Impact Environnemental des Décisions

**Valeur pour le citoyen** :
Le champ `climate_impact` (positif/neutre/négatif) existe déjà. Transformons-le en un véritable tableau de bord environnemental : un « budget carbone » estimé des décisions municipales. Graphique en jauges : combien de délibérations sont favorables au climat ce trimestre ? Évolution dans le temps ? Quels sont les thèmes les plus « verts » et les plus « gris » ?

**Faisabilité technique** :
- **data.json** : Le champ `climate_impact` est déjà présent. Ajouter optionnellement `climate_confidence` (float 0-1) et `climate_rationale` (string court) extraits par le Worker.
- **Frontend** : Nouveau sous-onglet « 🌱 Climat » dans la vue Budget (ou onglet dédié). Afficher :
  - Jauge circulaire : % de délibérations à impact positif par conseil
  - Timeline empilée : nombre de délibérations positif/neutre/négatif par mois
  - Nuage de tags climatiques
- **Aggregator** : Ajouter l'agrégation `climate_stats` dans l'objet `analysis` du conseil.

**Effort estimé** : **Faible** — Données déjà en partie disponibles. Ajout de visualisations, enrichissement mineur du pipeline.

---

## 8. 🎙️ Podcast Audio Automatisé — « L'Observatoire à écouter »

**Valeur pour le citoyen** :
Tout le monde n'a pas le temps (ou l'envie) de lire. Un épisode audio de 3 à 5 minutes après chaque conseil municipal, généré automatiquement par IA, résume les décisions clés, les votes notables, et les impacts budgétaires. Accessible en mobilité, dans les transports ou en faisant du sport. Idéal pour élargir l'audience au-delà des lecteurs.

**Faisabilité technique** :
- **Pipeline (Publisher)** : Après génération du `data.json`, utiliser une API Text-to-Speech (Amazon Polly — déjà dans l'écosystème AWS, voix française « Léa » ou « Mathieu » en neural) pour générer un fichier MP3 à partir du résumé global du conseil (`analysis.vote_summary` + synthèse des décisions clés).
- **S3** : Stocker `podcast/YYYY-MM-DD-conseil.mp3` dans le bucket public.
- **data.json** : Ajouter `audio_summary_url` (string, URL S3/CloudFront).
- **Frontend** : Lecteur audio HTML5 natif intégré dans le header de chaque conseil. RSS feed pour les applications de podcast (iTunes, Spotify).
- **Alternative économique** : Générer le script côté Validator (texte) et faire le TTS via l'API Web Speech du navigateur côté client (gratuit, mais qualité moindre et pas de podcast).

**Effort estimé** : **Moyen** — Amazon Polly est simple à intégrer et peu coûteux (~4 $/million de caractères). Le RSS feed pour podcasts ajoute un peu de complexité. Alternative client-side : **faible**.

---

## 9. 🧵 Contexte Historique — « Que s'est-il passé depuis ? »

**Valeur pour le citoyen** :
Un visiteur arrive sur une délibération vieille de 8 mois. Il se demande : « Et ensuite, ça a donné quoi ? ». Cette fonctionnalité affiche une timeline des délibérations liées (ex : Délibération X a engagé un budget → Délibération Y 6 mois plus tard a ajusté ce budget → Délibération Z a inauguré le projet). Le citoyen comprend le cycle de vie complet d'une décision, pas juste un instantané.

**Faisabilité technique** :
- **Pipeline (Worker)** : Ajouter l'extraction de `related_deliberations` (array de références, ex : numéros de délibération citées dans le PDF).
- **Pipeline (Aggregator)** : Post-traitement pour construire un graphe de liens entre délibérations (même thème + mentions croisées). Stocker `linked_deliberations` dans chaque délibération.
- **data.json** : Ajouter `linked_deliberations: [{id, title, date, relationship}]`.
- **Frontend** : Sous chaque délibération, une section « 🧵 Contexte » avec mini-timeline des délibérations liées, triées chronologiquement.
- **Validator** : Règle D14 — lien vers une délibération introuvable → WARN.

**Effort estimé** : **Moyen** — Extraction déjà partiellement faisable par le LLM. La construction du graphe côté Aggregator est le défi principal.

---

## 10. 🏆 Score de Transparence — « Le Baromètre de la Démocratie Locale »

**Valeur pour le citoyen** :
Attribuer un score objectif et automatique au conseil municipal : combien de votes étaient unanimes vs disputés ? Combien de délibérations ont un impact « Néant » (administratif) vs substantiel ? Combien de désaccords ont été documentés ? Le citoyen voit d'un coup d'œil si le conseil a été « consensuel », « technique » ou « conflictuel », et suit l'évolution dans le temps. C'est un indicateur de santé démocratique locale, neutre et calculé.

**Faisabilité technique** :
- **Aggregator** : Déjà présent (statistiques globales du conseil). Ajouter le calcul d'un score composite :
  - **Indice de consensus** (0-100) : basé sur la présence de désaccords, votes contre, abstentions.
  - **Indice de substance** (0-100) : ratio de délibérations `is_substantial = true`.
  - **Indice de clarté** (0-100) : basé sur la présence de `eli5_summary`, d'impacts documentés, d'acronymes expliqués.
  - **Score global** = moyenne pondérée.
- **data.json** : Ajouter `transparency_score` au niveau conseil (dans `analysis`).
- **Frontend** : Badge visuel type « jauge circulaire » en haut de chaque conseil. Page « Baromètre » avec graphique d'évolution sur tous les conseils.
- **Validator** : Règle S7 — variation brutale du score (>30 pts entre deux conseils consécutifs) → WARN (alerte sur potentiel changement de pratique de publication).

**Effort estimé** : **Faible** — Calculs déterministes (pas de LLM), UI simple. L'Aggregator fait déjà 80% du travail de stats.

---

## 📋 Synthèse des Efforts

| # | Fonctionnalité | Effort | Impact Citoyen |
|---|---|---|---|
| 1 | Cartographie des délibérations | Moyen | ⭐⭐⭐⭐⭐ |
| 2 | Alertes thématiques personnalisables | Moyen | ⭐⭐⭐⭐ |
| 3 | Suivi des promesses | Élevé | ⭐⭐⭐⭐⭐ |
| 4 | Explication « Comme si j'avais 12 ans » | **Faible** | ⭐⭐⭐⭐ |
| 5 | Comparateur de votes par élu | Élevé | ⭐⭐⭐⭐ |
| 6 | Recherche sémantique | Moyen | ⭐⭐⭐ |
| 7 | Tableau de bord climat | **Faible** | ⭐⭐⭐ |
| 8 | Podcast audio automatisé | Moyen | ⭐⭐⭐ |
| 9 | Contexte historique (liens) | Moyen | ⭐⭐⭐ |
| 10 | Score de transparence | **Faible** | ⭐⭐⭐⭐ |

**Recommandation de priorité** : Commencer par les fonctionnalités à **effort faible** (4, 7, 10) qui apportent une valeur immédiate, puis attaquer les efforts **moyens** (1, 2, 6, 8, 9), et enfin les efforts **élevés** (3, 5) qui sont transformationnels mais nécessitent une base de données historique plus riche.
