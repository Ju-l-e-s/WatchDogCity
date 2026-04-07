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
    aws_secretsmanager as secretsmanager,
    aws_cloudfront as cloudfront,
    aws_cloudfront_origins as origins,
    aws_cloudwatch as cloudwatch,
    aws_s3_deployment as s3_deploy,
)
from constructs import Construct


class WatchdogStack(Stack):
    def __init__(self, scope: Construct, id: str, **kwargs):
        super().__init__(scope, id, **kwargs)

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
        subscribers_table.add_global_secondary_index(
            index_name="token-index",
            partition_key=dynamodb.Attribute(name="token", type=dynamodb.AttributeType.STRING),
        )

        # ── S3 Website Bucket (EXISTING) ──────────────────────────────────
        historical_bucket_name = "watchdogstack-websitebucket75c24d94-clsmaf2ocvxq"
        website_bucket = s3.Bucket.from_bucket_name(
            self, "WebsiteBucket",
            historical_bucket_name
        )

        # ── CloudFront Distribution ───────────────────────────────────────
        distribution = cloudfront.Distribution(
            self, "WebsiteDistribution",
            default_behavior=cloudfront.BehaviorOptions(
                origin=origins.S3StaticWebsiteOrigin(website_bucket),
                viewer_protocol_policy=cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
                compress=True, # Active GZIP et Brotli automatiquement
            ),
            default_root_object="index.html",
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

        # ── Secrets ───────────────────────────────────────────────────────
        gemini_secret = secretsmanager.Secret(
            self, "GeminiSecret",
            secret_name="watchdog/gemini-api-key",
            description="Clé API pour le moteur d'analyse IA Watchdog",
        )

        # ── Lambda common config ──────────────────────────────────────────
        common_env = {
            "COUNCILS_TABLE": councils_table.table_name,
            "DELIBERATIONS_TABLE": deliberations_table.table_name,
            "SUBSCRIBERS_TABLE": subscribers_table.table_name,
            "PDF_QUEUE_URL": pdf_queue.queue_url,
            "WEBSITE_BUCKET": website_bucket.bucket_name,
            "GEMINI_SECRET_ARN": gemini_secret.secret_arn,
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
            environment=common_env,
        )
        worker.add_event_source(lambda_events.SqsEventSource(
            pdf_queue,
            batch_size=1,
        ))
        councils_table.grant_read_write_data(worker)
        deliberations_table.grant_read_write_data(worker)
        gemini_secret.grant_read(worker)

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

        # Worker needs to invoke Publisher
        publisher.grant_invoke(worker)
        worker.add_environment("PUBLISHER_FUNCTION_NAME", publisher.function_name)

        # ── Lambda: Subscriber ────────────────────────────────────────────
        subscriber = lambda_.Function(
            self, "Subscriber",
            runtime=lambda_.Runtime.PROVIDED_AL2023,
            architecture=lambda_.Architecture.ARM_64,
            handler="bootstrap",
            code=lambda_.Code.from_asset("../dist/subscriber.zip"),
            timeout=Duration.seconds(10),
            environment={
                **common_env,
                "FROM_EMAIL": "watchdog@begles.citoyen",
            },
        )
        subscribers_table.grant_read_write_data(subscriber)
        subscriber.add_to_role_policy(iam.PolicyStatement(
            actions=["ses:SendEmail"],
            resources=["*"],
        ))

        # ── API Gateway ───────────────────────────────────────────────────
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
        subscriber_integration = apigw.LambdaIntegration(subscriber)
        api.root.add_resource("subscribe").add_method("POST", subscriber_integration)
        api.root.add_resource("confirm").add_method("GET", subscriber_integration)

        # ── Website Deployment ───────────────────────────────────────────
        s3_deploy.BucketDeployment(
            self, "DeployWebsite",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["style.css", "index.html", "data.json"])],
            destination_bucket=website_bucket,
            distribution=distribution,
            distribution_paths=["/*"],
            cache_control=[s3_deploy.CacheControl.max_age(Duration.days(365))],
        )

        s3_deploy.BucketDeployment(
            self, "DeployWebsiteConfig",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["*.png", "*.svg", "node_modules/*", "data.json"])],
            destination_bucket=website_bucket,
            distribution=distribution,
            distribution_paths=["/index.html", "/style.css", "/app.js", "/fonts/*"],
            cache_control=[s3_deploy.CacheControl.no_cache()],
            prune=False,
        )

        s3_deploy.BucketDeployment(
            self, "DeployDataJson",
            sources=[s3_deploy.Source.asset("../frontend", exclude=["*", "!data.json"])],
            destination_bucket=website_bucket,
            distribution=distribution,
            distribution_paths=["/data.json"],
            cache_control=[s3_deploy.CacheControl.max_age(Duration.days(365))],
            prune=False,
        )

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
