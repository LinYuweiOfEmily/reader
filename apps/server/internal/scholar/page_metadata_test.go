package scholar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPageStructuredFallbacks(t *testing.T) {
	tags, _ := pageMeta([]byte(`<meta name="dc.creator" content="Ada Lovelace"><meta name="dcterms.abstract" content="DC abstract"><script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"JSON title","datePublished":"2024-2-3","author":{"name":"Wrong Author"}}</script>`))
	m := pageMetadata(tags, "https://example.org/paper")
	if m.Title != "JSON title" || m.Abstract != "DC abstract" || m.Date != "2024-02-03" || len(m.Creators) != 1 || m.Creators[0].Family != "Lovelace" {
		t.Fatalf("fallbacks: %+v", m)
	}
	tags, _ = pageMeta([]byte(`<meta name="citation_title" content="Correct"><meta name="citation_author" content="Correct Author"><script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"Wrong","author":{"name":"Wrong Author"}}</script>`))
	m = pageMetadata(tags, "https://example.org/paper")
	if m.Title != "Correct" || len(m.Creators) != 1 || m.Creators[0].Given != "Correct" {
		t.Fatalf("citation priority: %+v", m)
	}
}

func TestPageJSONLDRejectsUnrelatedArticles(t *testing.T) {
	for _, body := range []string{
		`{"@type":"ItemList","itemListElement":[{"@type":"ScholarlyArticle","headline":"Unrelated"}]}`,
		`{"@type":"WebPage","citation":{"@type":"ScholarlyArticle","headline":"Reference"}}`,
		`[{"@type":"ScholarlyArticle","headline":"One"},{"@type":"ScholarlyArticle","headline":"Two"}]`,
	} {
		tags, _ := pageMeta([]byte(`<script type="application/ld+json">` + body + `</script>`))
		if m := pageMetadata(tags, "https://example.org/paper"); m.Title != "" {
			t.Fatalf("unrelated article selected: %+v", m)
		}
	}
}

func TestPagePDFCandidates(t *testing.T) {
	var hits []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/page":
			w.Write([]byte(`<meta name="citation_pdf_url" content="/missing"><meta name="citation_pdf_url" content="/missing#same"><link rel="alternate" type="application/pdf" href="/download"><a href="/unrelated.pdf">Other</a>`))
		case "/download":
			w.Write([]byte(pdfBytes))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := New(1 << 20)
	c.AllowPrivateHosts = true
	r, err := c.pageResult(context.Background(), s.URL+"/page")
	if err != nil || len(r.PDF) == 0 {
		t.Fatalf("candidate fallback: %v", err)
	}
	if strings.Join(hits, ",") != "/page,/missing,/download" {
		t.Fatalf("requests %v", hits)
	}
}

func TestPagePDFErrorAndBound(t *testing.T) {
	var hits int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" {
			w.Write([]byte(`<meta name="citation_pdf_url" content="/a"><meta name="citation_pdf_url" content="/b"><meta name="citation_pdf_url" content="/c"><meta name="citation_pdf_url" content="/d"><a href="/unrelated.pdf">Other</a>`))
			return
		}
		hits++
		http.Error(w, "denied", http.StatusForbidden)
	}))
	defer s.Close()
	c := New(1 << 20)
	c.AllowPrivateHosts = true
	_, err := c.pageResult(context.Background(), s.URL+"/page")
	if hits != 3 || err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("hits=%d err=%v", hits, err)
	}
}

func TestPagePDFCancellationStopsCandidates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hits := 0
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" {
			w.Write([]byte(`<meta name="citation_pdf_url" content="/a"><meta name="citation_pdf_url" content="/b">`))
			return
		}
		hits++
		cancel()
		<-r.Context().Done()
	}))
	defer s.Close()
	c := New(1 << 20)
	c.AllowPrivateHosts = true
	_, err := c.pageResult(ctx, s.URL+"/page")
	if !errors.Is(err, context.Canceled) || hits != 1 {
		t.Fatalf("hits=%d err=%v", hits, err)
	}
}

func TestPagePDFNoUnrelatedFallback(t *testing.T) {
	candidates := pagePDFCandidates(map[string][]string{"citation_pdf_url": {"/main"}}, []string{"/reference.pdf"}, "https://example.org/article")
	if strings.Join(candidates, ",") != "https://example.org/main" {
		t.Fatalf("candidates %v", candidates)
	}
	c := New(1 << 20)
	tags := map[string][]string{"citation_pdf_url": {"http://127.0.0.1/private"}}
	candidates = pagePDFCandidates(tags, nil, "https://example.org/article")
	_, err := c.pdf(context.Background(), candidates[0])
	if err == nil || !strings.Contains(err.Error(), "本机") {
		t.Fatalf("private URL accepted: %v", err)
	}
}

func TestPageJSONLDBoundedAndGraph(t *testing.T) {
	tags, _ := pageMeta([]byte(`<script type="application/ld+json">{"@graph":[{"@type":"Organization","name":"Not article"},{"@type":"ScholarlyArticle","headline":"Paper","encoding":{"encodingFormat":"application/pdf","contentUrl":"/download"}}]}</script>`))
	if pageMetadata(tags, "").Title != "Paper" || len(pagePDFCandidates(tags, nil, "https://example.org/paper")) != 1 {
		t.Fatalf("graph: %v", tags)
	}
	tags, _ = pageMeta([]byte(`<script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"` + strings.Repeat("x", 256<<10) + `"}</script>`))
	if pageMetadata(tags, "").Title != "" {
		t.Fatal("oversized JSON parsed")
	}
	tags, _ = pageMeta([]byte(`<script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"First"}</script><script type="application/ld+json">` + strings.Repeat(" ", 256<<10) + `{}</script>`))
	if pageMetadata(tags, "").Title != "" {
		t.Fatal("incomplete JSON-LD scan selected first article")
	}
}

func TestPageJSONLDRejectsTruncatedTraversal(t *testing.T) {
	filler := strings.Repeat(`{"@type":"Organization"},`, 130)
	page := `<script type="application/ld+json">{"@graph":[{"@type":"ScholarlyArticle","headline":"First"},` + filler + `{"@type":"ScholarlyArticle","headline":"Second"}]}</script>`
	tags, _ := pageMeta([]byte(page))
	if pageMetadata(tags, "").Title != "" {
		t.Fatal("truncated traversal selected first paper")
	}
}

func TestPageDOINormalization(t *testing.T) {
	for _, value := range []string{"https://doi.org/10.1234/paper", "DOI:10.1234/paper", "10.1234/paper"} {
		tags, _ := pageMeta([]byte(`<meta name="citation_doi" content="` + value + `">`))
		if got := pageMetadata(tags, "").DOI; got != "10.1234/paper" {
			t.Errorf("%q became %q", value, got)
		}
	}
	tags, _ := pageMeta([]byte(`<script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"Paper","identifier":{"@type":"PropertyValue","propertyID":"DOI","value":"https://doi.org/10.1234/json"}}</script>`))
	if pageMetadata(tags, "").DOI != "10.1234/json" {
		t.Fatal("missing JSON-LD DOI")
	}
}

func TestPagePDFDeniedStillUsesArxiv(t *testing.T) {
	var hits []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/page":
			fmt.Fprint(w, `<meta name="citation_pdf_url" content="/denied"><meta name="citation_arxiv_id" content="1706.03762">`)
		case "/denied":
			w.WriteHeader(403)
		case "/api":
			fmt.Fprint(w, `<feed><entry><title>Paper</title></entry></feed>`)
		case "/pdf/1706.03762":
			w.Write([]byte(pdfBytes))
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	c := New(1 << 20)
	c.AllowPrivateHosts = true
	c.ArXivAPI = s.URL + "/api"
	c.ArXivPDF = s.URL + "/pdf/"
	result, err := c.pageResult(context.Background(), s.URL+"/page")
	if err != nil || len(result.PDF) == 0 {
		t.Fatalf("fallback blocked: %v requests=%v", err, hits)
	}
}

func TestPageJSONLDIncompleteScriptCannotSelectAnother(t *testing.T) {
	filler := strings.Repeat(`{"@type":"Organization"},`, 130)
	tags, _ := pageMeta([]byte(`<script type="application/ld+json">{"@graph":[` + filler + `{"@type":"ScholarlyArticle","headline":"Hidden"}]}</script><script type="application/ld+json">{"@type":"ScholarlyArticle","headline":"Other"}</script>`))
	if pageMetadata(tags, "").Title != "" {
		t.Fatal("incomplete earlier script allowed another article")
	}
}
