#!/usr/bin/env python3
import json
import os
import shutil
import html
import re
from datetime import datetime

BASE_URL = "https://www.lobservatoiredebegles.fr"
DATA_PATH = "data.json"
DIST_DIR = "dist"

THEME_COLORS = {
    "environnement": "#22c55e",
    "budget": "#a855f7",
    "urbanisme": "#f97316",
    "social": "#6366f1",
    "culture": "#f59e0b",
    "sport": "#ec4899",
    "securite": "#ef4444",
    "sécurité": "#ef4444",
    "mobilite": "#06b6d4",
    "mobilité": "#06b6d4",
    "administration": "#64748b",
    "education": "#3b82f6",
    "éducation": "#3b82f6",
    "default": "#9ca3af"
}

CSS = """
:root {
  --text: #1f2937; --bg: #f9fafb; --bg-card: #ffffff;
  --border: #e5e7eb; --primary: #2563eb;
  font-family: system-ui, -apple-system, sans-serif;
}
* { box-sizing: border-box; }
body { margin: 0; padding: 0; background: var(--bg); color: var(--text); line-height: 1.6; }
.container { max-width: 900px; margin: 0 auto; padding: 2rem 1rem; }
header { background: var(--bg-card); border-bottom: 1px solid var(--border); padding: 1rem; text-align: center; }
header a { text-decoration: none; color: var(--text); font-weight: bold; font-size: 1.5rem; display: flex; align-items: center; justify-content: center; gap: 0.5rem; }
nav { max-width: 900px; margin: 0 auto; padding: 1rem; font-size: 0.9rem; }
.breadcrumb { color: #6b7280; }
.breadcrumb a { color: var(--primary); text-decoration: none; }
h1, h2, h3 { line-height: 1.2; margin-top: 0; }
.card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 0.75rem; padding: 1.5rem; margin-bottom: 1.5rem; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
.card h2, .card h3 { margin-bottom: 0.5rem; }
.card a { color: var(--text); text-decoration: none; }
.card a:hover { color: var(--primary); }
.tag { display: inline-block; padding: 0.25rem 0.75rem; border-radius: 9999px; font-size: 0.75rem; font-weight: bold; color: white; margin-right: 0.5rem; margin-bottom: 0.5rem; text-decoration: none; }
.meta { font-size: 0.875rem; color: #4b5563; margin-bottom: 1rem; }
.budget { font-weight: bold; color: var(--text); background: #f3f4f6; display: inline-block; padding: 0.25rem 0.75rem; border-radius: 0.5rem; margin-top: 0.5rem; }
footer { text-align: center; padding: 2rem 1rem; border-top: 1px solid var(--border); margin-top: 3rem; color: #6b7280; font-size: 0.875rem; }
footer a { color: var(--primary); text-decoration: none; }
.spa-link { display: block; text-align: center; background: var(--primary); color: white; padding: 1rem; border-radius: 0.75rem; text-decoration: none; font-weight: bold; margin-bottom: 2rem; box-shadow: 0 4px 6px rgba(37, 99, 235, 0.2); transition: transform 0.2s; }
.spa-link:hover { transform: translateY(-2px); }
.grid { display: grid; gap: 1.5rem; grid-template-columns: repeat(auto-fill, minmax(300px, 1fr)); }
table { width: 100%; border-collapse: collapse; margin: 1rem 0; background: var(--bg-card); border-radius: 0.75rem; overflow: hidden; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
th, td { padding: 1rem; text-align: left; border-bottom: 1px solid var(--border); }
th { background: #f3f4f6; font-weight: 600; }
.pdf-link { display: inline-flex; align-items: center; gap: 0.5rem; background: #f3f4f6; padding: 0.5rem 1rem; border-radius: 0.5rem; color: var(--text); text-decoration: none; font-weight: 600; margin-bottom: 1.5rem; }
.pdf-link:hover { background: #e5e7eb; }
@media (max-width: 600px) {
  .grid { grid-template-columns: 1fr; }
}
"""

def slugify(text):
    text = text.lower()
    text = re.sub(r'\.pdf$', '', text)  # strip .pdf suffix
    text = re.sub(r'[éèêë]', 'e', text)
    text = re.sub(r'[àâä]', 'a', text)
    text = re.sub(r'[ùûü]', 'u', text)
    text = re.sub(r'[îï]', 'i', text)
    text = re.sub(r'[ôö]', 'o', text)
    text = re.sub(r'[ç]', 'c', text)
    text = re.sub(r'[^a-z0-9]+', '-', text)
    return text.strip('-')

def get_color(tag):
    return THEME_COLORS.get(slugify(tag), THEME_COLORS["default"])

def format_euros(amount):
    if not amount: return "N/A"
    return f"{amount:,.0f} €".replace(',', ' ')

def generate_html(title, desc, url_path, content, breadcrumb, schema=None):
    canonical = f"{BASE_URL}{url_path}"
    schema_script = f'\n    <script type="application/ld+json">\n    {json.dumps(schema, indent=2)}\n    </script>' if schema else ""
    return f"""<!DOCTYPE html>
<html lang="fr">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{html.escape(title)} - WatchdogCity Bègles</title>
    <meta name="description" content="{html.escape(desc)}">
    <link rel="canonical" href="{canonical}">
    <link rel="icon" href="data:image/svg+xml,<svg xmlns=%22http://www.w3.org/2000/svg%22 viewBox=%220 0 100 100%22><text y=%22.9em%22 font-size=%2290%22>🔭</text></svg>">
    
    <meta property="og:title" content="{html.escape(title)}">
    <meta property="og:description" content="{html.escape(desc)}">
    <meta property="og:url" content="{canonical}">
    <meta property="og:type" content="website">
    <meta property="og:site_name" content="L'Observatoire de Bègles">
    
    <meta name="twitter:card" content="summary_large_image">
    <meta name="twitter:title" content="{html.escape(title)}">
    <meta name="twitter:description" content="{html.escape(desc)}">
    
    <style>{CSS}</style>{schema_script}
</head>
<body>
    <header>
        <a href="/">🔭 WatchdogCity Bègles</a>
    </header>
    <nav>
        <div class="breadcrumb">{breadcrumb}</div>
    </nav>
    <main class="container">
        {content}
    </main>
    <footer>
        <p>Une initiative citoyenne. Accédez à la <a href="https://www.lobservatoiredebegles.fr">version interactive complète</a>.</p>
    </footer>
</body>
</html>"""

def main():
    if not os.path.exists(DATA_PATH):
        print(f"Erreur: Fichier {DATA_PATH} introuvable.")
        return

    with open(DATA_PATH, 'r', encoding='utf-8') as f:
        try:
            data = json.load(f)
        except json.JSONDecodeError as e:
            print(f"Erreur: {DATA_PATH} est malformé. {e}")
            return

    if os.path.exists(DIST_DIR):
        shutil.rmtree(DIST_DIR)
    os.makedirs(DIST_DIR)

    sitemap_urls = []

    def write_page(filepath, html_content, url_path):
        full_path = os.path.join(DIST_DIR, filepath)
        os.makedirs(os.path.dirname(full_path), exist_ok=True)
        with open(full_path, 'w', encoding='utf-8') as f:
            f.write(html_content)
        sitemap_urls.append(url_path)

    councils = sorted(
        [c for c in data.get('councils', []) if c.get('category') == 'Conseil municipal'],
        key=lambda x: x.get('date', ''),
        reverse=True
    )

    all_deliberations = []
    themes = {}

    for c in councils:
        c_date = c.get('date', 'inconnue')
        c_title = c.get('title', f"Conseil municipal du {c_date}")

        for d in c.get('deliberations', []):
            d['council_date'] = c_date
            d['council_title'] = c_title
            all_deliberations.append(d)

            tags = [t.strip() for t in d.get('topic_tag', '').split('|') if t.strip()]
            for tag in tags:
                if tag not in themes:
                    themes[tag] = []
                themes[tag].append(d)

    # 1. Accueil
    home_title = "Tous les Conseils Municipaux"
    home_desc = "Retrouvez les résumés, délibérations et analyses budgétaires de tous les conseils municipaux de la ville de Bègles."
    home_content = f"""
    <a href="https://www.lobservatoiredebegles.fr" class="spa-link">Explorer les données de façon interactive 🔭</a>
    <h1>{home_title}</h1>
    <div style="margin-bottom: 2rem;"><a href="/budget/" class="tag" style="background: {THEME_COLORS['budget']}; font-size: 1rem;">💰 Voir le récapitulatif du budget global</a></div>
    <div class="grid">
    """

    for c in councils:
        c_date = c.get('date', '')
        c_title = c.get('title', '')
        dels = c.get('deliberations', [])
        budget = sum(d.get('budget_impact') or 0 for d in dels)
        home_content += f"""
        <div class="card">
            <h2><a href="/conseils/{c_date}/">{html.escape(c_title)}</a></h2>
            <div class="meta">🗓 {c_date} &nbsp;|&nbsp; 📝 {len(dels)} délibérations</div>
            <p>{html.escape(c.get('summary', '')[:150])}...</p>
            <div class="budget">Impact budget: {format_euros(budget)}</div>
        </div>"""
    home_content += "</div>"

    schema_home = {
        "@context": "https://schema.org",
        "@type": "WebSite",
        "name": "WatchdogCity Bègles",
        "url": BASE_URL,
        "description": home_desc
    }
    write_page("index.html", generate_html(home_title, home_desc, "/", home_content, "Accueil", schema_home), "/")

    # 2. Pages par conseil
    for c in councils:
        c_date = c.get('date', '')
        c_title = c.get('title', '')
        dels = c.get('deliberations', [])

        page_title = f"{c_title} - {c_date}"
        page_desc = c.get('summary', f"Toutes les délibérations du {c_title}")
        breadcrumb = f'<a href="/">Accueil</a> > {c_date}'

        content = f"<h1>{html.escape(c_title)}</h1><p>{html.escape(page_desc)}</p><h2>Délibérations ({len(dels)})</h2><div class=\"grid\">"

        for d in dels:
            d_id = slugify(d.get('id', ''))
            tags = [t.strip() for t in d.get('topic_tag', '').split('|') if t.strip()]
            tags_html = "".join([f'<a href="/themes/{slugify(t)}/" class="tag" style="background:{get_color(t)}">{html.escape(t)}</a>' for t in tags])
            content += f"""
            <div class="card">
                <h3><a href="/deliberations/{d_id}/">{html.escape(d.get('title', ''))}</a></h3>
                <div>{tags_html}</div>
                <p>{html.escape(d.get('summary', '')[:120])}...</p>
                <div class="budget">Budget: {format_euros(d.get('budget_impact', 0))}</div>
            </div>"""
        content += "</div>"

        schema_council = {
            "@context": "https://schema.org",
            "@type": "Event",
            "name": page_title,
            "startDate": c_date,
            "description": page_desc,
            "url": f"{BASE_URL}/conseils/{c_date}/"
        }
        write_page(f"conseils/{c_date}/index.html", generate_html(page_title, page_desc, f"/conseils/{c_date}/", content, breadcrumb, schema_council), f"/conseils/{c_date}/")

    # 3. Pages par délibération
    for d in all_deliberations:
        d_id = slugify(d.get('id', ''))
        c_date = d.get('council_date', '')
        title = d.get('title', '')
        desc = d.get('summary', '')

        breadcrumb = f'<a href="/">Accueil</a> > <a href="/conseils/{c_date}/">Conseil du {c_date}</a> > {d_id}'
        tags = [t.strip() for t in d.get('topic_tag', '').split('|') if t.strip()]
        tags_html = "".join([f'<a href="/themes/{slugify(t)}/" class="tag" style="background:{get_color(t)}">{html.escape(t)}</a>' for t in tags])

        analysis = d.get('analysis_data', {}) or {}
        vote = d.get('vote', {}) or {}

        vote_html = ""
        if vote.get('has_vote'):
            vote_html = f"""
            <h3>Résultat du vote</h3>
            <ul>
                <li><strong>Pour:</strong> {vote.get('pour', 'N/A')}</li>
                <li><strong>Contre:</strong> {vote.get('contre', 'N/A')}</li>
                <li><strong>Abstention:</strong> {vote.get('abstention', 'N/A')}</li>
            </ul>
            """

        pdf_link = f"<a href=\"{d.get('pdf_url', '#')}\" target=\"_blank\" class=\"pdf-link\">📄 Consulter le PDF original</a>" if d.get('pdf_url') else ""

        content = f"""
        <h1>{html.escape(title)}</h1>
        <div style="margin-bottom: 1rem;">{tags_html}</div>
        <div class="budget" style="margin-bottom: 1.5rem; font-size: 1.1rem;">Impact budgétaire: {format_euros(d.get('budget_impact', 0))}</div>
        <div>{pdf_link}</div>
        
        <div class="card">
            <h2>Résumé</h2>
            <p>{html.escape(desc)}</p>
            
            <h2>Analyse détaillée</h2>
            <h3>Contexte</h3>
            <p>{html.escape(analysis.get('contexte') or 'Non précisé')}</p>
            
            <h3>Décision</h3>
            <p>{html.escape(analysis.get('decision') or 'Non précisée')}</p>
            
            <h3>Impacts</h3>
            <p>{html.escape(analysis.get('impacts') or 'Non précisés')}</p>
            
            <h3>Points débattus</h3>
            <p>{html.escape(analysis.get('points_debattus') or 'Aucun débat notable.')}</p>
            
            {vote_html}
        </div>
        """

        schema_delib = {
            "@context": "https://schema.org",
            "@type": "Article",
            "headline": title,
            "description": desc,
            "datePublished": c_date,
            "url": f"{BASE_URL}/deliberations/{d_id}/"
        }
        write_page(f"deliberations/{d_id}/index.html", generate_html(title, desc, f"/deliberations/{d_id}/", content, breadcrumb, schema_delib), f"/deliberations/{d_id}/")

    # 4. Pages par thème
    for theme, dels in themes.items():
        t_slug = slugify(theme)
        title = f"Délibérations : {theme}"
        desc = f"Retrouvez toutes les délibérations de la ville de Bègles concernant : {theme}"
        breadcrumb = f'<a href="/">Accueil</a> > Thèmes > {theme}'

        content = f"<h1>Thème : {html.escape(theme)}</h1><div class=\"grid\">"
        for d in sorted(dels, key=lambda x: x.get('council_date', ''), reverse=True):
            d_id = slugify(d.get('id', ''))
            content += f"""
            <div class="card" style="border-top: 4px solid {get_color(theme)}">
                <h3><a href="/deliberations/{d_id}/">{html.escape(d.get('title', ''))}</a></h3>
                <div class="meta">Conseil du {d.get('council_date', '')}</div>
                <p>{html.escape(d.get('summary', '')[:120])}...</p>
                <div class="budget">{format_euros(d.get('budget_impact', 0))}</div>
            </div>"""
        content += "</div>"

        schema_theme = {
            "@context": "https://schema.org",
            "@type": "CollectionPage",
            "name": title,
            "description": desc,
            "url": f"{BASE_URL}/themes/{t_slug}/"
        }
        write_page(f"themes/{t_slug}/index.html", generate_html(title, desc, f"/themes/{t_slug}/", content, breadcrumb, schema_theme), f"/themes/{t_slug}/")

    # 5. Page Budget
    budget_dels = sorted([d for d in all_deliberations if (d.get('budget_impact') or 0) > 0],
                         key=lambda x: x.get('budget_impact', 0), reverse=True)

    b_title = "Récapitulatif des Budgets"
    b_desc = "Classement de toutes les délibérations ayant un impact budgétaire pour la ville de Bègles, par ordre décroissant."
    breadcrumb = '<a href="/">Accueil</a> > Budget'

    content = f"<h1>{b_title}</h1><p>{b_desc}</p><div style='overflow-x: auto;'><table><thead><tr><th>Date</th><th>Délibération</th><th>Thèmes</th><th>Montant</th></tr></thead><tbody>"
    for d in budget_dels:
        d_id = slugify(d.get('id', ''))
        tags = [t.strip() for t in d.get('topic_tag', '').split('|') if t.strip()]
        tags_html = "".join([f'<span class="tag" style="background:{get_color(t)}">{html.escape(t)}</span>' for t in tags])

        content += f"""<tr>
            <td style="white-space: nowrap;">{d.get('council_date', '')}</td>
            <td><a href="/deliberations/{d_id}/">{html.escape(d.get('title', ''))}</a></td>
            <td>{tags_html}</td>
            <td class="budget" style="white-space: nowrap;">{format_euros(d.get('budget_impact'))}</td>
        </tr>"""
    content += "</tbody></table></div>"

    schema_budget = {
        "@context": "https://schema.org",
        "@type": "Dataset",
        "name": b_title,
        "description": b_desc,
        "url": f"{BASE_URL}/budget/"
    }
    write_page("budget/index.html", generate_html(b_title, b_desc, "/budget/", content, breadcrumb, schema_budget), "/budget/")

    # 6. Sitemap.xml
    now = datetime.now().strftime("%Y-%m-%d")
    sitemap_content = '<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n'
    for url in sitemap_urls:
        priority = "1.0" if url == "/" else "0.8" if url.startswith("/conseils/") else "0.6"
        sitemap_content += f"  <url>\n    <loc>{BASE_URL}{url}</loc>\n    <lastmod>{now}</lastmod>\n    <priority>{priority}</priority>\n  </url>\n"
    sitemap_content += "</urlset>"

    with open(os.path.join(DIST_DIR, "sitemap.xml"), 'w', encoding='utf-8') as f:
        f.write(sitemap_content)

    # 7. Robots.txt
    robots_content = f"User-agent: *\nAllow: /\nSitemap: {BASE_URL}/sitemap.xml\n"
    with open(os.path.join(DIST_DIR, "robots.txt"), 'w', encoding='utf-8') as f:
        f.write(robots_content)

    # 8. .nojekyll
    with open(os.path.join(DIST_DIR, ".nojekyll"), 'w', encoding='utf-8') as f:
        f.write("")

if __name__ == "__main__":
    main()
