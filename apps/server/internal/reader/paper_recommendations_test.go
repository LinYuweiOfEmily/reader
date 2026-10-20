package reader

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"reader.local/server/internal/scholar"
)

func TestPaperRecommendationPreferences(t *testing.T) {
	s, _, _ := translationFixture(t)
	response := request(t, s, "GET", "/api/papers/recommendations", nil)
	if response.Code != 200 || !strings.Contains(response.Body.String(), "mismatch") {
		t.Fatal(response.Code, response.Body.String())
	}
	p := defaultPaperRecommendationPreferences()
	p.Interests = "RLVR systems"
	response = request(t, s, "PUT", "/api/papers/recommendations/preferences", strings.NewReader(jsonText(p)))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	got, err := s.paperRecommendationPreferences(context.Background())
	if err != nil || got.Interests != p.Interests {
		t.Fatal(got, err)
	}
	p.Count = 100
	if response = request(t, s, "PUT", "/api/papers/recommendations/preferences", strings.NewReader(jsonText(p))); response.Code != 400 {
		t.Fatal("invalid count accepted")
	}
	got, _ = s.paperRecommendationPreferences(context.Background())
	if got.Count != 5 {
		t.Fatal("invalid preference overwrote saved value")
	}
}

func TestPaperRecommendationDedupAndReadFilter(t *testing.T) {
	items := []scholar.Metadata{
		{Title: "Already read", ArXiv: "2601.12345v2", URL: "https://arxiv.org/abs/2601.12345v2"},
		{Title: "New paper", ArXiv: "2602.12345v1", URL: "https://arxiv.org/abs/2602.12345v1", Abstract: "abstract"},
		{Title: "New paper", DOI: "10.1000/new", URL: "https://doi.org/10.1000/new"},
	}
	docs := []Document{{ID: "read", Title: "Already read", ReadingStatus: "done", Metadata: PaperMetadata{ArXiv: "2601.12345v1"}}, {ID: "added", Title: "New paper", Metadata: PaperMetadata{DOI: "10.1000/new"}}}
	history := []paperRecommendationRun{{Papers: []recommendedPaper{{Title: "New paper", ArXiv: "2602.12345"}}}}
	got := paperRecommendationCandidates(items, docs, history)
	if len(got) != 1 || got[0].DocumentID != "added" || !got[0].PreviousRecommendation || got[0].DOI != "10.1000/new" {
		t.Fatalf("%+v", got)
	}
}

func TestPaperRecommendationSkipsUnidentifiableMetadata(t *testing.T) {
	got := paperRecommendationCandidates([]scholar.Metadata{{Title: "∞", URL: "https://example.org/paper"}}, nil, nil)
	if len(got) != 0 {
		t.Fatal("unidentifiable metadata should be skipped")
	}
}

func TestPaperRecommendationHistoryRefreshesReadState(t *testing.T) {
	history := []paperRecommendationRun{{Papers: []recommendedPaper{{Title: "Paper", ArXiv: "2601.12345v1", DocumentID: "deleted"}}}}
	docs := []Document{{ID: "current", Title: "Paper", ReadingStatus: "done", Metadata: PaperMetadata{ArXiv: "2601.12345v2"}}}
	refreshRecommendationDocuments(history, docs)
	p := history[0].Papers[0]
	if p.DocumentID != "current" || !p.Read {
		t.Fatalf("stale history state %+v", p)
	}
	refreshRecommendationDocuments(history, nil)
	if history[0].Papers[0].Read || history[0].Papers[0].DocumentID != "" {
		t.Fatal("removed document left stale state")
	}
}

func TestPaperRecommendationRejectsInventedAndMalformedResults(t *testing.T) {
	candidates := []recommendedPaper{{ID: "a", Title: "Verified title", URL: "https://arxiv.org/abs/2601.12345"}}
	valid := `{"papers":[{"id":"a","priority":"high","scores":{"relevance":9,"inspiration":8,"evidence":7,"novelty":6},"reason":"与课题相关","limitation":"仅摘要筛选"}]}`
	got, err := parsePaperRecommendations(valid, candidates, 1)
	if err != nil || len(got) != 1 || got[0].Title != "Verified title" || got[0].Score != 7.9 {
		t.Fatal(got, err)
	}
	for _, bad := range []string{
		strings.Replace(valid, `"id":"a"`, `"id":"invented"`, 1),
		strings.Replace(valid, `"relevance":9`, `"relevance":90`, 1),
		strings.Replace(valid, `"evidence":7,`, "", 1),
		strings.Replace(valid, `"priority":"high"`, `"priority":"urgent"`, 1),
		valid + `{}`,
		`{"papers":[]}`,
	} {
		if _, err := parsePaperRecommendations(bad, candidates, 1); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestPaperRecommendationHistoryBoundAndCanceledSave(t *testing.T) {
	s, _, _ := translationFixture(t)
	for i := 0; i < 32; i++ {
		if err := s.savePaperRecommendationRun(context.Background(), paperRecommendationRun{ID: fmt.Sprint(i), Papers: []recommendedPaper{}}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := s.paperRecommendationHistory(context.Background())
	if err != nil || len(history) != 30 || history[0].ID != "31" || history[29].ID != "2" {
		t.Fatal(history, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.savePaperRecommendationRun(ctx, paperRecommendationRun{ID: "canceled"}); err == nil {
		t.Fatal("canceled task persisted")
	}
	history, _ = s.paperRecommendationHistory(context.Background())
	b, _ := json.Marshal(history)
	if strings.Contains(string(b), "canceled") {
		t.Fatal("partial run stored")
	}
}

func TestPaperRecommendationHTTPVerifiedMetadataAndFailure(t *testing.T) {
	s := testServer(t)
	hits := scholarFixture(t, s)
	malformed := false
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		_, raw, ok := strings.Cut(body.Messages[len(body.Messages)-1].Content, "输入资料：\n")
		if !ok {
			t.Error("no candidate input")
			return
		}
		var input struct {
			Candidates []recommendedPaper `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(raw), &input); err != nil || len(input.Candidates) != 1 {
			t.Error("wrong candidates", err)
			return
		}
		if malformed {
			writeAPIReply(w, `{"papers":[{"id":"fabricated"}]}`)
			return
		}
		writeAPIReply(w, fmt.Sprintf(`{"papers":[{"id":%q,"priority":"high","scores":{"relevance":9,"inspiration":8,"evidence":7,"novelty":6},"reason":"方法适合当前课题","limitation":"仅核验摘要"}]}`, input.Candidates[0].ID))
	}))
	defer provider.Close()
	configureTranslationTest(t, s, provider.URL)
	response := request(t, s, "POST", "/api/papers/recommendations", strings.NewReader(`{}`))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var run paperRecommendationRun
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil || len(run.Papers) != 1 || run.Papers[0].Title != "Attention Is All You Need" || run.Provider != "text-api" {
		t.Fatal(run, err)
	}
	if hits.Load() != 1 {
		t.Fatal("recommendations must not download PDF")
	}
	malformed = true
	response = request(t, s, "POST", "/api/papers/recommendations", strings.NewReader(`{}`))
	if response.Code != 502 {
		t.Fatal(response.Code, response.Body.String())
	}
	history, _ := s.paperRecommendationHistory(context.Background())
	if len(history) != 1 {
		t.Fatal("failed run replaced history")
	}
	s.recommendationMu.Lock()
	response = request(t, s, "POST", "/api/papers/recommendations", strings.NewReader(`{}`))
	s.recommendationMu.Unlock()
	if response.Code != 409 {
		t.Fatal("concurrent run accepted")
	}
}

func TestPaperRecommendationHTTPCancelDoesNotPersist(t *testing.T) {
	s := testServer(t)
	scholarFixture(t, s)
	started := make(chan struct{})
	release := make(chan struct{})
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer provider.Close()
	defer close(release)
	configureTranslationTest(t, s, provider.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "http://127.0.0.1:17840/api/papers/recommendations", strings.NewReader(`{}`)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer test-secret")
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); s.Handler().ServeHTTP(response, req) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("provider not reached")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not terminate recommendation")
	}
	if response.Code != 499 {
		t.Fatal(response.Code, response.Body.String())
	}
	history, _ := s.paperRecommendationHistory(context.Background())
	if len(history) != 0 {
		t.Fatal("canceled recommendation saved")
	}
	if !s.recommendationMu.TryLock() {
		t.Fatal("canceled task retained lock")
	}
	s.recommendationMu.Unlock()
}

// Opt-in: external scholarly searches and one paid ZCode model call. Never
// opens or changes the user's Reader library/configuration.
func TestPaperRecommendationLive(t *testing.T) {
	if os.Getenv("READER_TEST_PAPER_RECOMMENDATION_LIVE") != "1" {
		t.Skip("external search and model call")
	}
	s := testServer(t)
	model := zcodeModelID(zcodeModelRef{ProviderID: "zai-api", ModelID: "GLM-5.3-Flash"})
	config := AIConfig{Primary: "zcode", Models: map[string]string{"zcode": model}, Efforts: map[string]map[string]string{"zcode": {model: "low"}}, Capabilities: map[string]Capability{}}
	config.Capabilities["zcode"] = Capability{Text: true, Fingerprint: configPrint(config, "zcode")}
	if err := s.writeAIConfig(config); err != nil {
		t.Fatal(err)
	}
	response := request(t, s, "POST", "/api/papers/recommendations", strings.NewReader(`{}`))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var run paperRecommendationRun
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil || len(run.Papers) == 0 {
		t.Fatal(err)
	}
	t.Logf("completed %d recommendations via %s; %d source warnings", len(run.Papers), run.Provider, len(run.Warnings))
	for _, p := range run.Papers {
		t.Logf("%.1f %s %s", p.Score, p.Title, p.URL)
	}
}
