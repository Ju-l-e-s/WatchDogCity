package main

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

type CouncilListing struct {
	CouncilID string
	Title     string
	Category  string
	Date      string
	URL       string
	Summary   string
}

type PDFItem struct {
	Title string
	URL   string
}

type Scraper struct {
	listURL string
}

func NewScraper(listURL string) *Scraper {
	return &Scraper{listURL: listURL}
}

func (sc *Scraper) ScrapeCouncilList() ([]CouncilListing, error) {
	doc, err := fetchDocument(sc.listURL)
	if err != nil {
		return nil, fmt.Errorf("http get list page: %w", err)
	}

	var listings []CouncilListing
	doc.Find("li.list__item").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.publications-list-item__title-link")
		url, _ := link.Attr("href")
		if url == "" {
			return
		}
		title := strings.TrimSpace(link.Text())
		
		// Tentative d'extraction du résumé avec plusieurs sélecteurs possibles
		summary := strings.TrimSpace(s.Find(".publications-list-item__excerpt").Text())
		if summary == "" {
			summary = strings.TrimSpace(s.Find(".publications-list-item__text").Text())
		}
		if summary == "" {
			summary = strings.TrimSpace(s.Find(".publications-list-item__content").Text())
		}

		category := strings.TrimSpace(s.Find("span.theme").Text())
		if category == "" {
			category = "Conseil municipal"
		}

		pubDate, _ := s.Find("time").Attr("datetime")
		if pubDate == "" {
			return
		}
		// Prefer the actual session date extracted from the title over the
		// publication date from the <time datetime> attribute (which can be
		// a few days later than when the council actually met).
		sessionDate := parseDateFromTitle(title)
		if sessionDate == "" {
			sessionDate = pubDate
		}
		listings = append(listings, CouncilListing{
			CouncilID: url,
			Title:     title,
			Category:  normalizeCategory(category),
			Date:      sessionDate,
			URL:       url,
			Summary:   summary,
		})
	})
	return listings, nil
}

func (sc *Scraper) ScrapePDFLinks(councilURL string) ([]PDFItem, error) {
	doc, err := fetchDocument(councilURL)
	if err != nil {
		return nil, err
	}

	var items []PDFItem
	doc.Find(".telecharger-item").Each(func(_ int, s *goquery.Selection) {
		title := strings.TrimSpace(s.Find(".telecharger-item__title").Text())
		link := s.Find("a.telecharger-item__link")
		href, exists := link.Attr("href")
		
		if exists && strings.HasSuffix(strings.ToLower(href), ".pdf") {
			items = append(items, PDFItem{
				Title: title,
				URL:   href,
			})
		}
	})
	return items, nil
}

func (sc *Scraper) ScrapeNextCouncilDate(url string) (string, error) {
	doc, err := fetchDocument(url)
	if err != nil {
		return "", err
	}

	// Cible le premier <li> dans le widget des prochains conseils
	nextDate := strings.TrimSpace(doc.Find(".infowidget .rte ul li strong").First().Text())
	return nextDate, nil
}

func fetchDocument(url string) (*goquery.Document, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return goquery.NewDocumentFromReader(resp.Body)
}

var frMonthMap = map[string]string{
	"janvier": "01", "fevrier": "02", "février": "02", "mars": "03",
	"avril": "04", "mai": "05", "juin": "06", "juillet": "07",
	"aout": "08", "août": "08", "septembre": "09", "octobre": "10",
	"novembre": "11", "decembre": "12", "décembre": "12",
}

// parseDateFromTitle extracts the actual council session date from a French title like
// "Délibérations du conseil municipal du 21 avril 2026" → "2026-04-21".
// Returns "" if no date can be parsed.
func parseDateFromTitle(title string) string {
	// Match "du <day> <month> <year>" or "le <day> <month> <year>"
	re := regexp.MustCompile(`(?i)(?:du|le)\s+(\d{1,2})\s+([a-zéû]+)\s+(\d{4})`)
	m := re.FindStringSubmatch(strings.ToLower(title))
	if m == nil {
		return ""
	}
	monthNum, ok := frMonthMap[m[2]]
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s-%s-%02s", m[3], monthNum, m[1])
}

func normalizeCategory(cat string) string {
	cat = strings.ToLower(cat)
	if strings.Contains(cat, "ccas") || strings.Contains(cat, "centre communal") {
		return "CCAS"
	}
	if strings.Contains(cat, "estey") {
		return "Estey"
	}
	return "Conseil municipal"
}
