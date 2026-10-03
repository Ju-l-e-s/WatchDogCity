package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const listPageHTML = `
<ul class="list is-columns-4">
  <li class="list__item">
    <article class="publications-list-item">
      <h3 class="publications-list-item__title">
        <a href="https://example.com/conseil-28-mars/" class="publications-list-item__title-link">
          <span class="underline">Délibérations du conseil municipal du 28 mars 2026</span>
        </a>
      </h3>
      <time datetime="2026-03-28">28/03/2026</time>
    </article>
  </li>
  <li class="list__item">
    <article class="publications-list-item">
      <h3 class="publications-list-item__title">
        <span class="theme publications-list-item__category">Centre communal d'action sociale</span>
        <a href="https://example.com/ccas-26-jan/" class="publications-list-item__title-link">
          <span class="underline">Délibérations du CCAS du 26 janvier 2026</span>
        </a>
      </h3>
      <time datetime="2026-01-26">26/01/2026</time>
    </article>
  </li>
</ul>`

const detailPageHTML = `
<ul class="telecharger__list">
  <li class="telecharger__list-item">
    <div class="telecharger-item">
      <p class="telecharger-item__title">D01-2026_020 Élection du Maire</p>
      <a class="btn telecharger-item__link" href="https://example.com/D01.pdf" download="">Télécharger</a>
    </div>
  </li>
  <li class="telecharger__list-item">
    <div class="telecharger-item">
      <p class="telecharger-item__title">D02-2026_021 Détermination du nombre d'adjoints</p>
      <a class="btn telecharger-item__link" href="https://example.com/D02.pdf" download="">Télécharger</a>
    </div>
  </li>
</ul>`

func TestScrapeCouncilList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(listPageHTML))
	}))
	defer server.Close()

	s := NewScraper(server.URL)
	listings, err := s.ScrapeCouncilList(context.Background())
	require.NoError(t, err)
	require.Len(t, listings, 2)

	assert.Equal(t, "https://example.com/conseil-28-mars/", listings[0].CouncilID)
	assert.Equal(t, "Conseil municipal", listings[0].Category)
	assert.Equal(t, "2026-03-28", listings[0].Date)
	assert.Equal(t, "https://example.com/conseil-28-mars/", listings[0].URL)

	assert.Equal(t, "https://example.com/ccas-26-jan/", listings[1].CouncilID)
	assert.Equal(t, "CCAS", listings[1].Category)
}

func TestScrapePDFLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(detailPageHTML))
	}))
	defer server.Close()

	s := NewScraper("unused")
	pdfs, err := s.ScrapePDFLinks(context.Background(), server.URL)
	require.NoError(t, err)
	require.Len(t, pdfs, 2)

	assert.Equal(t, "https://example.com/D01.pdf", pdfs[0].URL)
	assert.Equal(t, "D01-2026_020 Élection du Maire", pdfs[0].Title)
	assert.Equal(t, "https://example.com/D02.pdf", pdfs[1].URL)
}

func TestScrapeCouncilListCorrectsFutureYearFromPublication(t *testing.T) {
	html := `<li class="list__item"><a class="publications-list-item__title-link" href="https://example.com/council-2028/">Délibérations du conseil municipal du 28 septembre 2028</a><div class="publications-list-item__excerpt">Délibérations du conseil municipal du 28 septembre 2028 — 28 documents</div><time datetime="2026-10-01">01/10/2026</time></li>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(html))
	}))
	defer server.Close()

	listings, err := NewScraper(server.URL).ScrapeCouncilList(context.Background())
	require.NoError(t, err)
	require.Len(t, listings, 1)
	assert.Equal(t, "2026-09-28", listings[0].Date)
	assert.True(t, listings[0].DateFromTitle)
	assert.Equal(t, "Délibérations du conseil municipal du 28 septembre 2026", listings[0].Title)
	assert.Contains(t, listings[0].Summary, "septembre 2026")
	assert.Equal(t, "https://example.com/council-2028/", listings[0].CouncilID)
}

func TestParseDateFromTitle_OrdinalAndSingleDigit(t *testing.T) {
	assert.Equal(t, "2025-07-01", parseDateFromTitle("Conseil municipal du 1ᵉʳ juillet 2025"))
	assert.Equal(t, "2026-06-05", parseDateFromTitle("Délibérations du 5 juin 2026"))
	assert.Equal(t, "", parseDateFromTitle("Conseil municipal du 31 février 2026"))
}

func TestIsDeliberationPDFTitle_ExcludesAttachments(t *testing.T) {
	assert.False(t, isDeliberationPDFTitle("Ordre du jour"))
	assert.False(t, isDeliberationPDFTitle("D04 – Maquette du Budget"))
	assert.True(t, isDeliberationPDFTitle("D04 – Vote du budget primitif 2026"))
}

func TestScrapePDFLinksRepairsDuplicateMunicipalLink(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead && r.URL.Path == "/D07-2026_097.pdf" {
			w.Header().Set("Content-Type", "application/pdf")
			return
		}
		if r.URL.Path == "/detail" {
			w.Write([]byte(`<div class="telecharger-item"><p class="telecharger-item__title">D07 – 2026_097</p><a class="telecharger-item__link" href="` + server.URL + `/D08-2026_098.pdf">PDF</a></div><div class="telecharger-item"><p class="telecharger-item__title">D08 – 2026_098</p><a class="telecharger-item__link" href="` + server.URL + `/D08-2026_098.pdf">PDF</a></div>`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	items, err := NewScraper("unused").ScrapePDFLinks(context.Background(), server.URL+"/detail")
	require.NoError(t, err)
	require.Len(t, items, 2)
	assert.Equal(t, server.URL+"/D07-2026_097.pdf", items[0].URL)
	assert.Equal(t, server.URL+"/D08-2026_098.pdf", items[1].URL)
}

func TestFetchDocumentCancelledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.Write([]byte("<html></html>"))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err := fetchDocument(ctx, server.URL)
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, 100*time.Millisecond)
}

func TestNormalizeCategory(t *testing.T) {
	cases := []struct {
		cat      string
		title    string
		expected string
	}{
		// Category tag detected
		{"", "Délibérations du conseil municipal", "Conseil municipal"},
		{"Conseil municipal", "Délibérations du conseil municipal", "Conseil municipal"},
		{"Centre communal d'action sociale", "Délibérations du CCAS", "CCAS"},
		{"Centre social et culturel de l'Estey", "Délibérations de l'Estey", "Estey"},
		{"Les établissements", "Délibérations", "Conseil municipal"},
		// Title-based fallback (empty category tag)
		{"", "Délibérations du conseil d'administration du CCAS du 26 janvier 2026", "CCAS"},
		{"", "Délibérations du Conseil d'administration du Centre social et culturel l'Estey du 24 novembre 2025", "Estey"},
		{"", "Délibérations du conseil municipal du 28 mars 2026", "Conseil municipal"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, normalizeCategory(tc.cat, tc.title), "cat=%q title=%q", tc.cat, tc.title)
	}
}
