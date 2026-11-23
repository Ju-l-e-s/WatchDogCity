# Migration Gemini et diffusion Brevo

## Gemini

[Google annonce](https://aistudio.google.com/docs/deprecations) la redirection des modèles 2.5 le 1er décembre 2026. Tester les nouveaux identifiants directement. Un identifiant 2.5 ne constitue plus une restauration du comportement après cette date.

| Étape | Variable de synthèse CDK | Valeur proposée | Raisonnement | Plafond de sortie |
|---|---|---|---|---:|
| Extraction PDF | `GEMINI_PDF_EXTRACT_MODEL` | `gemini-3.5-flash-lite` | minimal | 8 192 |
| Vérification PDF | `GEMINI_PDF_VERIFY_MODEL` | `gemini-3.8-flash` | medium | 4 096 |
| Rédaction newsletter | `GEMINI_NEWSLETTER_WRITE_MODEL` | `gemini-3.8-flash` | low | 32 768 |
| Vérification newsletter | `GEMINI_NEWSLETTER_VERIFY_MODEL` | `gemini-3.8-flash` | medium | 16 384 |

Chaque fonction lit `GEMINI_MODEL` et `GEMINI_FACT_CHECK_MODEL` dans son environnement. Validator et Notifier utilisent le même couple newsletter. Le SDK Go et `GenerateContent` v1beta sont conservés. Les quatre schémas fonctionnels sont inchangés. L’essai simulé vérifie la sérialisation Go. L’acceptation des requêtes et schémas par Google reste à vérifier dans un projet d’essai.

### Références et lacunes

- `D10-2026_128` couvre trois lignes AP/CP et leur différence avec une dépense. Référence de départ dans [le test QC](../lambdas/shared/qc_test.go#L82).
- `D23-2026_113` couvre le plafond de 23 792 €, les justificatifs, la convention et la chronologie 2024–2025. Les [corrections de septembre](../scripts/corrections/2026-09-28.json#L294) indexent les pages 2, 5 et 6. Vérifier les votes et l’intégralité du dispositif sur les octets figés avant de retenir cette annotation.
- `D06-2026_124`, `D07-2026_125` et `D09-2026_127` sont référencés dans [les essais Worker](../lambdas/worker/factcheck_test.go#L61). Il manque des annotations complètes de leurs faits essentiels.
- La [fixture newsletter de migration](../lambdas/shared/newsletter_migration_test.go) combine des cas de stade et de financement déjà documentés, des votes injectés et un calendrier synthétique. Elle vérifie aussi que l’omission d’un fait essentiel ou d’un décompte est détectable. L’[instantané de juin](../scripts/localtest/fixtures/approved_council_2026-06-22.json) convient aux aperçus, il ne fournit pas les identifiants par sujet du contrat courant.
- Le [manifeste PDF](../lambdas/worker/corpus/manifest.json) contient encore un gabarit. Aucun PDF de référence n’est versionné. Ajouter ensuite un budget dense, un PDF scanné et un conseil entier avec relecture humaine indépendante. Les assertions textuelles du lot initial mesurent quelques faits attendus. Elles ne remplacent pas la vérification phrase par phrase et peuvent rejeter une paraphrase correcte.

### Progression et décision de mise en service

1. Exécuter `go test ./...` dans `lambdas/shared`, `lambdas/worker`, `lambdas/validator`, `lambdas/notifier`, `lambdas/brevo-webhook` et `scripts/localtest` avec les variables d’essai réel absentes. Les tests SDK et Brevo utilisent des réponses simulées.
2. Demander l’autorisation d’un lot Gemini **avec un plafond en USD, la liste des cas et le nombre de répétitions**. Première proposition pour dimensionner ce plafond, un passage sur chacun des deux PDF financiers et sur la lettre synthétique. Les wrappers existants bornent ce premier lot à 40 appels SDK au maximum, dont 8 par PDF et 24 pour la lettre. Pour chiffrer le plafond, relever les tailles, pages et budgets de tokens, inclure les tentatives, appliquer le tarif payant du projet et le niveau de résolution. Aucune exécution si le plafond n’est pas convenu. Isoler la clé sur un projet d’essai avec une limite de requêtes effective adaptée au lot. Une alerte de facturation seule n’arrête pas les appels. Ajouter ensuite des répétitions et des cas sur une autorisation distincte si nécessaire.
3. Après autorisation, fournir les PDF locaux et leur SHA-256. Les commandes suivantes font de vrais appels payants avec le pipeline courant et écrivent seulement les résultats de test dans la sortie. La clé Gemini du projet d’essai doit déjà être fournie par l’environnement de l’opérateur. Utiliser un second passage sur `D23-2026_113` en changeant le cas, le fichier et son empreinte.

   ```bash
   cd lambdas/worker
   WATCHDOG_GEMINI_EVAL=1 WATCHDOG_EVAL_PDF_CASE=D10-2026_128 \
     WATCHDOG_EVAL_PDF=/chemin/D10-2026_128.pdf WATCHDOG_EVAL_PDF_SHA256='<SHA256>' \
     GEMINI_MODEL=gemini-3.5-flash-lite GEMINI_FACT_CHECK_MODEL=gemini-3.8-flash \
     go test -count=1 -v -run '^TestMigrationLiveFinancialPDF$' .
   ```

   ```bash
   cd lambdas/shared
   WATCHDOG_GEMINI_EVAL=1 GEMINI_MODEL=gemini-3.8-flash GEMINI_FACT_CHECK_MODEL=gemini-3.8-flash \
     go test -count=1 -v -run '^TestMigrationLiveNewsletter$' .
   ```

4. Comparer la fidélité des montants, votes, dates, stade et conditions, puis la couverture des faits annotés. Pour chaque vérificateur, lui présenter aussi des affirmations correctes et des erreurs connues sur les mêmes données. Relever les erreurs critiques acceptées, les rejets de faits corrects, les sorties tronquées, la latence et le coût par résultat reçu et accepté. Les logs `gemini_attempt` fournissent `requested_model`, `model_version`, les motifs de fin, les tokens dont le raisonnement, le résultat technique et la durée. Les journaux de l’essai doivent conserver le commit, les références figées, les configurations et la date. Un petit lot sert à détecter des écarts, pas à établir une latence p95 fiable.
5. Demander une autorisation de déploiement distincte après la revue. Proposer comme minimum aucun écart critique de montant, vote, stade ou condition observé, aucune baisse de couverture des références et des temps compatibles avec les limites actuelles. Vérifier ensuite les versions effectives et les erreurs dans les journaux. Si une étape doit être reconfigurée, changer seulement sa variable. Les statuts `APPROVED` et la vérification factuelle restent exigés.

## Commande de diffusion Brevo

### Permissions et préparation

- Attacher la politique CDK dont l’ARN est fourni dans `NewsletterReleaseOperatorPolicyArn` à une identité d’administration assumée par un opérateur autorisé. Le Notifier a uniquement `GetItem` sur le registre `watchdog-newsletter-releases`. L’opérateur peut y inscrire une autorisation et prendre le verrou de préparation du conseil avec une mise à jour DynamoDB limitée aux attributs concernés. Le registre retient un enregistrement par campagne.
- Utiliser une clé Brevo d’administration injectée localement dans `WATCHDOG_BREVO_ADMIN_API_KEY`. Ne pas la fournir à la Lambda ou au workflow. Restreindre les opérateurs autorisés à envoyer directement dans l’interface Brevo. Brevo propose seulement `None`, `Read` et `All` pour les campagnes email. La clé qui permet au Notifier de créer des brouillons et des aperçus dispose donc de capacités plus larges chez Brevo. Les protections applicatives et IAM couvrent les chemins WatchDogCity, tandis qu’un usage direct d’une clé Brevo habilitée ou une édition dans l’interface à la toute dernière seconde relève des permissions Brevo. [Permissions Brevo](https://help.brevo.com/hc/en-us/articles/40158285046930-About-API-key-permissions)
- Vérifier le brouillon dans Brevo, sa liste #2 et son contenu avant de présenter la demande de confirmation. Ajuster `AWS_REGION`, `COUNCILS_TABLE` ou `NEWSLETTER_RELEASES_TABLE` uniquement pour une installation qui utilise d’autres valeurs.

### Exécuter après le consentement propre à la campagne

Depuis `lambdas/notifier`, inspecter le brouillon avec une identité opérateur. Cette commande consulte AWS et Brevo. Faire confirmer explicitement, dans un message distinct, le sujet, le conseil, l’ID de campagne, la liste et, le cas échéant, la date exacte de programmation.

```bash
go run . release --council-id '<ID_DU_CONSEIL>' --campaign-id <ID_BREVO>
```

Ajouter `--scheduled-at '<DATE_RFC3339>'` pour proposer une programmation. Une fois la confirmation explicite reçue, rejouer la commande avec le même conseil, le même brouillon et le même horaire, plus `--expires` et `--confirm` affichés à l’étape précédente. L’empreinte et la date d’expiration protègent la version du sujet, le HTML, les destinataires et l’action examinés. Le registre interdit de déclencher deux fois la même campagne, même avec une nouvelle empreinte. La commande de lecture sans ces paramètres ne diffuse aucune lettre. La date de programmation doit être exprimée à la seconde près.

Le résultat HTTP accepté correspond à une demande prise en charge. `reconcile_only` confirme l’envoi à partir de `sentDate` lorsque Brevo retourne `sent`. Si Brevo reste en cours ou si un appel retourne une erreur incertaine, examiner la même campagne par ID. Une tentative de création ambiguë conserve `newsletter_draft_request` sur le conseil et une tentative de diffusion ambiguë conserve son inscription dans le registre. Les reprises recherchent le brouillon existant. Si aucune campagne n’est retrouvée, l’opérateur doit établir l’issue dans Brevo avant toute remise à zéro contrôlée. Un identifiant, une autorisation ou un registre ne doivent pas être supprimés pour contourner cette garde.
