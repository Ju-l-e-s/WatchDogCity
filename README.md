# 🔭 WatchDog City - Bègles

**L'intelligence artificielle au service de la transparence citoyenne.**

WatchDog City est une plateforme indépendante qui décode les rapports municipaux complexes de la ville de Bègles pour les transformer en résumés clairs, neutres et accessibles à tous.

## 🚀 Mission
La politique locale produit des dizaines de pages de rapports techniques (PDF) souvent difficiles à suivre pour les citoyens. WatchDog City utilise **Gemini 1.5 Pro** pour extraire l'essentiel :
- **Décisions concrètes** : Ce qui a été réellement acté.
- **Impacts locaux** : Ce que cela change pour les habitants.
- **Résultats de votes** : La position de la majorité et de l'opposition.
- **Analyse structurée** : Contexte, enjeux et points de controverse.

## 🛠️ Architecture Technique
Le projet repose sur une infrastructure **100% Serverless** sur AWS, gérée via **AWS CDK** :

- **Backend (Go)** : 4 fonctions Lambda spécialisées (Orchestrateur, Worker IA, Publisher, Subscriber).
- **IA (Google Gemini)** : Analyse sémantique avancée des documents officiels.
- **Stockage** : DynamoDB pour les données structurées, S3 pour le site statique et les PDF.
- **Sécurité** : Protection Cloudflare Turnstile et WAF pour minimiser les coûts et bloquer les spams.
- **Monitoring** : Dashboard CloudWatch personnalisé pour le suivi des erreurs et des coûts en temps réel.

## 📈 Suivi des Coûts
Le projet intègre un suivi de consommation des jetons (tokens) Gemini directement dans le dashboard AWS, permettant une maîtrise totale du budget de fonctionnement.

## 🔧 Installation & Maintenance
Le projet utilise un `Makefile` pour simplifier les opérations courantes :

```bash
make build    # Compiler les fonctions Go
make deploy   # Déployer l'infrastructure complète sur AWS
make logs-worker # Surveiller l'activité de l'IA en direct
```

## 👷 Auteur
**Béglais de naissance**, développeur et Cloud Architect passionné par l'émancipation citoyenne par la technologie.

---
*Ce projet est une initiative citoyenne bénévole, non-affiliée à la mairie de Bègles.*
