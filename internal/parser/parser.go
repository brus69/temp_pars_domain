package parser

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"domain-analyzer/internal/config"

	"github.com/PuerkitoBio/goquery"
)

type Result struct {
	Domain       string    `json:"domain"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Description  string    `json:"description"`
	H1           string    `json:"h1"`
	Text         string    `json:"text"`
	Phones       string    `json:"phones"`
	Emails       string    `json:"emails"`
	INN          string    `json:"inn"`
	SocialMedia  string    `json:"socialMedia"`
	Address      string    `json:"address"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"errorMessage"`
	ScrapedAt    time.Time `json:"scrapedAt"`
}

type Parser struct {
	cfg    *config.Config
	client *http.Client
	retry  int
	delay  time.Duration
}

func New(cfg *config.Config) *Parser {
	return &Parser{
		cfg: cfg,
		client: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		retry: cfg.Retry,
		delay: time.Duration(cfg.Delay) * time.Millisecond,
	}
}

func (p *Parser) ParseDomains(domains []string) <-chan *Result {
	results := make(chan *Result, len(domains))

	var wg sync.WaitGroup
	sem := make(chan struct{}, p.cfg.Workers)

	go func() {
		for _, domain := range domains {
			sem <- struct{}{}
			wg.Add(1)
			go func(d string) {
				defer wg.Done()
				result := p.parseDomain(d)
				results <- result
				<-sem
				time.Sleep(p.delay)
			}(domain)
		}
		wg.Wait()
		close(results)
	}()

	return results
}

func (p *Parser) parseDomain(domain string) *Result {
	result := &Result{
		Domain:    domain,
		ScrapedAt: time.Now(),
	}

	url := "https://" + domain
	if !strings.HasPrefix(domain, "http") {
		url = "https://" + domain
	}

	for attempt := 1; attempt <= p.retry; attempt++ {
		resp, err := p.client.Get(url)
		if err != nil {
			result.Status = "failed"
			result.ErrorMessage = fmt.Sprintf("attempt %d: %v", attempt, err)
			if attempt < p.retry {
				time.Sleep(time.Duration(attempt) * p.delay)
			}
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			result.Status = "failed"
			result.ErrorMessage = fmt.Sprintf("HTTP %d", resp.StatusCode)
			if attempt < p.retry {
				time.Sleep(time.Duration(attempt) * p.delay)
			}
			continue
		}

		doc, err := goquery.NewDocumentFromReader(resp.Body)
		if err != nil {
			result.Status = "failed"
			result.ErrorMessage = fmt.Sprintf("parse error: %v", err)
			if attempt < p.retry {
				time.Sleep(time.Duration(attempt) * p.delay)
			}
			continue
		}

		result.URL = url
		result.Title = doc.Find("title").First().Text()
		result.Description, _ = doc.Find("meta[name='description']").Attr("content")
		result.H1 = doc.Find("h1").First().Text()
		result.Text = extractText(doc)

		result.Phones = extractPhones(doc.Text())
		result.Emails = extractEmails(doc.Text())
		result.INN = extractINN(doc.Text())
		result.SocialMedia = extractSocialMedia(doc)
		result.Address = extractAddress(doc.Text())

		result.Status = "success"
		return result
	}

	return result
}

func extractText(doc *goquery.Document) string {
	doc.Find("script, style, nav, header, footer").Each(func(i int, s *goquery.Selection) {
		s.Remove()
	})
	text := doc.Find("body").Text()
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

var phoneRegex = regexp.MustCompile(`(?:\+7|8|7)[\s\-]?\(?[0-9]{3}\)?[\s\-]?[0-9]{3}[\s\-]?[0-9]{2}[\s\-]?[0-9]{2}`)

func extractPhones(text string) string {
	phones := phoneRegex.FindAllString(text, -1)
	unique := uniqueStrings(phones)
	return strings.Join(unique, ", ")
}

var emailRegex = regexp.MustCompile(`[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}`)

func extractEmails(text string) string {
	emails := emailRegex.FindAllString(text, -1)
	unique := uniqueStrings(emails)
	return strings.Join(unique, ", ")
}

var innRegex = regexp.MustCompile(`\b[0-9]{10,12}\b`)

func extractINN(text string) string {
	inns := innRegex.FindAllString(text, -1)
	for _, inn := range inns {
		if len(inn) >= 10 && len(inn) <= 12 {
			return inn
		}
	}
	return ""
}

func extractSocialMedia(doc *goquery.Document) string {
	var socials []string
	sel := doc.Find("a[href]")
	sel.Each(func(i int, s *goquery.Selection) {
		href, _ := s.Attr("href")
		if strings.Contains(href, "vk.com") || strings.Contains(href, "t.me") ||
			strings.Contains(href, "facebook.com") || strings.Contains(href, "instagram.com") ||
			strings.Contains(href, "twitter.com") || strings.Contains(href, "youtube.com") {
			socials = append(socials, href)
		}
	})
	return strings.Join(uniqueStrings(socials), ", ")
}

var addressKeywords = []string{"адрес:", "г.", "г.,", "ул.", "улица", "дом", "офис"}

func extractAddress(text string) string {
	lines := strings.Split(text, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		found := 0
		for _, kw := range addressKeywords {
			if strings.Contains(lower, kw) {
				found++
			}
		}
		if found >= 2 {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func uniqueStrings(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item != "" && !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}
