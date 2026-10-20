// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { api } from "@reader/api";
import type { RecommendationRun } from "@reader/core";
import { DailyPapers } from "./DailyPapers";
import { importReference } from "./actions";
import { paper } from "./fixtures";

vi.mock("@reader/api", () => ({
  api: {
    getPaperRecommendations: vi.fn(),
    savePaperRecommendationPreferences: vi.fn(),
    recommendPapers: vi.fn(),
  },
}));
vi.mock("./actions", () => ({ importReference: vi.fn() }));

const preferences = {
  interests: "LLM RL",
  queries: ["policy optimization"],
  count: 3,
};
const run: RecommendationRun = {
  id: "previous",
  createdAt: "2026-10-20T10:00:00Z",
  provider: "codex",
  preferences,
  warnings: [],
  papers: [
    {
      id: "one",
      title: "Verified paper",
      authors: ["Researcher"],
      date: "2026",
      status: "预印本",
      url: "javascript:alert(1)",
      doi: "10.1000/test",
      arxiv: "",
      abstract: "Source abstract",
      priority: "high",
      score: 8,
      scores: { relevance: 8, inspiration: 8, evidence: 8, novelty: 8 },
      reason: "Relevant",
      limitation: "Abstract only",
      previousRecommendation: false,
    },
  ],
};
let root: Root, host: HTMLDivElement;
const onClose = vi.fn(),
  openDocument = vi.fn();
beforeEach(async () => {
  vi.stubGlobal("IS_REACT_ACT_ENVIRONMENT", true);
  vi.clearAllMocks();
  vi.mocked(api.getPaperRecommendations).mockResolvedValue({
    preferences,
    history: [run],
  });
  vi.mocked(api.savePaperRecommendationPreferences).mockResolvedValue(
    preferences,
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root.render(<DailyPapers onClose={onClose} openDocument={openDocument} />),
  );
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  document.body.innerHTML = "";
});
const click = async (text: string) => {
  const button = [...document.querySelectorAll("button")].find(
    (item) => item.textContent === text,
  );
  expect(button, text).toBeTruthy();
  await act(async () => button!.click());
};
it("keeps the last successful recommendation when generation fails", async () => {
  vi.mocked(api.recommendPapers).mockRejectedValue(
    new Error("provider failed"),
  );
  await click("生成今日推荐");
  expect(document.body.textContent).toContain("provider failed");
  expect(document.body.textContent).toContain("Verified paper");
  expect(api.savePaperRecommendationPreferences).toHaveBeenCalledWith(
    preferences,
    expect.any(AbortSignal),
  );
});
it("serializes preference saving before allowing generation cancellation", async () => {
  const doc = paper({ id: "imported", title: "Verified paper" });
  vi.mocked(importReference).mockResolvedValue(doc);
  await click("导入论文库");
  let finish!: (value: typeof preferences) => void;
  vi.mocked(api.savePaperRecommendationPreferences).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  vi.mocked(api.recommendPapers).mockImplementation(
    () => new Promise(() => {}),
  );
  await click("生成今日推荐");
  expect(document.body.textContent).not.toContain("取消推荐");
  await click("Close");
  expect(onClose).not.toHaveBeenCalled();
  await click("打开论文");
  expect(openDocument).not.toHaveBeenCalled();
  expect(api.recommendPapers).not.toHaveBeenCalled();
  await act(async () => finish(preferences));
  await click("取消推荐");
  expect(api.recommendPapers).toHaveBeenCalledOnce();
});
it("aborts cancellation and ignores a late successful response", async () => {
  let finish!: (value: RecommendationRun) => void;
  vi.mocked(api.recommendPapers).mockImplementation(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await click("生成今日推荐");
  const signal = vi.mocked(api.recommendPapers).mock.calls[0][0]!;
  await click("取消推荐");
  expect(signal.aborted).toBe(true);
  await act(async () =>
    finish({
      ...run,
      id: "late",
      papers: [{ ...run.papers[0], title: "Late result" }],
    }),
  );
  expect(document.body.textContent).toContain("Verified paper");
  expect(document.body.textContent).not.toContain("Late result");
});
it("aborts an active request when the dialog closes", async () => {
  vi.mocked(api.recommendPapers).mockImplementation(
    () => new Promise(() => {}),
  );
  await click("生成今日推荐");
  const signal = vi.mocked(api.recommendPapers).mock.calls[0][0]!;
  await click("Close");
  expect(signal.aborted).toBe(true);
  expect(onClose).toHaveBeenCalledOnce();
});
it("imports via the existing action and opens the resulting document", async () => {
  const doc = paper({ id: "imported", title: "Verified paper" });
  vi.mocked(importReference).mockResolvedValue(doc);
  expect(document.querySelector('a[href^="javascript:"]')).toBeNull();
  await click("导入论文库");
  expect(importReference).toHaveBeenCalledExactlyOnceWith("10.1000/test");
  await click("打开论文");
  expect(openDocument).toHaveBeenCalledWith(doc);
  expect(onClose).toHaveBeenCalledOnce();
});
it("ignores a stale initial load after closing and reopening", async () => {
  await act(async () => root.render(null));
  let finish!: (value: {
    preferences: typeof preferences;
    history: RecommendationRun[];
  }) => void;
  vi.mocked(api.getPaperRecommendations).mockImplementationOnce(
    () =>
      new Promise((resolve) => {
        finish = resolve;
      }),
  );
  await act(async () =>
    root.render(<DailyPapers onClose={onClose} openDocument={openDocument} />),
  );
  expect(document.querySelector('[aria-label="研究兴趣"]')).toBeNull();
  await act(async () => root.render(null));
  await act(async () =>
    root.render(<DailyPapers onClose={onClose} openDocument={openDocument} />),
  );
  await act(async () =>
    finish({
      preferences: { ...preferences, interests: "Stale interests" },
      history: [
        {
          ...run,
          id: "stale",
          papers: [{ ...run.papers[0], title: "Stale result" }],
        },
      ],
    }),
  );
  expect(document.body.textContent).toContain("Verified paper");
  expect(document.body.textContent).not.toContain("Stale result");
  expect(
    document.querySelector<HTMLTextAreaElement>('[aria-label="研究兴趣"]')!
      .value,
  ).toBe("LLM RL");
});
