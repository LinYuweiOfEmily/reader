import { useEffect, useRef, useState } from "react";
import { api } from "@reader/api";
import type {
  Document,
  PaperRecommendationPreferences,
  RecommendationRun,
  RecommendedPaper,
} from "@reader/core";
import { Button } from "@reader/ui/components/button";
import { Textarea } from "@reader/ui/components/textarea";
import { Badge } from "@reader/ui/components/badge";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@reader/ui/components/dialog";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@reader/ui/components/select";
import { Loader2 } from "lucide-react";
import { useReaderStore } from "../store";
import { importReference } from "./actions";

function safeURL(value: string) {
  try {
    const url = new URL(value);
    return ["https:", "http:"].includes(url.protocol) ? url.href : undefined;
  } catch {
    return undefined;
  }
}

/** Mounted only while open: closing drops transient edits and cancels generation. */
export function DailyPapers({
  onClose,
  openDocument,
}: {
  onClose: () => void;
  openDocument: (document: Document) => void;
}) {
  const [preferences, setPreferences] =
    useState<PaperRecommendationPreferences>();
  const [queries, setQueries] = useState("");
  const [history, setHistory] = useState<RecommendationRun[]>([]);
  const [selected, setSelected] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<"save" | "generate" | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const alive = useRef(true);
  const operation = useRef<AbortController | null>(null);
  const run = history.find((item) => item.id === selected);

  useEffect(() => {
    let current = true;
    alive.current = true;
    void api
      .getPaperRecommendations()
      .then((state) => {
        if (!current || !alive.current) return;
        setPreferences(state.preferences);
        setQueries(state.preferences.queries.join("\n"));
        setHistory(state.history.slice(0, 30));
        setSelected(state.history[0]?.id ?? "");
      })
      .catch((e: Error) => {
        if (current && alive.current) setError(e.message);
      })
      .finally(() => {
        if (current && alive.current) setLoading(false);
      });
    return () => {
      current = false;
      alive.current = false;
      operation.current?.abort();
    };
  }, []);

  const close = () => {
    // Keep saves serialized; canceling only the UI while a PUT is in flight
    // could let an older preference overwrite the next recommendation.
    if (busy === "save") return;
    alive.current = false;
    operation.current?.abort();
    onClose();
  };
  const cancel = () => {
    operation.current?.abort();
    operation.current = null;
    setBusy(null);
    setNotice("已取消本次推荐");
  };
  const submit = async (generate: boolean) => {
    if (!preferences || operation.current) return;
    const controller = new AbortController();
    operation.current = controller;
    const current = () => alive.current && !controller.signal.aborted;
    setBusy("save");
    setError("");
    setNotice("");
    try {
      const saved = await api.savePaperRecommendationPreferences(
        {
          ...preferences,
          interests: preferences.interests.trim(),
          queries: queries
            .split("\n")
            .map((q) => q.trim())
            .filter(Boolean),
        },
        controller.signal,
      );
      if (!current()) return;
      setPreferences(saved);
      setQueries(saved.queries.join("\n"));
      if (generate) {
        setBusy("generate");
        const result = await api.recommendPapers(controller.signal);
        if (!current()) return;
        setHistory((old) =>
          [result, ...old.filter((item) => item.id !== result.id)].slice(0, 30),
        );
        setSelected(result.id);
      } else setNotice("偏好已保存");
    } catch (e) {
      if (current()) setError((e as Error).message);
    } finally {
      if (operation.current === controller) {
        operation.current = null;
        if (alive.current) setBusy(null);
      }
    }
  };
  const valid = !!preferences?.interests.trim() && !!queries.trim();
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) close();
      }}
    >
      <DialogContent className="max-h-[85vh] overflow-y-auto sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>今日论文推荐</DialogTitle>
          <DialogDescription>
            按研究偏好检索并筛选论文；评分和理由是基于摘要的 AI
            判断，尚未核验全文。
          </DialogDescription>
        </DialogHeader>
        {loading && <p role="status">正在读取推荐记录…</p>}
        {preferences && (
          <details open={!history.length} className="rounded-lg border p-3">
            <summary className="cursor-pointer">研究偏好</summary>
            <div className="mt-3 grid gap-3">
              <label className="grid gap-1">
                研究兴趣
                <Textarea
                  aria-label="研究兴趣"
                  value={preferences.interests}
                  disabled={!!busy}
                  maxLength={8000}
                  onChange={(e) =>
                    setPreferences({
                      ...preferences,
                      interests: e.target.value,
                    })
                  }
                />
              </label>
              <label className="grid gap-1">
                检索词（每行一条）
                <Textarea
                  aria-label="检索词"
                  value={queries}
                  disabled={!!busy}
                  maxLength={8000}
                  onChange={(e) => setQueries(e.target.value)}
                />
              </label>
              <div className="flex items-center gap-2">
                <Select
                  value={String(preferences.count)}
                  disabled={!!busy}
                  onValueChange={(count) => {
                    if (count)
                      setPreferences({ ...preferences, count: Number(count) });
                  }}
                >
                  <SelectTrigger aria-label="推荐篇数">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {[3, 4, 5].map((count) => (
                      <SelectItem key={count} value={String(count)}>
                        {count} 篇
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <Button
                  variant="outline"
                  disabled={!valid || !!busy}
                  onClick={() => void submit(false)}
                >
                  保存偏好
                </Button>
              </div>
            </div>
          </details>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <Button
            disabled={!valid || !!busy || loading}
            onClick={() => void submit(true)}
          >
            {busy === "generate" && (
              <Loader2 className="animate-spin motion-reduce:animate-none" />
            )}
            {busy === "generate"
              ? "正在检索与筛选…"
              : busy === "save"
                ? "正在保存偏好…"
                : "生成今日推荐"}
          </Button>
          {busy === "generate" && (
            <Button variant="outline" onClick={cancel}>
              取消推荐
            </Button>
          )}
          {history.length > 0 && (
            <Select
              value={selected}
              onValueChange={(id) => {
                if (id) setSelected(id);
              }}
            >
              <SelectTrigger
                aria-label="推荐历史"
                className="min-w-0 max-w-full"
              >
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {history.map((item) => (
                  <SelectItem key={item.id} value={item.id}>
                    {new Date(item.createdAt).toLocaleString("zh-CN")} ·{" "}
                    {item.papers.length} 篇
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </div>
        {error && (
          <p role="alert" className="text-destructive">
            {error}
          </p>
        )}
        {notice && (
          <p role="status" className="text-muted-foreground">
            {notice}
          </p>
        )}
        {run && (
          <section aria-label="推荐结果" className="grid min-w-0 gap-3">
            <p className="text-xs text-muted-foreground">
              AI：{run.provider} · 最近 30 次推荐保存在本机
            </p>
            {run.warnings.map((warning, index) => (
              <p key={index} className="text-sm text-muted-foreground">
                {warning}
              </p>
            ))}
            {run.papers.map((paper) => (
              <PaperRecommendation
                key={`${run.id}:${paper.id}`}
                paper={paper}
                disabled={busy === "save"}
                onOpen={(document) => {
                  if (busy === "save") return;
                  close();
                  openDocument(document);
                }}
              />
            ))}
          </section>
        )}
      </DialogContent>
    </Dialog>
  );
}

function PaperRecommendation({
  paper,
  onOpen,
  disabled,
}: {
  paper: RecommendedPaper;
  onOpen: (doc: Document) => void;
  disabled: boolean;
}) {
  const documents = useReaderStore((state) => state.documents);
  const [imported, setImported] = useState<Document>();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const existing =
    documents.find((doc) => doc.id === (imported?.id || paper.documentId)) ||
    imported;
  const url = safeURL(paper.url);
  const reference = paper.doi || paper.arxiv || url;
  const importPaper = async () => {
    if (busy || !reference) return;
    setBusy(true);
    setError("");
    try {
      setImported(await importReference(reference));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <article className="grid min-w-0 gap-2 rounded-lg border p-3">
      <h3 className="font-medium break-words">{paper.title}</h3>
      <p className="text-xs text-muted-foreground break-words">
        {paper.authors.join("、")} · {paper.date} · {paper.status}
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <Badge variant="secondary">
          {
            { high: "优先阅读", medium: "值得阅读", low: "备选阅读" }[
              paper.priority
            ]
          }
        </Badge>
        <span>AI 评分 {paper.score.toFixed(1)}/10</span>
        {paper.previousRecommendation && (
          <Badge variant="outline">曾推荐</Badge>
        )}
        {(existing || paper.documentId) && (
          <Badge variant="outline">已入库</Badge>
        )}
        {(paper.read || existing?.readingStatus === "done") && (
          <Badge variant="outline">已读</Badge>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        相关性 {paper.scores.relevance} · 启发性 {paper.scores.inspiration} ·
        证据 {paper.scores.evidence} · 新颖性 {paper.scores.novelty}
      </p>
      <p className="break-words">{paper.reason}</p>
      <p className="text-muted-foreground break-words">
        局限：{paper.limitation}
      </p>
      {paper.abstract && (
        <details>
          <summary className="cursor-pointer text-muted-foreground">
            原始摘要
          </summary>
          <p className="mt-2 whitespace-pre-wrap break-words">
            {paper.abstract}
          </p>
        </details>
      )}
      <div className="flex flex-wrap items-center gap-3">
        {url && (
          <a
            href={url}
            target="_blank"
            rel="noreferrer noopener"
            className="underline underline-offset-4"
          >
            论文来源
          </a>
        )}
        {paper.doi && (
          <a
            href={`https://doi.org/${encodeURIComponent(paper.doi)}`}
            target="_blank"
            rel="noreferrer noopener"
            className="underline underline-offset-4"
          >
            DOI
          </a>
        )}
        {paper.arxiv && (
          <a
            href={`https://arxiv.org/abs/${encodeURIComponent(paper.arxiv)}`}
            target="_blank"
            rel="noreferrer noopener"
            className="underline underline-offset-4"
          >
            arXiv
          </a>
        )}
        {existing ? (
          <Button
            size="sm"
            variant="outline"
            disabled={disabled}
            onClick={() => onOpen(existing)}
          >
            打开论文
          </Button>
        ) : (
          <Button
            size="sm"
            variant="outline"
            disabled={disabled || busy || !reference}
            onClick={() => void importPaper()}
          >
            {busy ? "正在导入…" : "导入论文库"}
          </Button>
        )}
      </div>
      {error && (
        <p role="alert" className="text-destructive">
          {error}
        </p>
      )}
    </article>
  );
}
