# Design Spec: Tableau de Bord du Budget (Observatoire Bègles)

## 1. Objectif
Ajouter une nouvelle page/vue "Budget" à l'Observatoire Citoyen de Bègles. Cette page permettra aux citoyens de visualiser clairement les dépenses publiques votées lors des conseils municipaux, sous forme d'un tableau de bord global et d'un flux détaillé classé par thématiques.

## 2. Architecture et Contraintes
- **Pure Frontend** : Aucune modification du backend (Go/Lambda) ni du prompt de l'IA (Gemini) n'est requise.
- **Source de données** : Le frontend exploitera le fichier `data.json` existant.
- **Extraction** : Le code JavaScript côté client identifiera les délibérations contenant une dépense en recherchant le badge HTML spécifique généré par le système actuel (ex: `<span class="inline-flex items-center px-2 py-0.5 rounded text-[10px] font-bold bg-amber-50 text-amber-700 border border-amber-100 ml-2 shadow-sm">💰 45 450 €</span>`).
- **Performance** : Les calculs (sommes, regroupements par thématique) seront effectués dynamiquement par le navigateur.

## 3. Composants de l'Interface (Frontend)

### 3.1. Navigation
- Ajout d'un lien "Budget" dans le menu de navigation principal permettant de basculer entre la vue "Flux d'actualité" (actuelle) et la vue "Budget".

### 3.2. Le Dashboard (En-tête)
- **Métriques clés** : Affichage du montant total voté cumulé et du nombre total de délibérations financières.
- **Visualisation** : Un graphique simple (ex: répartition par grandes thématiques comme Urbanisme, Éducation, Culture, etc.).

### 3.3. Le Flux des Dépenses (Classé par Thématique)
- Les délibérations ayant un impact financier seront regroupées et affichées par thématique (ex: Éducation, Culture, Urbanisme, etc.).
- À l'intérieur de chaque thématique, les délibérations seront triées par date (de la plus récente à la plus ancienne).
- **Pour chaque élément** :
  - La date du conseil municipal.
  - Le titre de la délibération.
  - Le montant (mis en évidence).
  - **Accès complet** : La possibilité de lire ou de dérouler le texte complet de la délibération (résumé généré par l'IA et votes) directement depuis cette vue, sans perdre le contexte.

## 4. Extraction et Traitement des Données (Logique JS)
- Au chargement des données (`data.json`), une fonction utilitaire parcourra toutes les délibérations de tous les conseils.
- **Extraction du montant** : Elle utilisera une expression régulière (Regex) pour détecter la présence du badge `💰 [montant] €` dans le contenu de la délibération. Le montant sera nettoyé (suppression des espaces insécables, du symbole €, etc.) et converti en nombre.
- **Classification par thématique** : Le script déduira la thématique de la délibération (soit via une métadonnée existante dans `data.json`, soit par mots-clés dans le titre/contenu si nécessaire) pour permettre le regroupement.

## 5. Gestion des Erreurs et Cas Limites
- **Absence de données** : Si aucune délibération financière n'est trouvée, un message explicatif clair s'affichera au lieu d'un tableau de bord vide ou cassé.
- **Thématique inconnue** : Les délibérations dont la thématique ne peut être déterminée seront classées dans une catégorie "Divers" ou "Administration Générale".
- **Formatage des nombres** : Les montants extraits devront être formatés correctement pour l'affichage (ex: "1.2M €" pour les grands nombres, formatage local français).
- **Responsive Design** : Le tableau de bord et les graphiques devront s'adapter parfaitement aux écrans mobiles.
