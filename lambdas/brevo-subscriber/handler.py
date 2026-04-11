import json
import os
import re
import urllib.request
import urllib.error
import boto3
from botocore.exceptions import BotoCoreError, ClientError

_email_re = re.compile(r'^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$')
_ddb = boto3.resource('dynamodb')


def lambda_handler(event, context):
    # ── Parse body ──────────────────────────────────────────────────────────
    try:
        body = json.loads(event.get('body') or '{}')
    except json.JSONDecodeError:
        return _response(400, {'error': 'invalid JSON'})

    email = (body.get('email') or '').strip().lower()

    if not email or not _email_re.match(email):
        return _response(400, {'error': 'invalid email'})

    # ── Write to DynamoDB ────────────────────────────────────────────────────
    try:
        table = _ddb.Table(os.environ['TABLE_NAME'])
        table.put_item(Item={'email': email, 'status': 'confirmed'})
    except (BotoCoreError, ClientError) as exc:
        print(f'error: dynamodb put_item failed for {email}: {exc}')
        return _response(500, {'error': 'internal error'})

    # ── Send welcome email via Brevo ─────────────────────────────────────────
    _send_welcome_email(email)

    return _response(200, {'message': 'subscribed'})


def _send_welcome_email(email: str) -> None:
    payload = json.dumps({
        'sender': {
            'name': "L'Observatoire de Bègles",
            'email': 'contact@lobservatoiredebegles.fr',
        },
        'to': [{'email': email}],
        'subject': "Bienvenue à l'Observatoire",
        'htmlContent': (
            '<h1>Merci pour votre inscription</h1>'
            '<p>Vous recevrez bientôt nos analyses.</p>'
        ),
        'tracking': {
            'clicks': False
        }
    }).encode()

    req = urllib.request.Request(
        'https://api.brevo.com/v3/smtp/email',
        data=payload,
        headers={
            'api-key': os.environ['BREVO_API_KEY'],
            'Content-Type': 'application/json',
        },
        method='POST',
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            if resp.status >= 300:
                print(f'warn: brevo returned {resp.status} for {email}')
    except urllib.error.URLError as exc:
        print(f'warn: brevo call failed for {email}: {exc}')


def _response(status: int, body: dict) -> dict:
    return {
        'statusCode': status,
        'headers': {
            'Content-Type': 'application/json',
            'Access-Control-Allow-Origin': '*',
        },
        'body': json.dumps(body),
    }
