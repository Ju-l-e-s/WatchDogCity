# 🔭 Observatoire Citoyen de Bègles - Guide Architectural Complet

Ce document détaille les choix techniques, les stratégies d'infrastructure et les principes opérationnels de l'Observatoire Citoyen de Bègles. Ce projet est conçu selon les meilleures pratiques du **AWS Well-Architected Framework**.

---

## 1. Vue d'Ensemble & Objectifs
L'Observatoire est une plateforme d'analyse automatisée des délibérations municipales.
- **But** : Transparence démocratique via l'IA.
- **Contrainte** : Coût d'exploitation proche de 0€ (Modèle "Free Tier" First).
- **Techno** : Architecture 100% Serverless sur AWS + IA Gemini (Google).

---

## 2. Choix de l'Architecture (Infrastructure as Code)

### Pourquoi AWS CDK ?
Plutôt que Terraform ou la console, nous utilisons **AWS CDK (Python)**.
- **Avantage** : On définit l'infrastructure avec du "vrai" code (boucles, conditions, types).
- **Maintenance** : Un seul `watchdog_stack.py` contient tout le plan de bataille (Single Source of Truth).

### Les Services Clés (Le "Pourquoi")
1.  **S3 (Static Website Hosting)** : Hébergement du frontend (HTML/JS/Images) pour un coût dérisoire et une disponibilité de 99,99%.
2.  **CloudFront (CDN)** : 
    - **Performance** : Cache le site au plus proche des Béglais (Edge Locations).
    - **Sécurité** : HTTPS obligatoire via certificat SSL gratuit (ACM).
    - **FinOps** : Réduit les coûts de sortie de données (Egress) de S3.
3.  **DynamoDB (NoSQL)** :
    - Choisi pour sa scalabilité et son mode **Pay-per-request**.
    - Stocke les métadonnées des conseils et des délibérations.
    - Utilise des **GSI (Global Secondary Indexes)** pour des recherches rapides (ex: retrouver toutes les délibérations d'un conseil spécifique).
4.  **Lambda (Compute)** :
    - Zéro serveur à gérer. On ne paie qu'à la milliseconde d'exécution.
    - Langages utilisés : **Go** (pour la performance/typage) et **Python** (pour le CDK).

---

## 3. Workflow de Données (Event-Driven Design)

Le projet suit un pattern asynchrone pour éviter les timeouts et garantir la robustesse :

1.  **Trigger (EventBridge)** : Un "Cron" Cloud tous les jours à 18h déclenche la Lambda `Orchestrator`.
2.  **Scraping (Orchestrator)** : Elle vérifie si un nouveau compte-rendu PDF est disponible sur le site de la mairie.
3.  **Messaging (SQS)** : Si un PDF est trouvé, elle envoie un message dans une file d'attente SQS.
    - *Pourquoi SQS ?* Pour isoler les erreurs. Si l'analyse d'un PDF échoue, il retourne dans la file (Retry) sans bloquer le reste.
4.  **Intelligence (Worker)** : La Lambda `Worker` consomme le message, télécharge le PDF, l'envoie à l'API **Gemini 1.5 Flash** pour résumé/analyse, et stocke le résultat en base.
5.  **Diffusion (Publisher)** : Une fois les données en base, le `Publisher` regénère le fichier `data.json` sur S3 et invalide le cache CloudFront.

---

## 4. Stratégie DevOps (CI/CD)

**GitHub Actions** automatise tout le cycle de vie :
- **Build** : Compilation des binaires Go (multi-architecture ARM64 pour réduire les coûts Lambda de 20% par rapport à x86).
- **Test** : Exécution des tests unitaires avant chaque déploiement.
- **Deploy** : Utilisation du secret `AWS_ACCESS_KEY_ID` pour pousser l'infrastructure via `cdk deploy`.

---

## 5. Vision FinOps (Optimisation des Coûts)

Le projet est conçu pour rester dans le **AWS Free Tier** (offre gratuite) même avec des milliers de visiteurs :
- **Lambda ARM64 (Graviton2)** : Plus performant et moins cher.
- **DynamoDB On-Demand** : On ne paie pas pour une capacité "réservée" inutile.
- **CloudFront Cache** : 99% des requêtes ne touchent jamais S3, ce qui minimise les coûts de transfert.
- **IA (Gemini)** : Utilisation du modèle **Gemini 1.5 Pro** (Version "Précision Max"). Contrairement au modèle Flash, le modèle Pro possède une capacité de raisonnement accrue et une fenêtre contextuelle immense, permettant d'analyser des rapports budgétaires de plusieurs centaines de pages sans perte de détail.

---

## 6. Sécurité

1.  **Principle of Least Privilege** : Chaque Lambda possède un rôle IAM avec uniquement les permissions nécessaires (ex: `Worker` ne peut pas supprimer de fichiers S3, il peut juste lire/écrire).
2.  **Secrets Manager** : La clé API Gemini n'est jamais en clair dans le code, elle est récupérée dynamiquement depuis AWS Secrets Manager.
3.  **Cloudflare (WAF/Turnstile)** : Protection contre le spam des formulaires et les attaques par déni de service (DDoS).

## 8. Engagement & Newsletter (Double Opt-in)

Pour fidéliser les citoyens, un système de newsletter automatisé est intégré :
- **Gestion des Abonnés** : Une table DynamoDB dédiée gère les états d'abonnement (`pending`, `confirmed`).
- **Flux d'Inscription** :
    1. **Saisie** : Le citoyen entre son email sur le frontend.
    2. **Validation (Subscriber Lambda)** : Création d'un token UUID unique et envoi d'un email de confirmation via **AWS SES**.
    3. **Confirmation** : Un clic sur le lien (`/confirm?token=...`) bascule le statut en `confirmed`.
- **Désinscription Chirurgicale** : Chaque email contient un lien unique permettant une suppression immédiate des données (RGPD-compliant).
- **Notifications Automatisées** : Le `Publisher` est configuré pour déclencher des envois ciblés dès qu'une nouvelle analyse majeure est publiée, garantissant une information fraîche et pertinente.

---

## 9. Vision Future & Évolutivité
L'architecture modulaire permet d'envisager facilement :
- **Multi-Communes** : Adapter le scraper pour supporter d'autres villes.
- **Alertes Thématiques** : Permettre aux abonnés de ne recevoir que les délibérations sur l'Environnement ou l'Urbanisme.
- **Analyse de Sentiment** : Utiliser l'IA pour détecter les sujets de tension lors des débats municipaux.
