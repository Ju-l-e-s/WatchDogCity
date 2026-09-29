package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "article":
		if err := runArticle(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ article: %v\n", err)
			os.Exit(1)
		}
	case "newsletter":
		if err := runNewsletter(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ newsletter: %v\n", err)
			os.Exit(1)
		}
	case "newsletter-test":
		if err := runNewsletterTest(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "❌ newsletter-test: %v\n", err)
			os.Exit(1)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Sous-commande inconnue: %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`🔬 Watchdog Local Test — essais locaux et envoi explicite à la liste test

PRÉREQUIS:
  article/newsletter : GEMINI_API_KEY
  newsletter-test  : accès AWS en lecture ou --fixture ; BREVO_API_KEY uniquement avec --send

USAGE:
  go run . article    --pdf <chemin.pdf>  [--model gemini-2.5-pro]
  go run . newsletter [--fixtures <chemin.json>] [--sample] [--model gemini-2.5-pro]
  go run . newsletter-test (--council-id <id> | --fixture <fichier.json>) [--template-file <fichier.html>] [--send --confirm-list-id 3]
  go run . help

SOUS-COMMANDES:
  article      Analyse un PDF de délibération via Gemini + QC déterministe
  newsletter   Génère les paramètres newsletter via Gemini + preview HTML
  newsletter-test Lit les paramètres APPROVED et prépare un vrai email Brevo pour la liste test #3

OPTIONS GLOBALES:
  --model      Modèle Gemini pour article/newsletter (défaut: gemini-2.5-pro)

EXEMPLES:
  # Tester l'analyse d'un PDF
  GEMINI_API_KEY=xxx go run . article --pdf ~/Downloads/delib_budget.pdf

  # Tester la newsletter avec les données sample
  GEMINI_API_KEY=xxx go run . newsletter --sample

  # Tester la newsletter avec un fichier fixtures custom
  GEMINI_API_KEY=xxx go run . newsletter --fixtures ./fixtures/custom.json

  # Relire une newsletter APPROVED, puis recevoir le vrai email sur la liste test #3
  go run . newsletter-test --council-id '<ID_DU_CONSEIL>'
  BREVO_API_KEY=xxx go run . newsletter-test --fixture fixtures/approved_council_2026-06-22.json --template-file ../../docs/email-templates/brevo_template_v2.html --send --confirm-list-id 3

  # Utiliser un modèle différent
  GEMINI_API_KEY=xxx go run . article --pdf delib.pdf --model gemini-2.5-flash`)
}
