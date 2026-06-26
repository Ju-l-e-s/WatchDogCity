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
	case "help", "-h", "--help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Sous-commande inconnue: %q\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`🔬 Watchdog Local Test — Test Gemini prompts sans publier ni envoyer

PRÉREQUIS:
  export GEMINI_API_KEY=<votre clé>

USAGE:
  go run . article    --pdf <chemin.pdf>  [--model gemini-2.5-pro]
  go run . newsletter [--fixtures <chemin.json>] [--sample] [--model gemini-2.5-pro]
  go run . help

SOUS-COMMANDES:
  article      Analyse un PDF de délibération via Gemini + QC déterministe
  newsletter   Génère les paramètres newsletter via Gemini + preview HTML

OPTIONS GLOBALES:
  --model      Modèle Gemini à utiliser (défaut: gemini-2.5-pro)

EXEMPLES:
  # Tester l'analyse d'un PDF
  GEMINI_API_KEY=xxx go run . article --pdf ~/Downloads/delib_budget.pdf

  # Tester la newsletter avec les données sample
  GEMINI_API_KEY=xxx go run . newsletter --sample

  # Tester la newsletter avec un fichier fixtures custom
  GEMINI_API_KEY=xxx go run . newsletter --fixtures ./fixtures/custom.json

  # Utiliser un modèle différent
  GEMINI_API_KEY=xxx go run . article --pdf delib.pdf --model gemini-2.5-flash`)
}
