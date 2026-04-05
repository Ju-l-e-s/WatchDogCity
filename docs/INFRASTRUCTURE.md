# Stratégie d'Infrastructure & Sécurité (Cloudflare + AWS)

Ce projet utilise une architecture hybride pour maximiser la gratuité et la sécurité.

## 🛡️ Le Bouclier : Cloudflare (Gratuit)
Cloudflare agit comme point d'entrée unique. Il filtre le trafic avant qu'il n'atteigne AWS, ce qui réduit les coûts Lambda/API Gateway.

### Configuration à l'achat du domaine :
1. **DNS** : Faire pointer les serveurs de noms (NS) de votre registraire vers Cloudflare.
2. **Proxy (Nuage Orange)** : Activer le proxy sur l'enregistrement A ou CNAME de votre site.
3. **Sécurité > WAF** :
   - Activer le **Bot Fight Mode** (Gratuit) pour bloquer les robots connus.
   - Créer une règle de pare-feu pour autoriser uniquement le trafic venant de pays spécifiques si nécessaire.
4. **Turnstile (Anti-Spam)** :
   - Créer un "Site" dans le tableau de bord Turnstile.
   - Récupérer la **Site Key** et la **Secret Key**.

## ⚡ Le Moteur : AWS (Serverless)
AWS héberge les données et la logique de traitement.

### Optimisation des coûts :
- **API Gateway & Lambda** : Protégés par Cloudflare, ils ne s'exécutent que pour les humains réels.
- **S3/CloudFront** (Optionnel) : Cloudflare peut mettre en cache les fichiers statiques (images, data.json) gratuitement, évitant les frais de transfert AWS.

## 📧 Formulaire de Contact & Newsletter
Le formulaire de contact utilise **Cloudflare Turnstile** pour valider l'humanité de l'expéditeur sans friction (souvent sans CAPTCHA visible).

### Variables à mettre à jour :
- `frontend/index.html` : Remplacer la clé de test `1x00000000000000000000AA` par la vraie **Site Key**.
- Backend (Lambda) : Vérifier le jeton Turnstile avec la **Secret Key** côté serveur.
