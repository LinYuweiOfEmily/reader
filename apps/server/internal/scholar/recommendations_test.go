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

func recommendationClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c := New(1024)
	c.HTTP = s.Client()
	c.AllowPrivateHosts = true
	c.ArXivAPI = s.URL + "/arxiv"
	c.SemanticScholar = s.URL + "/s2/"
	return c
}

func TestRecommendationsArxivMetadataAndDedup(t *testing.T) {
	c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/arxiv" {
			t.Errorf("unexpected request %s", r.URL)
		}
		if r.URL.Query().Get("sortBy") != "relevance" || strings.Contains(r.URL.Query().Get("search_query"), "submittedDate:") || !strings.Contains(r.URL.Query().Get("search_query"), `all:"reinforcement" AND all:"learning"`) {
			t.Error("search must match keywords by relevance without excluding classic papers")
		}
		entry := strings.TrimSuffix(strings.SplitN(atom, "<entry>", 2)[1], "</feed>")
		fmt.Fprint(w, `<feed xmlns="http://www.w3.org/2005/Atom" xmlns:arxiv="http://arxiv.org/schemas/atom"><entry>`+entry+"<entry>"+strings.ReplaceAll(entry, "v7", "v8")+"</feed>")
	})
	papers, warnings, err := c.SearchRecommendations(context.Background(), []string{"reinforcement learning", "reinforcement learning"}, 20)
	if err != nil || len(papers) != 1 || len(warnings) != 0 {
		t.Fatalf("got %v %v %v", papers, warnings, err)
	}
	p := papers[0]
	if p.ArXiv != "1706.03762v7" || p.URL != "https://arxiv.org/abs/1706.03762v7" || p.Date != "2017-06-12" || len(p.Creators) != 2 || p.Abstract == "" {
		t.Fatalf("metadata incomplete: %+v", p)
	}
}

func TestRecommendationsFallbackAndPublicURLs(t *testing.T) {
	c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/arxiv" {
			http.Error(w, "unavailable", 503)
			return
		}
		if r.URL.Path != "/s2/search" {
			t.Errorf("unexpected request: %s", r.URL)
		}
		fmt.Fprint(w, `{"data":[
			{"title":"A","abstract":"Evidence","externalIds":{"DOI":"https://doi.org/10.1234/ABC"},"year":2026,"authors":[{"name":"A B"}]},
			{"title":"A duplicate","externalIds":{"DOI":"10.1234/abc"}},
			{"title":"Private","url":"http://127.0.0.1/secret"},
			{"title":"Credentials","url":"https://user:pass@example.org/paper"},
			{"title":"Public","url":"https://www.semanticscholar.org/paper/abc","publicationTypes":["Conference"]}
		]}`)
	})
	papers, warnings, err := c.SearchRecommendations(context.Background(), []string{"RL"}, 10)
	if err != nil || len(papers) != 2 || len(warnings) == 0 {
		t.Fatalf("got %v %v %v", papers, warnings, err)
	}
	if papers[0].DOI != "10.1234/abc" || papers[0].URL != "https://doi.org/10.1234/abc" || papers[1].ItemType != "conference" {
		t.Fatalf("unexpected metadata: %+v", papers)
	}
}

func TestRecommendationsInvalidResponses(t *testing.T) {
	for _, body := range []string{"<feed>", "<html></html>", `<feed><entry><id>https://evil.test/abs/2601.12345</id><title>Forged</title></entry></feed>`, `<feed><entry><id>http://arxiv.org/api/errors</id><title>Error</title></entry></feed>`} {
		t.Run(body, func(t *testing.T) {
			c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) })
			p, warnings, err := c.SearchRecommendations(context.Background(), []string{"RL"}, 5)
			if err == nil || len(p) != 0 || len(warnings) == 0 {
				t.Fatalf("got %v %v %v", p, warnings, err)
			}
		})
	}
}

func TestRecommendationsValidationAndCancellation(t *testing.T) {
	c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) { t.Error("should not request") })
	for _, tc := range []struct {
		queries []string
		limit   int
	}{
		{nil, 5}, {[]string{" "}, 5}, {[]string{strings.Repeat("a", 301)}, 5}, {[]string{"RL"}, 0}, {[]string{"RL"}, 61}, {make([]string, 9), 5},
	} {
		if _, _, err := c.SearchRecommendations(context.Background(), tc.queries, tc.limit); err == nil {
			t.Errorf("accepted invalid input %+v", tc)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.SearchRecommendations(ctx, []string{"RL"}, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestRecommendationsCancellationDuringRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) { cancel(); <-r.Context().Done() })
	if _, _, err := c.SearchRecommendations(ctx, []string{"RL"}, 5); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestRecommendationsLimitAndEmptySourceFallback(t *testing.T) {
	c := recommendationClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/arxiv" {
			if r.URL.Query().Get("max_results") != "1" {
				t.Error("arxiv request must be bounded")
			}
			fmt.Fprint(w, "<feed></feed>")
			return
		}
		if r.URL.Query().Get("limit") != "1" {
			t.Error("fallback request must be bounded")
		}
		fmt.Fprint(w, `{"data":[{"title":"One","externalIds":{"ArXiv":"2601.12345v2"}},{"title":"Two","externalIds":{"ArXiv":"2601.12346"}}]}`)
	})
	papers, warnings, err := c.SearchRecommendations(context.Background(), []string{"RL"}, 1)
	if err != nil || len(papers) != 1 || papers[0].ArXiv != "2601.12345v2" || papers[0].URL != "https://arxiv.org/abs/2601.12345v2" || len(warnings) == 0 {
		t.Fatalf("got %v %v %v", papers, warnings, err)
	}
}
