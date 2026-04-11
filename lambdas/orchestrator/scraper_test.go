package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
	listings, err := s.ScrapeCouncilList()
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
	pdfs, err := s.ScrapePDFLinks(server.URL)
	require.NoError(t, err)
	require.Len(t, pdfs, 2)

	assert.Equal(t, "https://example.com/D01.pdf", pdfs[0].URL)
	assert.Equal(t, "D01-2026_020 Élection du Maire", pdfs[0].Title)
	assert.Equal(t, "https://example.com/D02.pdf", pdfs[1].URL)
}

func TestNormalizeCategory(t *testing.T) {
	cases := []struct {
		raw      string
		expected string
	}{
		{"", "Conseil municipal"},
		{"Conseil municipal", "Conseil municipal"},
		{"Centre communal d'action sociale", "CCAS"},
		{"Centre social et culturel de l'Estey", "Estey"},
		{"Les établissements", "Conseil municipal"},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.expected, normalizeCategory(tc.raw), "input: %q", tc.raw)
	}
}
