package reader

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

func (s *Server) getPaperRecommendations(w http.ResponseWriter, r *http.Request) {
	p, err := s.paperRecommendationPreferences(r.Context())
	if err != nil {
		fail(w, 500, "无法读取推荐偏好")
		return
	}
	history, err := s.paperRecommendationHistory(r.Context())
	if err != nil {
		fail(w, 500, "无法读取推荐历史")
		return
	}
	docs, err := s.Store.Documents()
	if err != nil {
		fail(w, 500, "无法读取书库")
		return
	}
	refreshRecommendationDocuments(history, docs)
	respond(w, 200, map[string]any{"preferences": p, "history": history})
}
func refreshRecommendationDocuments(history []paperRecommendationRun, docs []Document) {
	existing := map[string]string{}
	read := map[string]bool{}
	for _, d := range docs {
		if d.DeletedAt == "" {
			for _, k := range recommendationKeys(d.Title, d.Metadata.DOI, d.Metadata.ArXiv) {
				existing[k] = d.ID
				read[k] = read[k] || d.ReadingStatus == "done"
			}
		}
	}
	for i := range history {
		for j := range history[i].Papers {
			p := &history[i].Papers[j]
			p.DocumentID = ""
			p.Read = false
			for _, k := range recommendationKeys(p.Title, p.DOI, p.ArXiv) {
				p.Read = p.Read || read[k]
				if doc := existing[k]; doc != "" {
					p.DocumentID = doc
				}
			}
		}
	}
}
func (s *Server) putPaperRecommendationPreferences(w http.ResponseWriter, r *http.Request) {
	var p paperRecommendationPreferences
	if !decode(w, r, &p) {
		return
	}
	if err := validatePaperRecommendationPreferences(&p); err != nil {
		fail(w, 400, err.Error())
		return
	}
	b, _ := json.Marshal(p)
	if _, err := s.Store.DB.ExecContext(r.Context(), "INSERT INTO settings(key,value) VALUES('papers:recommendation-preferences',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(b)); err != nil {
		fail(w, 500, "推荐偏好保存失败")
		return
	}
	respond(w, 200, p)
}
func (s *Server) recommendPapers(w http.ResponseWriter, r *http.Request) {
	var body struct{}
	if !decode(w, r, &body) {
		return
	}
	if !s.recommendationMu.TryLock() {
		fail(w, 409, "已有推荐任务正在进行，请等待完成或取消")
		return
	}
	defer s.recommendationMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	p, err := s.paperRecommendationPreferences(ctx)
	if err != nil {
		fail(w, 500, "无法读取推荐偏好")
		return
	}
	service := s.generationService(s.aiConfig())
	if err = service.ValidateInput(AIInput{}, false); err != nil {
		fail(w, 400, err.Error())
		return
	}
	history, err := s.paperRecommendationHistory(ctx)
	if err != nil {
		fail(w, 500, "无法读取推荐历史")
		return
	}
	items, warnings, err := s.scholarClient().SearchRecommendations(ctx, p.Queries, 24)
	if err != nil {
		recommendationFailure(w, ctx, "论文检索失败："+err.Error())
		return
	}
	if warnings == nil {
		warnings = []string{}
	}
	docs, err := s.Store.Documents()
	if err != nil {
		fail(w, 500, "无法读取书库")
		return
	}
	candidates := paperRecommendationCandidates(items, docs, history)
	if len(candidates) == 0 {
		fail(w, 422, "检索结果中没有未读候选，请调整检索词后重试")
		return
	}
	if len(candidates) < p.Count {
		warnings = append(warnings, "未读候选不足，已按实际检索数量推荐")
	}
	in := AIInput{Prompt: paperRecommendationPrompt(p, candidates)}
	// A single model call ranks this verified candidate set. No conversation is
	// kept alive between days; only validated metadata/results are persisted.
	result, err := service.Generate(ctx, in, false, nil)
	if err != nil {
		recommendationFailure(w, ctx, "AI 筛选失败："+err.Error())
		return
	}
	papers, err := parsePaperRecommendations(result.Text, candidates, p.Count)
	if err != nil {
		fail(w, 502, err.Error())
		return
	}
	// The library may have changed while the provider was generating.
	docs, err = s.Store.Documents()
	if err != nil {
		fail(w, 500, "无法读取书库")
		return
	}
	read := map[string]bool{}
	for _, d := range docs {
		if d.ReadingStatus == "done" {
			for _, k := range recommendationKeys(d.Title, d.Metadata.DOI, d.Metadata.ArXiv) {
				read[k] = true
			}
		}
	}
	kept := make([]recommendedPaper, 0, len(papers))
	for _, paper := range papers {
		done := false
		for _, k := range recommendationKeys(paper.Title, paper.DOI, paper.ArXiv) {
			done = done || read[k]
		}
		if !done {
			kept = append(kept, paper)
		}
	}
	if len(kept) == 0 {
		fail(w, 422, "本轮候选已标记为已读，请重新推荐")
		return
	}
	if len(kept) != len(papers) {
		warnings = append(warnings, "已移除生成期间标记为已读的论文")
	}
	if result.Fallback {
		warnings = append(warnings, "主 Agent 不可用，本轮使用备用 API 完成筛选")
	}
	run := paperRecommendationRun{ID: id(), CreatedAt: now(), Provider: result.Provider, Preferences: p, Warnings: warnings, Papers: kept}
	runs := []paperRecommendationRun{run}
	refreshRecommendationDocuments(runs, docs)
	run = runs[0]
	if err = s.savePaperRecommendationRun(ctx, run); err != nil {
		recommendationFailure(w, ctx, "推荐结果保存失败")
		return
	}
	respond(w, 200, run)
}
func recommendationFailure(w http.ResponseWriter, ctx context.Context, message string) {
	if errors.Is(ctx.Err(), context.Canceled) {
		fail(w, 499, "推荐已取消")
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fail(w, 504, "推荐超时，请缩小检索范围或检查 AI 连接")
		return
	}
	fail(w, 502, message)
}
