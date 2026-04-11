import json
import boto3
import time
from datetime import datetime

# Configuration
DATA_FILE = 'frontend/data.json'
COUNCILS_TABLE = 'watchdog-councils'
DELIBERATIONS_TABLE = 'watchdog-deliberations'
REGION = 'eu-west-3'

def restore():
    print(f"🚀 Démarrage de la restauration depuis {DATA_FILE}...")
    
    try:
        with open(DATA_FILE, 'r', encoding='utf-8') as f:
            data = json.load(f)
    except FileNotFoundError:
        print(f"❌ Erreur : Fichier {DATA_FILE} introuvable.")
        return

    dynamodb = boto3.resource('dynamodb', region_name=REGION)
    councils_table = dynamodb.Table(COUNCILS_TABLE)
    delibs_table = dynamodb.Table(DELIBERATIONS_TABLE)

    # 1. Restaurer les métadonnées du prochain conseil
    next_date = data.get('next_council_date')
    if next_date:
        print(f"📝 Restauration des métadonnées : {next_date}")
        councils_table.put_item(Item={
            'council_id': 'metadata#next_council',
            'date_text': next_date,
            'updated_at': datetime.utcnow().isoformat() + 'Z'
        })

    # 2. Restaurer les conseils et délibérations
    councils = data.get('councils', [])
    print(f"📦 {len(councils)} conseils trouvés.")

    with councils_table.batch_writer() as council_batch, \
         delibs_table.batch_writer() as delib_batch:
        
        for c in councils:
            council_id = c.get('id')
            print(f"  🔹 Traitement du conseil : {c.get('title')}")
            
            # Préparation de l'item Conseil
            # Note: total_pdfs et processed_pdfs sont estimés à partir du nombre de délibs présentes
            delibs = c.get('deliberations', [])
            council_item = {
                'council_id': council_id,
                'category': c.get('category'),
                'date': c.get('date'),
                'title': c.get('title'),
                'summary': c.get('summary'),
                'source_url': c.get('source_url'),
                'total_pdfs': len(delibs),
                'processed_pdfs': len(delibs),
                'created_at': data.get('generated_at', datetime.utcnow().isoformat() + 'Z')
            }
            council_batch.put_item(Item=council_item)

            # Préparation des items Délibérations
            for d in delibs:
                # On aplatit l'objet vote pour correspondre au schéma DynamoDB
                vote = d.get('vote', {})
                delib_item = {
                    'id': d.get('id'),
                    'council_id': council_id,
                    'title': d.get('title'),
                    'topic_tag': d.get('topic_tag'),
                    'pdf_url': d.get('pdf_url'),
                    'summary': d.get('summary'),
                    'is_substantial': d.get('is_substantial', False),
                    'acronyms': d.get('acronyms'),
                    'analysis_data': d.get('analysis_data'),
                    'has_vote': vote.get('has_vote', False),
                    'vote_pour': vote.get('pour'),
                    'vote_contre': vote.get('contre'),
                    'vote_abstention': vote.get('abstention'),
                    'disagreements': d.get('disagreements'),
                    'processed_at': data.get('generated_at', datetime.utcnow().isoformat() + 'Z')
                }
                # Nettoyage des valeurs None (DynamoDB n'aime pas les None pour certains types)
                delib_item = {k: v for k, v in delib_item.items() if v is not None}
                delib_batch.put_item(Item=delib_item)

    print("\n✅ Restauration terminée avec succès !")
    print(f"   - Table {COUNCILS_TABLE} mise à jour.")
    print(f"   - Table {DELIBERATIONS_TABLE} mise à jour.")

if __name__ == "__main__":
    restore()
