package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
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

func (sc *Scraper) ScrapeCouncilList(ctx context.Context) ([]CouncilListing, error) {
	doc, err := fetchDocument(ctx, sc.listURL)
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
		// The municipality can publish a title with the wrong year. A council
		// cannot take place after its deliberations have been published.
		sessionDate, correctedTitle := councilDateAndTitle(title, pubDate)
		if sessionDate == "" {
			sessionDate = pubDate
		}
		if sessionDate == "" {
			log.Printf("warn: council %q has no date (no <time datetime> and no date in title) — skipping", title)
			return
		}
		listings = append(listings, CouncilListing{
			CouncilID: url,
			Title:     correctedTitle,
			Category:  normalizeCategory(category, title),
			Date:      sessionDate,
			URL:       url,
			Summary:   strings.Replace(summary, title, correctedTitle, 1),
		})
	})
	return listings, nil
}

func (sc *Scraper) ScrapePDFLinks(ctx context.Context, councilURL string) ([]PDFItem, error) {
	doc, err := fetchDocument(ctx, councilURL)
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
	// Distinct documents sometimes point to the same municipal URL. Repair a
	// mismatched link only when the filename implied by its label exists.
	for i := 0; i < len(items); i++ {
		for j := 0; j < i; j++ {
			if items[i].URL != items[j].URL {
				continue
			}
			repaired := false
			for _, index := range []int{j, i} {
				candidate := pdfURLFromTitle(items[index].URL, items[index].Title)
				if candidate != "" && candidate != items[index].URL && pdfURLExists(ctx, candidate) {
					log.Printf("warn: duplicate municipal PDF link %s; using verified %s for %s", items[index].URL, candidate, items[index].Title)
					items[index].URL = candidate
					repaired = true
					break
				}
			}
			if !repaired {
				return nil, fmt.Errorf("duplicate PDF link %s for %q and %q", items[i].URL, items[j].Title, items[i].Title)
			}
		}
	}
	return items, nil
}

func councilDateAndTitle(title, published string) (string, string) {
	session := parseDateFromTitle(title)
	publicationDate, pubErr := time.Parse("2006-01-02", published)
	sessionDate, sessionErr := time.Parse("2006-01-02", session)
	if pubErr != nil || sessionErr != nil || !sessionDate.After(publicationDate) {
		return session, title
	}
	for _, year := range []int{publicationDate.Year(), publicationDate.Year() - 1} {
		candidate := time.Date(year, sessionDate.Month(), sessionDate.Day(), 0, 0, 0, 0, time.UTC)
		if candidate.Month() != sessionDate.Month() || candidate.Day() != sessionDate.Day() || candidate.After(publicationDate) || publicationDate.Sub(candidate) > 365*24*time.Hour {
			continue
		}
		fixed := strings.Replace(title, fmt.Sprint(sessionDate.Year()), fmt.Sprint(year), 1)
		log.Printf("warn: council title date %s is after publication %s; using %s", session, published, candidate.Format("2006-01-02"))
		return candidate.Format("2006-01-02"), fixed
	}
	return session, title
}

var pdfLabelRE = regexp.MustCompile(`(?i)\bD\s*(\d{1,2})\s*[-–—]?\s*(\d{4})[_-](\d{2,3})\b`)

func pdfURLFromTitle(href, title string) string {
	match := pdfLabelRE.FindStringSubmatch(title)
	parsed, err := url.Parse(href)
	if match == nil || err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	number, _ := strconv.Atoi(match[1])
	sequence, _ := strconv.Atoi(match[3])
	parsed.Path = path.Join(path.Dir(parsed.Path), fmt.Sprintf("D%02d-%s_%03d.pdf", number, match[2], sequence))
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func pdfURLExists(ctx context.Context, href string) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, href, nil)
	if err != nil {
		return false
	}
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK && strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/pdf")
}

func (sc *Scraper) ScrapeNextCouncilDate(ctx context.Context, url string) (string, error) {
	doc, err := fetchDocument(ctx, url)
	if err != nil {
		return "", err
	}

	now := time.Now()
	todayStr := now.Format("2006-01-02")
	todayTime, _ := time.Parse("2006-01-02", todayStr)

	var nextDate string
	dateRe := regexp.MustCompile(`(?i)(?:lundi|mardi|mercredi|jeudi|vendredi|samedi|dimanche)?\s*(\d{1,2})\s+([a-zéû]+)(?:\s+(\d{4}))?`)

	doc.Find(".infowidget .rte ul li strong").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		text := strings.TrimSpace(s.Text())
		if text == "" {
			return true
		}

		m := dateRe.FindStringSubmatch(text)
		if m == nil {
			if nextDate == "" {
				nextDate = text
			}
			return true
		}

		day, _ := strconv.Atoi(m[1])
		monthName := strings.ToLower(m[2])
		monthNumStr, ok := frMonthMap[monthName]
		if !ok {
			if nextDate == "" {
				nextDate = text
			}
			return true
		}
		month, _ := strconv.Atoi(monthNumStr)

		year := now.Year()
		if m[3] != "" {
			year, _ = strconv.Atoi(m[3])
		}

		parsedDate := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)

		if parsedDate.Before(todayTime) && m[3] == "" {
			parsedDate = time.Date(year+1, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		}

		if !parsedDate.Before(todayTime) {
			nextDate = text
			return false // break loop
		}

		return true
	})

	if nextDate == "" {
		nextDate = strings.TrimSpace(doc.Find(".infowidget .rte ul li strong").First().Text())
	}

	return nextDate, nil
}

func fetchDocument(ctx context.Context, url string) (*goquery.Document, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	limited := http.MaxBytesReader(nil, resp.Body, 10<<20)
	return goquery.NewDocumentFromReader(limited)
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

func normalizeCategory(cat, title string) string {
	catLow := strings.ToLower(cat)
	titleLow := strings.ToLower(title)
	if strings.Contains(catLow, "ccas") || strings.Contains(catLow, "centre communal") ||
		strings.Contains(titleLow, "ccas") || strings.Contains(titleLow, "centre communal") {
		return "CCAS"
	}
	if strings.Contains(catLow, "estey") || strings.Contains(titleLow, "estey") {
		return "Estey"
	}
	return "Conseil municipal"
}
