package scholar

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// SearchRecommendations retrieves bibliographic candidates, never full text.
// One combined arXiv request keeps its API load small. A separate service can
// still supply candidates when the arXiv export endpoint is unavailable.
func (c *Client) SearchRecommendations(ctx context.Context, queries []string, limit int) ([]Metadata, []string, error) {
	if len(queries) < 1 || len(queries) > 8 || limit < 1 || limit > 60 {
		return nil, nil, Error{"检索需要 1–8 条查询，候选数量须为 1–60"}
	}
	unique := []string{}
	seenQueries := map[string]bool{}
	for _, query := range queries {
		query = clean(query)
		if query == "" || len([]rune(query)) > 300 {
			return nil, nil, Error{"每条检索词须为 1–300 个字符"}
		}
		if !seenQueries[strings.ToLower(query)] {
			unique = append(unique, query)
			seenQueries[strings.ToLower(query)] = true
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	var warnings []string
	result := []Metadata{}
	seen := map[string]bool{}
	add := func(items []Metadata) {
		for _, m := range items {
			m, ok := recommendationMetadata(m)
			if !ok {
				continue
			}
			keys := []string{"url:" + m.URL}
			if m.ArXiv != "" {
				keys = append(keys, "arxiv:"+bareArXiv(m.ArXiv))
			}
			if m.DOI != "" {
				keys = append(keys, "doi:"+m.DOI)
			}
			duplicate := false
			for _, key := range keys {
				duplicate = duplicate || seen[key]
			}
			for _, key := range keys {
				seen[key] = true
			}
			if !duplicate && len(result) < limit {
				result = append(result, m)
			}
		}
	}
	items, err := c.recommendationsArxiv(ctx, unique, limit)
	if ctx.Err() != nil {
		return nil, warnings, ctx.Err()
	}
	if err != nil {
		warnings = append(warnings, "arXiv 检索失败："+err.Error())
	} else {
		add(items)
	}
	if len(result) > 0 {
		return result, warnings, nil
	}
	if err == nil {
		warnings = append(warnings, "arXiv 未返回可用候选，已尝试备用来源")
	}
	for i, query := range unique {
		// No pagination or automatic retries, including for rate-limit errors.
		if i > 0 {
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, warnings, ctx.Err()
			case <-timer.C:
			}
		}
		items, err = c.recommendationsS2(ctx, query, limit)
		if ctx.Err() != nil {
			return nil, warnings, ctx.Err()
		}
		if err != nil {
			warnings = append(warnings, "Semantic Scholar 检索失败："+err.Error())
			break
		}
		add(items)
		if len(result) >= limit {
			break
		}
	}
	if len(result) == 0 {
		message := "没有检索到可用论文，请调整关键词或稍后重试"
		if len(warnings) > 0 {
			message = strings.Join(warnings, "；") + "；" + message
		}
		return nil, warnings, Error{message}
	}
	return result, warnings, nil
}

func (c *Client) recommendationsArxiv(ctx context.Context, queries []string, limit int) ([]Metadata, error) {
	parts := make([]string, len(queries))
	for i, q := range queries {
		// Ordinary keyword lines match all words, without requiring an exact
		// phrase. Explicit field queries retain arXiv's advanced syntax.
		if !strings.Contains(q, ":") {
			terms := strings.Fields(strings.ReplaceAll(q, `"`, ""))
			for j, term := range terms {
				terms[j] = `all:"` + term + `"`
			}
			q = strings.Join(terms, " AND ")
		}
		parts[i] = "(" + q + ")"
	}
	params := url.Values{
		"search_query": {strings.Join(parts, " OR ")},
		"start":        {"0"}, "max_results": {strconv.Itoa(limit)}, "sortBy": {"relevance"}, "sortOrder": {"descending"},
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	data, _, _, err := c.get(ctx, c.ArXivAPI+"?"+params.Encode(), "application/atom+xml", 4<<20)
	if err != nil {
		return nil, err
	}
	var feed struct {
		XMLName xml.Name `xml:"feed"`
		Entries []struct {
			ID        string `xml:"id"`
			Title     string `xml:"title"`
			Summary   string `xml:"summary"`
			Published string `xml:"published"`
			DOI       string `xml:"http://arxiv.org/schemas/atom doi"`
			Authors   []struct {
				Name string `xml:"name"`
			} `xml:"author"`
		} `xml:"entry"`
	}
	if xml.Unmarshal(data, &feed) != nil {
		return nil, Error{"arXiv 返回了无法识别的数据"}
	}
	out := []Metadata{}
	for _, e := range feed.Entries {
		if strings.Contains(e.ID, "/api/errors") {
			return nil, Error{"arXiv 拒绝了检索表达式"}
		}
		u, err := url.Parse(e.ID)
		if err != nil || (u.Hostname() != "arxiv.org" && u.Hostname() != "export.arxiv.org") || !strings.HasPrefix(u.Path, "/abs/") {
			continue
		}
		id := strings.TrimPrefix(u.Path, "/abs/")
		if !arxivID.MatchString(id) {
			continue
		}
		m := Metadata{Title: clean(e.Title), Abstract: clean(e.Summary), ArXiv: id, DOI: e.DOI, ItemType: "preprint"}
		if date, err := time.Parse(time.RFC3339, e.Published); err == nil {
			m.Date = date.Format("2006-01-02")
		}
		for _, a := range e.Authors {
			if name := splitName(a.Name); name != (Creator{}) {
				m.Creators = append(m.Creators, name)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func (c *Client) recommendationsS2(ctx context.Context, query string, limit int) ([]Metadata, error) {
	params := url.Values{"query": {query}, "limit": {strconv.Itoa(limit)}, "fields": {s2Fields}}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	data, _, _, err := c.get(ctx, strings.TrimRight(c.SemanticScholar, "/")+"/search?"+params.Encode(), "application/json", 4<<20)
	if err != nil {
		return nil, err
	}
	var body struct {
		Data []s2Paper `json:"data"`
	}
	if json.Unmarshal(data, &body) != nil {
		return nil, Error{"Semantic Scholar 返回了无法识别的数据"}
	}
	out := make([]Metadata, 0, len(body.Data))
	for _, p := range body.Data {
		m := p.metadata()
		m.URL = p.URL
		out = append(out, m)
	}
	return out, nil
}

var recommendationDOI = regexp.MustCompile(`(?i)^10\.\d{4,9}/[^\s"<>]+$`)

func recommendationMetadata(m Metadata) (Metadata, bool) {
	m.Title, m.Abstract = clean(m.Title), clean(m.Abstract)
	if m.Title == "" {
		return m, false
	}
	m.DOI = strings.ToLower(strings.TrimSpace(m.DOI))
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "http://dx.doi.org/", "https://dx.doi.org/", "doi:"} {
		m.DOI = strings.TrimPrefix(m.DOI, prefix)
	}
	if !recommendationDOI.MatchString(m.DOI) {
		m.DOI = ""
	}
	if match := arxivID.FindStringSubmatch(strings.TrimSpace(m.ArXiv)); match != nil {
		m.ArXiv = strings.ToLower(match[1])
	} else {
		m.ArXiv = ""
	}
	if m.ArXiv != "" {
		m.URL = "https://arxiv.org/abs/" + m.ArXiv
	} else if m.DOI != "" {
		m.URL = "https://doi.org/" + m.DOI
	}
	u, err := url.Parse(m.URL)
	// Fixture transport allowances must never leak into returned paper links.
	if err != nil || u.User != nil || (&Client{}).checkURL(u) != nil {
		return m, false
	}
	if m.ItemType == "" {
		m.ItemType = "other"
	}
	if len(m.Creators) > 200 {
		m.Creators = m.Creators[:200]
	}
	if len([]rune(m.Title)) > 1000 || len([]rune(m.Abstract)) > 30000 {
		return m, false
	}
	return m, true
}
