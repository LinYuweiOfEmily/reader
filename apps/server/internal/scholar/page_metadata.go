package scholar

import (
	"encoding/json"
	"net/url"
	"strings"
)

// Only inspect top-level articles, graph entries and explicit mainEntity nodes.
// Walking arbitrary JSON recursively would mistake citations and search results
// for the article being imported. Ambiguous article sets are left untouched.
func scholarlyNodes(data []byte) ([]map[string]any, bool) {
	if len(data) > 256<<10 {
		return nil, false
	}
	var root any
	if json.Unmarshal(data, &root) != nil {
		return nil, false
	}
	var found []map[string]any
	budget := 128
	truncated := false
	var visit func(any, int)
	visit = func(v any, depth int) {
		if v == nil {
			return
		}
		if depth > 4 || budget == 0 {
			truncated = true
			return
		}
		budget--
		switch n := v.(type) {
		case []any:
			for _, v := range n {
				visit(v, depth+1)
				if truncated {
					break
				}
			}
		case map[string]any:
			typ, _ := n["@type"].(string)
			isArticle := typ == "ScholarlyArticle" || typ == "https://schema.org/ScholarlyArticle" || typ == "http://schema.org/ScholarlyArticle"
			if ts, ok := n["@type"].([]any); ok {
				for _, t := range ts {
					if t == "ScholarlyArticle" {
						isArticle = true
					}
				}
			}
			if isArticle {
				found = append(found, n)
				return
			}
			visit(n["@graph"], depth+1)
			if typ == "WebPage" {
				visit(n["mainEntity"], depth+1)
			}
		}
	}
	visit(root, 0)
	if truncated {
		return nil, false
	}
	return found, true
}

func applyScholarlyNode(tags map[string][]string, n map[string]any) {
	add := func(key string, v any) {
		if s, ok := v.(string); ok && clean(s) != "" {
			tags["jsonld."+key] = append(tags["jsonld."+key], clean(s))
		}
	}
	add("title", n["headline"])
	if len(tags["jsonld.title"]) == 0 {
		add("title", n["name"])
	}
	add("abstract", n["abstract"])
	add("date", n["datePublished"])
	identifiers := []any{n["identifier"]}
	if values, ok := n["identifier"].([]any); ok {
		identifiers = values
	}
	for _, value := range identifiers {
		if property, ok := value.(map[string]any); ok {
			label, _ := property["propertyID"].(string)
			if !strings.EqualFold(label, "doi") {
				continue
			}
			value = property["value"]
		}
		if text, ok := value.(string); ok {
			if doi := pageDOI(text); doi != "" {
				add("doi", doi)
				break
			}
		}
	}
	authors := []any{n["author"]}
	if a, ok := n["author"].([]any); ok {
		authors = a
	}
	for _, a := range authors {
		if p, ok := a.(map[string]any); ok {
			add("author", p["name"])
		} else {
			add("author", a)
		}
	}
	if p, ok := n["publisher"].(map[string]any); ok {
		add("publisher", p["name"])
	}
	// contentUrl alone may point to HTML; require an explicit PDF encoding.
	encodings := []any{n["encoding"]}
	if a, ok := n["encoding"].([]any); ok {
		encodings = a
	}
	for _, v := range encodings {
		if e, ok := v.(map[string]any); ok {
			if e["encodingFormat"] == "application/pdf" || e["fileFormat"] == "application/pdf" {
				add("pdf", e["contentUrl"])
			}
		}
	}
}

func pageDOI(raw string) string {
	value := strings.TrimSpace(raw)
	for _, prefix := range []string{"https://doi.org/", "http://doi.org/", "https://dx.doi.org/", "http://dx.doi.org/", "doi:"} {
		if strings.HasPrefix(strings.ToLower(value), prefix) {
			value = strings.TrimSpace(value[len(prefix):])
			break
		}
	}
	if recommendationDOI.MatchString(value) {
		return value
	}
	return ""
}

func pageDOIFromTags(tags map[string][]string) string {
	for _, key := range []string{"citation_doi", "dc.identifier", "dcterms.identifier", "jsonld.doi"} {
		for _, value := range tags[key] {
			if doi := pageDOI(value); doi != "" {
				return doi
			}
		}
	}
	return ""
}

func pagePDFCandidates(tags map[string][]string, links []string, baseURL string) []string {
	base, err := url.Parse(baseURL)
	if err != nil {
		return nil
	}
	strong := append([]string{}, tags["citation_pdf_url"]...)
	strong = append(strong, tags["alternate_pdf_url"]...)
	strong = append(strong, tags["jsonld.pdf"]...)
	// Ordinary links may be references or supplements. Use them only when no
	// publisher-declared full-text candidate exists.
	if len(strong) == 0 {
		strong = links
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range strong {
		ref, e := base.Parse(strings.TrimSpace(raw))
		if e != nil {
			continue
		}
		ref.Fragment = ""
		resolved := ref.String()
		if seen[resolved] || strings.TrimSpace(raw) == "" {
			continue
		}
		seen[resolved] = true
		out = append(out, resolved)
		if len(out) == 3 {
			break
		}
	}
	return out
}
