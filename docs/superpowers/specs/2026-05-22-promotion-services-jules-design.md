# Spécification : Intégration des services professionnels (Jules Laconfourque)

Cette spécification détaille l'intégration d'une section promotionnelle pour les services de développement de Jules Laconfourque sur le site de l'Observatoire Citoyen de Bègles.

## 1. Objectifs
- Valoriser le savoir-faire technique de l'auteur.
- Générer des prises de contact professionnelles (freelance/conseil).
- Maintenir l'image de neutralité et de bénévolat du projet citoyen.

## 2. Emplacements et Modifications

### 2.1 Footer (Signature discrète)
- **Emplacement** : Remplacement du paragraphe copyright actuel.
- **Modification** : 
    - Texte : `© 2026 Observatoire Bègles — Conçu par Jules Laconfourque. Contactez-moi pour vos projets.`
    - Le nom "Jules Laconfourque" sera un bouton/lien déclenchant l'ouverture de la modale de contact (`toggleContactModal(true)`).
    - Style : Souligné discret (`underline decoration-slate-300`), changement de couleur au survol pour indiquer l'interactivité.

### 2.2 Modale "Le Projet" (Encart détaillé)
- **Emplacement** : À la fin du contenu de la modale `about-modal`, avant le bouton "J'AI COMPRIS".
- **Contenu** : 
    - Titre : `Un projet informatique en tête ?` (Style `text-slate-900 font-bold text-lg mb-4`).
    - Corps de texte : Reprise du texte fourni par l'utilisateur.
    - Liste des services : Mise en forme avec des puces pour la lisibilité.
    - Bouton d'appel à l'action (CTA) : `DISCUTER DE MON PROJET`.
- **Interaction** : 
    - Le bouton CTA fermera la modale "À propos" et ouvrira la modale de contact après un léger délai pour la fluidité de l'animation.
    - Code suggéré : `onclick="toggleAboutModal(false); setTimeout(() => toggleContactModal(true), 300)"`.

## 3. Texte Définitif
> **Un projet informatique en tête ?**
> Au-delà de cette démarche citoyenne, je mets mes compétences en développement informatique au service de vos projets. Que vous soyez un particulier, une association ou une petite entreprise, je suis disponible pour vous accompagner.
> 
> **Ce que je peux faire pour vous :**
> - **Simplifier et automatiser :** Vous avez des tâches répétitives, des données à organiser ou des processus complexes ? Je peux créer des outils numériques sur-mesure pour vous faire gagner du temps.
> - **Donner vie à vos idées :** Vous avez une idée d'application, de site web ou de service numérique mais vous ne savez pas par où commencer ? Je vous accompagne, de la conception jusqu'à la réalisation.
> - **Conseil et accompagnement :** Besoin d'un avis technique simple pour démarrer un projet ou pour sécuriser vos outils numériques au quotidien ? Parlons-en.
> 
> Mon approche est simple : je ne cherche pas à vous vendre de la technologie pour la technologie, mais à construire des solutions robustes, utiles et surtout, faciles à utiliser.

## 4. Design & Accessibilité
- Les couleurs utiliseront les variables existantes (`brand-700`, `slate-900`).
- Les boutons respecteront les tailles de cibles tactiles minimales (44px).
- L'ordre de tabulation (tabindex) sera préservé pour les nouveaux éléments interactifs.

## 5. Validation
- Vérifier que les deux modales ne se superposent pas (fermeture de l'une avant ouverture de l'autre).
- Vérifier le rendu mobile (footer responsive).
