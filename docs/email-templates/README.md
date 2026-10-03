# Modèles Brevo

`brevo_template_active_7.html` est la copie versionnée du modèle de newsletter Brevo **#7**. `brevo_template_v2.html` et `brevo_template_v3.html` sont des versions historiques. Le CDK ne charge pas ces fichiers : il référence les modèles Brevo par identifiant. La configuration de Bègles pointe vers le modèle #7 (`cdk/cities/begles.json`), mais la variable d'environnement `BREVO_NEWSLETTER_TEMPLATE_ID` peut remplacer cet identifiant au déploiement.

Après toute correction d'un fichier HTML, vérifier le modèle réellement référencé par la Lambda Notifier, puis reporter la correction dans ce modèle sur Brevo. Brevo copie le HTML du modèle dans chaque campagne au moment de sa création : modifier le modèle ne met pas à jour les brouillons existants. Vérifier et corriger le HTML du brouillon avant son envoi. Prévisualiser le modèle avec des entrées dont `item.impact` est vide et avec des votes comportant seulement des abstentions avant de préparer une nouvelle campagne.
