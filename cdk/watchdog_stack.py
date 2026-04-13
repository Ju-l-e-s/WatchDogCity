import os
from aws_cdk import (
    Stack, Duration, RemovalPolicy, CfnOutput,
    aws_dynamodb as dynamodb,
    aws_s3 as s3,
    aws_sqs as sqs,
    aws_lambda as lambda_,
    aws_lambda_event_sources as lambda_events,
    aws_events as events,
    aws_events_targets as targets,
    aws_apigateway as apigw,
    aws_iam as iam,
    aws_cloudfront as cloudfront,
    aws_cloudfront_origins as origins,
    aws_cloudwatch as cloudwatch,
    aws_s3_deployment as s3_deploy,
    aws_certificatemanager as acm,
)
from constructs import Construct


class WatchdogStack(Stack):
    def __init__(self, scope: Construct, id: str, **kwargs):
        super().__init__(scope, id, **kwargs)

        gemini_api_key = os.environ.get("GEMINI_API_KEY", "dummy-key-for-synth")

        # ── DynamoDB Tables ──────────────────────────────────────────────
        councils_table = dynamodb.Table(
            self, "CouncilsTable",
            table_name="watchdog-councils",
            partition_key=dynamodb.Attribute(name="council_id", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
        )

        deliberations_table = dynamodb.Table(
            self, "DeliberationsTable",
            table_name="watchdog-deliberations",
            partition_key=dynamodb.Attribute(name="id", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
            stream=dynamodb.StreamViewType.NEW_IMAGE,
        )
        deliberations_table.add_global_secondary_index(
            index_name="council_id-index",
            partition_key=dynamodb.Attribute(name="council_id", type=dynamodb.AttributeType.STRING),
        )

        subscribers_table = dynamodb.Table(
            self, "SubscribersTable",
            table_name="watchdog-subscribers",
            partition_key=dynamodb.Attribute(name="email", type=dynamodb.AttributeType.STRING),
            billing_mode=dynamodb.BillingMode.PAY_PER_REQUEST,
            removal_policy=RemovalPolicy.RETAIN,
        )

        # ── S3 Website Bucket (EXISTING) ──────────────────────────────────
        historical_bucket_name = "watchdogstack-websitebucket75c24d94-clsmaf2ocvxq"
        website_bucket = s3.Bucket.from_bucket_name(
            self, "WebsiteBucket",
            historical_bucket_name
        )

        # ── CloudFront Distribution ───────────────────────────────────────
        acm_certificate_arn = os.environ.get("ACM_CERTIFICATE_ARN")
        domain_name = os.environ.get("DOMAIN_NAME", "lobservatoiredebegles.fr")
        domain_names = [domain_name, f"www.{domain_name}"]

        cert = None
        if acm_certificate_arn:
            cert = acm.Certificate.from_certificate_arn(self, "WebsiteCert", acm_certificate_arn)

        distribution = cloudfront.Distribution(
            self, "WebsiteDistribution",
            default_behavior=cloudfront.BehaviorOptions(
                origin=origins.S3StaticWebsiteOrigin(website_bucket),
                viewer_protocol_policy=cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
                compress=True, # Active GZIP et Brotli automatiquement
            ),
            default_root_object="index.html",
            domain_names=domain_names if cert else None,
            certificate=cert,
        )

        # ── SQS Queue + DLQ ───────────────────────────────────────────────
        dlq = sqs.Queue(
            self, "PdfDLQ",
            queue_name="watchdog-pdf-dlq",
            retention_period=Duration.days(14),
        )

        pdf_queue = sqs.Queue(
            self, "PdfQueue",
            queue_name="watchdog-pdf-queue",
            visibility_timeout=Duration.minutes(6),
            dead_letter_queue=sqs.DeadLetterQueue(
                max_receive_count=3,
                queue=dlq,
            ),
        )

        # ── Gemini Models ────────────────────────────────────────────────
        worker_model = "gemini-2.5-pro"
        aggregator_model = "gemini-3.1-pro-preview"

        # ── Lambda common config ──────────────────────────────────────────
        common_env = {
            "COUNCILS_TABLE": councils_table.table_name,
            "DELIBERATIONS_TABLE": deliberations_table.table_name,
            "SUBSCRIBERS_TABLE": subscribers_table.table_name,
            "PDF_QUEUE_URL": pdf_queue.queue_url,
            "WEBSITE_BUCKET": website_bucket.bucket_name,
            "GEMINI_API_KEY": gemini_api_key,
        }

        # ── Lambda: Orchestrator ──────────────────────────────────────────
        orchestrator = lambda_.Function(
            self, "Orchestrator",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/orchestrator.zip"),
            timeout=Duration.minutes(3),
            environment=common_env,
        )
        councils_table.grant_read_write_data(orchestrator)
        pdf_queue.grant_send_messages(orchestrator)

        # ── Lambda: Worker ────────────────────────────────────────────────
        worker = lambda_.Function(
            self, "Worker",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/worker.zip"),
            timeout=Duration.minutes(5),
            environment={
                **common_env,
                "GEMINI_MODEL": worker_model,
            },
        )
        worker.add_event_source(lambda_events.SqsEventSource(
            pdf_queue,
            batch_size=1,
        ))
        councils_table.grant_read_write_data(worker)
        deliberations_table.grant_read_write_data(worker)

        # ── Lambda: Publisher ─────────────────────────────────────────────
        publisher = lambda_.Function(
            self, "Publisher",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/publisher.zip"),
            timeout=Duration.minutes(5),
            environment={
                **common_env,
                "FROM_EMAIL": "watchdog@begles.citoyen",
                "SITE_URL": f"https://{distribution.distribution_domain_name}",
                "CLOUDFRONT_DISTRIBUTION_ID": distribution.distribution_id,
            },
        )
        councils_table.grant_read_write_data(publisher)
        deliberations_table.grant_read_data(publisher)
        subscribers_table.grant_read_data(publisher)
        website_bucket.grant_put(publisher)
        publisher.add_to_role_policy(iam.PolicyStatement(
            actions=["ses:SendEmail", "cloudfront:CreateInvalidation"],
            resources=["*"],
        ))

        # ── Lambda: Aggregator ───────────────────────────────────────────
        aggregator = lambda_.Function(
            self, "Aggregator",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/aggregator.zip"),
            timeout=Duration.minutes(5),
            environment={
                **common_env,
                "GEMINI_MODEL": aggregator_model,
                "PUBLISHER_FUNCTION_NAME": publisher.function_name,
            },
        )
        aggregator.add_event_source(lambda_events.DynamoEventSource(
            deliberations_table,
            starting_position=lambda_.StartingPosition.LATEST,
            batch_size=1,
            retry_attempts=2,
        ))
        councils_table.grant_read_write_data(aggregator)
        deliberations_table.grant_read_data(aggregator)
        publisher.grant_invoke(aggregator)

        # Worker needs to invoke Publisher (keeping for backward compatibility or removing if not needed)
        publisher.grant_invoke(worker)
        worker.add_environment("PUBLISHER_FUNCTION_NAME", publisher.function_name)

        # ── Newsletter & Contact config ───────────────────────────────────
        site_url = os.environ.get("SITE_URL", "https://www.lobservatoiredebegles.fr")
        sender_email = os.environ.get("SENDER_EMAIL", "noreply@lobservatoiredebegles.fr")
        contact_sender = os.environ.get("CONTACT_SENDER", "contact@lobservatoiredebegles.fr")
        admin_email = os.environ.get("ADMIN_EMAIL", "")
        ses_identity_arns = [
            f"arn:aws:ses:{self.region}:{self.account}:identity/lobservatoiredebegles.fr",
        ]

        # ── Lambda: SubscribeFunction ─────────────────────────────────────
        mail_api_key = os.environ.get("BREVO_API_KEY", "")
        brevo_list_id = os.environ.get("BREVO_LIST_ID", "2")
        brevo_template_id = os.environ.get("BREVO_TEMPLATE_ID", "1")

        api = apigw.RestApi(
            self, "WatchdogApi",
            rest_api_name="watchdog-api",
            deploy_options=apigw.StageOptions(stage_name="prod"),
            default_cors_preflight_options=apigw.CorsOptions(
                allow_origins=apigw.Cors.ALL_ORIGINS,
                allow_methods=["GET", "POST", "OPTIONS"],
                allow_headers=["Content-Type"],
            ),
        )

        subscribe_fn = lambda_.Function(
            self, "SubscribeFunction",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/subscriber.zip"),
            timeout=Duration.seconds(10),
            environment={
                "TABLE_NAME": subscribers_table.table_name,
                "SENDER_EMAIL": sender_email,
                "MAIL_API_KEY": mail_api_key,
                "BREVO_LIST_ID": brevo_list_id,
                "BREVO_TEMPLATE_ID": brevo_template_id,
                "API_URL": f"https://{api.rest_api_id}.execute-api.{self.region}.amazonaws.com/prod/",
            },
        )
        subscribers_table.grant_read_write_data(subscribe_fn)

        confirmer_fn = lambda_.Function(
            self, "ConfirmerFunction",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/confirmer.zip"),
            timeout=Duration.seconds(10),
            environment={
                "TABLE_NAME": subscribers_table.table_name,
                "MAIL_API_KEY": mail_api_key,
                "BREVO_LIST_ID": brevo_list_id,
                "REDIRECTION_URL": f"{site_url}/merci.html",
            },
        )
        subscribers_table.grant_read_write_data(confirmer_fn)

        # ── Lambda: ContactFunction ───────────────────────────────────────
        contact_fn = lambda_.Function(
            self, "ContactFunction",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/contact.zip"),
            timeout=Duration.seconds(10),
            environment={
                "SENDER_EMAIL": contact_sender,
                "ADMIN_EMAIL": admin_email,
                "MAIL_API_KEY": mail_api_key,
            },
        )
        contact_fn.add_to_role_policy(iam.PolicyStatement(
            actions=["ses:SendEmail"],
            resources=ses_identity_arns,
        ))

        # ── API Gateway Routing ───────────────────────────────────────────
        api.root.add_resource("subscribe").add_method("POST", apigw.LambdaIntegration(subscribe_fn))
        api.root.add_resource("contact").add_method("POST", apigw.LambdaIntegration(contact_fn))
        api.root.add_resource("confirm").add_method("GET", apigw.LambdaIntegration(confirmer_fn))

        # ── Website Deployment (Architecture Sécurisée) ─────────────────────────────

        # 1. Déploiement Principal : Assets Statiques (Images, Fonts) - SANS Invalidation
        deploy_website = s3_deploy.BucketDeployment(
            self, "DeployWebsite",
            sources=[s3_deploy.Source.asset("../frontend", 
                exclude=[
                    "data.json", "index.html", "app.js", "style.css", "input.css",
                    "node_modules/*", "package.json", "package-lock.json", "tailwind.config.js",
                    "*.md", ".gitignore"
                ])],
            destination_bucket=website_bucket,
            # On retire la distribution ici pour gagner 10-15 minutes de déploiement
            cache_control=[s3_deploy.CacheControl.from_string("public, max-age=31536000, immutable")],
        )

        # 2. Déploiement de la Configuration (HTML/CSS/JS) - AVEC Invalidation Chirurgicale
        deploy_config = s3_deploy.BucketDeployment(
            self, "DeployWebsiteConfig",
            sources=[s3_deploy.Source.asset("../frontend", 
                exclude=[
                    "*.png", "*.svg", "*.webp", "fonts/*", "node_modules/*", 
                    "data.json", "package.json", "package-lock.json", "input.css", "tailwind.config.js"
                ])],
            destination_bucket=website_bucket,
            distribution=distribution,
            # ON NE PURGE QUE LES FICHIERS CRITIQUES (Très rapide : < 60s)
            distribution_paths=["/index.html", "/app.js", "/style.css", "/merci.html"],
            cache_control=[s3_deploy.CacheControl.from_string("no-cache, no-store, must-revalidate")],
            prune=False,
        )

        # CRUCIAL : On force le Bloc 2 à s'exécuter APRES le Bloc 1 pour écraser le cache de 365j sur index.html par du no-cache.
        deploy_config.node.add_dependency(deploy_website)

        # 3. Déploiement des Données (data.json) : Cache Long SANS Invalidation
        # On utilise une approche plus directe pour ne pas être bloqué par les patterns d'exclusion
        deploy_data = s3_deploy.BucketDeployment(
            self, "DeployDataJson",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["*", "!data.json"])],
            destination_bucket=website_bucket,
            # Invalidation temporaire pour forcer le nettoyage de l'erreur 404
            distribution=distribution,
            distribution_paths=["/data.json"],
            cache_control=[s3_deploy.CacheControl.from_string("public, max-age=31536000, immutable")],
            prune=False,
        )

        # On s'assure que DeployDataJson s'exécute après DeployWebsite pour éviter un potentiel PRUNE
        deploy_data.node.add_dependency(deploy_website)

        # ── Monitoring & Dashboard ────────────────────────────────────────
        dashboard = cloudwatch.Dashboard(self, "WatchdogDashboard", dashboard_name="Watchdog-Begles-Health")
        
        dashboard.add_widgets(
            cloudwatch.GraphWidget(
                title="Lambda Errors",
                left=[
                    orchestrator.metric_errors(label="Orchestrator"),
                    worker.metric_errors(label="Worker"),
                    publisher.metric_errors(label="Publisher"),
                ],
                width=12
            ),
            cloudwatch.GraphWidget(
                title="SQS Activity",
                left=[
                    pdf_queue.metric_approximate_number_of_messages_visible(label="Messages en attente"),
                    dlq.metric_approximate_number_of_messages_visible(label="Échecs (DLQ)"),
                ],
                width=12
            ),
            cloudwatch.LogQueryWidget(
                title="Gemini Token Usage (Input/Output)",
                log_group_names=[worker.log_group.log_group_name],
                query_lines=[
                    "filter @message like /METRIC: GeminiUsage/",
                    "parse @message \"input=* output=*\" as in_t, out_t",
                    "stats sum(in_t) as Total_Input, sum(out_t) as Total_Output by bin(1d)"
                ],
                width=12
            ),
            cloudwatch.LogQueryWidget(
                title="Estimation Coût Gemini ($)",
                log_group_names=[worker.log_group.log_group_name],
                query_lines=[
                    "filter @message like /METRIC: GeminiUsage/",
                    "parse @message \"input=* output=*\" as in_t, out_t",
                    "fields (in_t * 0.00000125) as cost_in, (out_t * 0.00000375) as cost_out",
                    "stats sum(cost_in + cost_out) as Total_Cost_USD by bin(1d)"
                ],
                width=12
            )
        )

        # ── EventBridge: Daily trigger (Mon-Fri at 18:00 Paris / 16:00 UTC) ───
        events.Rule(
            self, "DailyTrigger",
            schedule=events.Schedule.cron(week_day="MON-FRI", hour="16", minute="0"),
            targets=[targets.LambdaFunction(orchestrator)],
        )

        # ── Stack Outputs ─────────────────────────────────────────────────
        CfnOutput(self, "WebsiteUrl", value=website_bucket.bucket_website_url)
        CfnOutput(self, "CloudFrontUrl", value=distribution.distribution_domain_name)
        CfnOutput(self, "ApiUrl", value=api.url)
