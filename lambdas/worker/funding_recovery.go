package main

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/ledongthuc/pdf"
	"github.com/watchdog/shared"
)

// Accept a positive documentary deferral for a future instrument. Other
// declarations of absence remain unresolved in this first version.
var explicitDeferral = regexp.MustCompile(`(?i)modalités[^.;:]{0,110}(seront|à définir|fixées ultérieurement)[^.;:]{0,100}(convention|avenant)`)

func canonicalQuote(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "’", "'")), " ")
}

// The evidence anchor reads only the cited physical pages. Images, form
// XObjects and undecodable fonts cannot establish an extractive addition.
func fundingPageText(ctx context.Context, original []byte, n int) (text string, err error) {
	// The PDF interpreter reports malformed streams with a panic.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("unreadable PDF page %d: %v", n, r)
		}
	}()
	reader, err := pdf.NewReader(bytes.NewReader(original), int64(len(original)))
	if err != nil {
		return "", err
	}
	if n < 1 || n > reader.NumPage() {
		return "", fmt.Errorf("invalid page %d", n)
	}
	page := reader.Page(n)
	streams := page.V.Key("Contents")
	var out strings.Builder
	var encoding pdf.TextEncoding
	var graphicsStack []pdf.TextEncoding
	fonts := map[string]pdf.TextEncoding{}
	show := func(v pdf.Value) {
		if encoding != nil {
			out.WriteString(encoding.Decode(v.RawString()))
		} else {
			out.WriteRune('�')
		}
	}
	read := func(stream pdf.Value) {
		pdf.Interpret(stream, func(stack *pdf.Stack, op string) {
			if ctx.Err() != nil || out.Len() > 2<<20 {
				panic("page reading limit reached")
			}
			switch op {
			case "Tf":
				stack.Pop()
				name := stack.Pop().Name()
				if fonts[name] == nil {
					font := page.Font(name)
					fonts[name] = font.Encoder()
				}
				encoding = fonts[name]
			case "TJ":
				items := stack.Pop()
				for i := 0; i < items.Len(); i++ {
					if v := items.Index(i); v.Kind() == pdf.String {
						show(v)
					}
				}
			case "Tj":
				show(stack.Pop())
			case "Tm", "Td", "TD", "T*", "ET":
				out.WriteByte(' ')
			case "q":
				graphicsStack = append(graphicsStack, encoding)
			case "Q":
				if len(graphicsStack) == 0 {
					panic("invalid graphics stack")
				}
				encoding = graphicsStack[len(graphicsStack)-1]
				graphicsStack = graphicsStack[:len(graphicsStack)-1]
			case "Do", "'", "\"":
				out.WriteRune('�')
				encoding = nil
			}
			for stack.Len() > 0 {
				stack.Pop()
			}
		})
	}
	if streams.Kind() == pdf.Array {
		for i := 0; i < streams.Len(); i++ {
			read(streams.Index(i))
		}
	} else {
		read(streams)
	}
	return canonicalQuote(out.String()), nil
}

func finishFundingReview(ctx context.Context, original []byte, result *GeminiResult, report factCheckResult) error {
	hold := func(reason string) error { return fmt.Errorf("%w: %s", errFactCheckReview, reason) }
	if !*report.Funding.ProseConsistent {
		return hold("public prose conflicts with final facts")
	}
	pending, err := pendingFinancialChecks(financialTargets(result), report.Financial)
	if err != nil {
		return err
	}
	if len(pending) > 0 {
		return hold("outstanding financial checks")
	}

	// Build a private copy. An error in the last clause must leave the
	// original candidate and its review stamp untouched.
	candidate := *result
	candidate.CriticalFacts = slices.Clone(result.CriticalFacts)

	pages := map[int]string{}
	anchor := func(p shared.PDFPassage) bool {
		if !validPassage(p) || p.Cell != "" {
			return false
		}
		page, ok := pages[p.Page]
		if !ok {
			page, err = fundingPageText(ctx, original, p.Page)
			if err != nil {
				return false
			}
			pages[p.Page] = page
		}
		return strings.Contains(page, canonicalQuote(p.Excerpt))
	}
	for _, row := range report.Funding.Checks {
		if row.Status == "NOT_ESTABLISHED" && (!anchor(*row.Evidence) || !explicitDeferral.MatchString(canonicalQuote(row.Evidence.Excerpt))) {
			return hold("unanchored exclusion or deferral")
		}
	}
	var remaining []factCheckFinding
	for _, f := range report.Findings {
		recoverable := f.Kind == "OMISSION" && f.Resolution == "BLOCK" && f.Field == "critical_facts" && f.FactID == "" && f.FinancialFactID != "" &&
			slices.Contains([]string{"PAYMENT_MODE", "PREREQUISITES", "DEADLINE"}, f.fundingCategory)
		if !recoverable {
			remaining = append(remaining, f)
			continue
		}
		if f.Evidence == nil || f.Claim != f.Evidence.Excerpt || !anchor(*f.Evidence) {
			return hold("missing literal evidence for proposed condition")
		}
		if !slices.ContainsFunc(result.CriticalFacts, func(fact shared.SourceFact) bool {
			return fact.ID == f.FinancialFactID && fact.Money != nil && fact.Money.Category == "SUBVENTION"
		}) {
			return hold("condition has no matching grant")
		}
		duplicate := false
		for _, fact := range candidate.CriticalFacts {
			if fact.Kind != "CONDITION" {
				continue
			}
			if sameOmissionPassage(fact.Evidence, *f.Evidence) || canonicalQuote(fact.Label) == canonicalQuote(f.Claim) {
				if fact.FinancialFactID != f.FinancialFactID || canonicalQuote(fact.Label) != canonicalQuote(f.Claim) {
					return hold("overlapping or conflicting condition")
				}
				duplicate = true
			}
		}
		if duplicate {
			continue
		}
		id := "clause_" + digest([]byte(fmt.Sprintf("%s/%d/%s", f.FinancialFactID, f.Evidence.Page, canonicalQuote(f.Claim))))[:24]
		candidate.CriticalFacts = append(candidate.CriticalFacts, shared.SourceFact{ID: id, Kind: "CONDITION", Audience: "ARTICLE", Status: "CONDITIONNEL", Label: f.Claim, Evidence: *f.Evidence, FinancialFactID: f.FinancialFactID})
	}
	report.Findings = remaining
	if err := acceptFactCheck(report, result.CriticalFacts); err != nil {
		return err
	}
	if err := validateCriticalFacts(&candidate); err != nil {
		return hold(err.Error())
	}
	payload, err := factCheckPayload(&candidate)
	if err != nil {
		return err
	}
	packet := *result.SourceFacts
	packet.ProofStatus = shared.ProofModelReviewed
	packet.ReviewedPayloadSHA256 = digest(payload)
	packet.Facts = candidate.CriticalFacts
	candidate.SourceFacts = &packet
	*result = candidate
	return nil
}
