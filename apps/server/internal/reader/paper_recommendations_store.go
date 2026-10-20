package reader

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"unicode/utf8"
)

type paperRecommendationPreferences struct {
	Interests string   `json:"interests"`
	Queries   []string `json:"queries"`
	Count     int      `json:"count"`
}
type paperRecommendationScores struct {
	Relevance   float64 `json:"relevance"`
	Inspiration float64 `json:"inspiration"`
	Evidence    float64 `json:"evidence"`
	Novelty     float64 `json:"novelty"`
}
type recommendedPaper struct {
	ID                     string                    `json:"id"`
	Title                  string                    `json:"title"`
	Authors                []string                  `json:"authors"`
	Date                   string                    `json:"date"`
	Status                 string                    `json:"status"`
	URL                    string                    `json:"url"`
	DOI                    string                    `json:"doi"`
	ArXiv                  string                    `json:"arxiv"`
	Abstract               string                    `json:"abstract"`
	Priority               string                    `json:"priority"`
	Score                  float64                   `json:"score"`
	Scores                 paperRecommendationScores `json:"scores"`
	Reason                 string                    `json:"reason"`
	Limitation             string                    `json:"limitation"`
	PreviousRecommendation bool                      `json:"previousRecommendation"`
	DocumentID             string                    `json:"documentId,omitempty"`
	Read                   bool                      `json:"read"`
}
type paperRecommendationRun struct {
	ID          string                         `json:"id"`
	CreatedAt   string                         `json:"createdAt"`
	Provider    string                         `json:"provider"`
	Preferences paperRecommendationPreferences `json:"preferences"`
	Warnings    []string                       `json:"warnings"`
	Papers      []recommendedPaper             `json:"papers"`
}

func defaultPaperRecommendationPreferences() paperRecommendationPreferences {
	return paperRecommendationPreferences{
		Interests: "研究大模型 RL/RLHF/RLVR。主线一：低比特训练与推理/采样 mismatch、量化误差与概率偏移修正；主线二：RL 加速、rollout、低精度算子、权重同步与异步训练。已有领域基础，重视方法启发与实验依据，接受预印本及经典论文。",
		Queries:   []string{"quantized rollout training mismatch", "low precision reinforcement learning language models", "asynchronous RLVR training acceleration"},
		Count:     5,
	}
}
func validatePaperRecommendationPreferences(p *paperRecommendationPreferences) error {
	p.Interests = strings.TrimSpace(p.Interests)
	if p.Interests == "" || utf8.RuneCountInString(p.Interests) > 3000 || p.Count < 3 || p.Count > 5 || len(p.Queries) < 1 || len(p.Queries) > 6 {
		return errors.New("请填写研究方向、1～6 个英文检索词组，并选择 3～5 篇；研究方向最多 3000 字")
	}
	for i, q := range p.Queries {
		q = strings.TrimSpace(q)
		if q == "" || utf8.RuneCountInString(q) > 160 {
			return errors.New("每个检索词组需为 1～160 字")
		}
		p.Queries[i] = q
	}
	return nil
}
func (s *Server) paperRecommendationPreferences(ctx context.Context) (paperRecommendationPreferences, error) {
	p := defaultPaperRecommendationPreferences()
	var raw string
	err := s.Store.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='papers:recommendation-preferences'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	err = json.Unmarshal([]byte(raw), &p)
	if err == nil {
		err = validatePaperRecommendationPreferences(&p)
	}
	return p, err
}
func (s *Server) paperRecommendationHistory(ctx context.Context) ([]paperRecommendationRun, error) {
	history := []paperRecommendationRun{}
	var raw string
	err := s.Store.DB.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='papers:recommendation-history'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return history, nil
	}
	if err != nil {
		return nil, err
	}
	err = json.Unmarshal([]byte(raw), &history)
	if history == nil {
		history = []paperRecommendationRun{}
	}
	return history, err
}
func (s *Server) savePaperRecommendationRun(ctx context.Context, run paperRecommendationRun) error {
	// The handler owns recommendationMu. Keep the read/replace atomic as well,
	// and use the request context so canceled jobs never commit partial output.
	tx, err := s.Store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	history := []paperRecommendationRun{}
	err = tx.QueryRowContext(ctx, "SELECT value FROM settings WHERE key='papers:recommendation-history'").Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if raw != "" {
		if err = json.Unmarshal([]byte(raw), &history); err != nil {
			return err
		}
	}
	history = append([]paperRecommendationRun{run}, history...)
	if len(history) > 30 {
		history = history[:30]
	}
	b, err := json.Marshal(history)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('papers:recommendation-history',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(b))
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit()
}
