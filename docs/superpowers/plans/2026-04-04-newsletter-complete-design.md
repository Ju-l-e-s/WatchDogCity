# Newsletter Complète — Plan d'implémentation

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mettre en place un système d'abonnement complet incluant la confirmation, un format d'email citoyen moderne et un lien de désabonnement obligatoire.

**Architecture:** Extension de la Lambda `subscriber` pour gérer le désabonnement par token, et mise à jour de la Lambda `publisher` pour formater les emails avec les nouvelles données (thématiques, votes) et le lien de retrait.

**Tech Stack:** Go 1.22, AWS SES, DynamoDB.

---

### Task 1: Gestion du Désabonnement (Lambda Subscriber)

**Files:**
- Modify: `lambdas/subscriber/handler.go`
- Test: `lambdas/subscriber/handler_test.go`

- [ ] **Step 1: Router la méthode GET /unsubscribe**

Modifier le handler principal pour détecter le chemin d'accès.

```go
// Dans lambdas/subscriber/handler.go, fonction handler
if req.HTTPMethod == http.MethodGet {
    if strings.Contains(req.Path, "unsubscribe") {
        return handleUnsubscribe(ctx, req)
    }
    return handleConfirm(ctx, req)
}
```

- [ ] **Step 2: Implémenter handleUnsubscribe**

Rechercher l'utilisateur par son token et le supprimer de la table `subscribers`.

```go
func handleUnsubscribe(ctx context.Context, req events.APIGatewayProxyRequest) (events.APIGatewayProxyResponse, error) {
    token := req.QueryStringParameters["token"]
    if token == "" {
        return apiResponse(400, map[string]string{"error": "missing token"}), nil
    }

    cfg, _ := config.LoadDefaultConfig(ctx)
    ddb := dynamodb.NewFromConfig(cfg)

    // Trouver l'email par token via le GSI
    out, err := ddb.Query(ctx, &dynamodb.QueryInput{
        TableName:              aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
        IndexName:              aws.String("token-index"),
        KeyConditionExpression: aws.String("token = :t"),
        ExpressionAttributeValues: map[string]types.AttributeValue{
            ":t": &types.AttributeValueMemberS{Value: token},
        },
    })
    if err != nil || len(out.Items) == 0 {
        return apiResponse(404, map[string]string{"error": "invalid token"}), nil
    }

    emailAttr := out.Items[0]["email"].(*types.AttributeValueMemberS)
    
    // Supprimer l'abonné
    _, err = ddb.DeleteItem(ctx, &dynamodb.DeleteItemInput{
        TableName: aws.String(os.Getenv("SUBSCRIBERS_TABLE")),
        Key:       map[string]types.AttributeValue{"email": &types.AttributeValueMemberS{Value: emailAttr.Value}},
    })

    return events.APIGatewayProxyResponse{
        StatusCode: 302,
        Headers:    map[string]string{"Location": os.Getenv("SITE_URL") + "?unsubscribed=true"},
    }, nil
}
```

- [ ] **Step 3: Vérifier les tests**

- [ ] **Step 4: Commit**

```bash
git add lambdas/subscriber/handler.go
git commit -m "feat(subscriber): add unsubscription handler via token"
```

---

### Task 2: Formatage Citoyen de l'Email (Lambda Publisher)

**Files:**
- Modify: `lambdas/publisher/handler.go`

- [ ] **Step 1: Récupérer le token de chaque abonné lors de l'envoi**

Modifier `sendNewsletter` pour inclure le lien de désabonnement personnalisé.

```go
// Dans lambdas/publisher/handler.go, fonction sendNewsletter
for _, item := range subScan.Items {
    emailAttr := item["email"].(*types.AttributeValueMemberS)
    tokenAttr := item["token"].(*types.AttributeValueMemberS) // Récupération du token
    
    unsubscribeURL := fmt.Sprintf("%s/unsubscribe?token=%s", os.Getenv("API_BASE_URL"), tokenAttr.Value)
    body := buildEmailBody(council, delibs, unsubscribeURL)
    // ... envoi SES
}
```

- [ ] **Step 2: Refonte de buildEmailBody**

Utiliser un style HTML moderne avec thématiques et votes clairs.

```go
func buildEmailBody(council *CouncilRecord, delibs []DeliberationRecord, unsubscribeURL string) string {
    var sb strings.Builder
    sb.WriteString(`
        <div style="font-family: sans-serif; max-width: 600px; margin: auto; color: #334155;">
            <h1 style="color: #1e3a8a; border-bottom: 2px solid #3b82f6; padding-bottom: 10px;">Observatoire Citoyen — Bègles</h1>
            <p>Bonjour, voici le résumé du <strong>` + council.Title + `</strong>.</p>
    `)

    for _, d := range delibs {
        sb.WriteString(`
            <div style="margin-bottom: 30px; padding: 20px; background: #f8fafc; border-radius: 12px; border: 1px solid #e2e8f0;">
                <span style="background: #dbeafe; color: #1d4ed8; padding: 4px 10px; border-radius: 20px; font-size: 11px; font-weight: bold; text-transform: uppercase;">` + (d.TopicTag) + `</span>
                <h2 style="font-size: 18px; margin: 10px 0;">` + d.Title + `</h2>
                <p style="font-size: 14px; line-height: 1.6;">` + d.Summary + `</p>
                <div style="margin: 15px 0;">
                    <strong style="color: #10b981;">` + fmt.Sprint(d.VotePour) + ` Pour</strong> | 
                    <strong style="color: #f43f5e;">` + fmt.Sprint(d.VoteContre) + ` Contre</strong> | 
                    <strong style="color: #94a3b8;">` + fmt.Sprint(d.VoteAbstention) + ` Abst.</strong>
                </div>
                <p style="font-size: 13px; font-style: italic; border-left: 3px solid #64748b; padding-left: 10px;">Position de l'opposition : ` + d.Disagreements + `</p>
                <a href="` + d.PDFURL + `" style="color: #3b82f6; font-size: 12px; font-weight: bold; text-decoration: none;">VOIR LE PDF SOURCE</a>
            </div>
        `)
    }

    sb.WriteString(`
            <hr style="border: 0; border-top: 1px solid #e2e8f0; margin: 40px 0;">
            <p style="font-size: 11px; color: #94a3b8; text-align: center;">
                Cet email a été envoyé par l'Observatoire Citoyen de Bègles.<br>
                <a href="` + unsubscribeURL + `" style="color: #64748b; text-decoration: underline;">Se désabonner de cette liste</a>
            </p>
        </div>
    `)
    return sb.String()
}
```

- [ ] **Step 3: Vérifier la compilation**

- [ ] **Step 4: Commit**

```bash
git add lambdas/publisher/handler.go
git commit -m "feat(publisher): modern email format with thematic tags and unsubscribe link"
```

---

### Task 3: Déploiement et Vérification

- [ ] **Step 1: Build et Déployer**

Run: `make deploy`

- [ ] **Step 2: Vérifier le lien de désabonnement**

S'abonner, attendre un email (ou forcer l'envoi), et tester le lien.

- [ ] **Step 3: Commit final**

```bash
git commit --allow-empty -m "chore: newsletter system complete"
```
