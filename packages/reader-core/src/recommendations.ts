export interface PaperRecommendationPreferences {
  interests: string;
  queries: string[];
  count: number;
}

export interface RecommendedPaper {
  id: string;
  title: string;
  authors: string[];
  date: string;
  status: string;
  url: string;
  doi: string;
  arxiv: string;
  abstract: string;
  priority: "high" | "medium" | "low";
  score: number;
  scores: {
    relevance: number;
    inspiration: number;
    evidence: number;
    novelty: number;
  };
  reason: string;
  limitation: string;
  previousRecommendation: boolean;
  documentId?: string;
  read?: boolean;
}

export interface RecommendationRun {
  id: string;
  createdAt: string;
  provider: string;
  preferences: PaperRecommendationPreferences;
  warnings: string[];
  papers: RecommendedPaper[];
}

export interface PaperRecommendations {
  preferences: PaperRecommendationPreferences;
  history: RecommendationRun[];
}
