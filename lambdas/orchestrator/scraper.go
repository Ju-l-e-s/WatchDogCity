package main

import (
	"fmt"
	"net/http"
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
	resp, err := http.Get(sc.listURL)
	if err != nil {
		return nil, fmt.Errorf("http get list page: %w", err)
	}
	defer resp.Body.Close()

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse html: %w", err)
	}

	var listings []CouncilListing
	doc.Find("article.publications-list-item").Each(func(_ int, s *goquery.Selection) {
		link := s.Find("a.publications-list-item__title-link")
		url, _ := link.Attr("href")
		title := strings.TrimSpace(link.Find("span.underline").Text())
		
		category := strings.TrimSpace(s.Find("span.theme").Text())
		if category == "" {
			category = "Conseil municipal"
		}
		
		pubDate, _ := s.Find("time").Attr("datetime")

		if url == "" || pubDate == "" {
			return
		}
		listings = append(listings, CouncilListing{
			CouncilID: url, 
			Title:     title,
			Category:  normalizeCategory(category),
			Date:      pubDate,
			URL:       url,
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

func normalizeCategory(cat string) string {
	cat = strings.ToLower(cat)
	if strings.Contains(cat, "ccas") {
		return "CCAS"
	}
	if strings.Contains(cat, "estey") {
		return "Estey"
	}
	return "Conseil municipal"
}
