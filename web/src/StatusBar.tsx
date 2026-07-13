import { phaseLabel, sourceLabel, sourcePillClass } from "./status";

export interface StatusBarProps {
  apiReady: boolean;
  importing: boolean;
  importPhase?: string;
  importSource?: string;
  importDetail?: string;
  importDone: number;
  importTotal: number;
  importItems: number;
  downloadingCount: number;
  queuedCount: number;
  coverBuildingCount: number;
  coverQueuedCount: number;
  coverLoadingCount: number;
  itemCount: number;
  completedCount: number;
}

function formatProgress(done: number, total: number): string {
  if (total > 0) return `${done}/${total}`;
  if (done > 0) return `${done}`;
  return "";
}

export function StatusBar({
  apiReady,
  importing,
  importPhase,
  importSource,
  importDetail,
  importDone,
  importTotal,
  importItems,
  downloadingCount,
  queuedCount,
  coverBuildingCount,
  coverQueuedCount,
  coverLoadingCount,
  itemCount,
  completedCount,
}: StatusBarProps) {
  let message = "";
  let source = "";
  let progress = "";
  let mode: "connecting" | "importing" | "ready" = "ready";

  if (!apiReady) {
    mode = "connecting";
    message = "连接 API 中…";
  } else if (importing) {
    mode = "importing";
    message = phaseLabel(importPhase, importDetail);
    source = sourceLabel(importSource);
    const prog = formatProgress(importDone, importTotal);
    if (prog) {
      progress = `${prog}${importItems > 0 ? ` · 已显示 ${importItems} 项` : ""}`;
    } else if (importItems > 0) {
      progress = `${importItems} 项`;
    }
  } else {
    const parts: string[] = [];
    if (itemCount > 0) parts.push(`${itemCount} 项`);
    if (completedCount > 0) parts.push(`${completedCount} 已完成`);
    if (downloadingCount > 0) parts.push(`${downloadingCount} 项下载中`);
    if (queuedCount > 0) parts.push(`${queuedCount} 项排队`);
    if (coverBuildingCount > 0) parts.push(`${coverBuildingCount} 个封面构建中`);
    if (coverQueuedCount > 0) parts.push(`${coverQueuedCount} 个封面队列`);
    if (coverLoadingCount > 0) parts.push(`${coverLoadingCount} 个封面请求中`);
    progress = parts.join(" · ");
  }

  const pillClass = sourcePillClass(importSource);
  const showPill = Boolean(source) && mode === "importing";

  return (
    <div
      className={[
        "stats",
        mode !== "ready" ? "stats--active" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-live="polite"
    >
      {mode === "connecting" && <span className="status-bar-spinner" />}
      {message && <span className="stats-message">{message}</span>}
      {showPill && (
        <span className={["status-pill", pillClass].join(" ")}>{source}</span>
      )}
      {progress && <span className="stats-progress">{progress}</span>}
    </div>
  );
}

export function AppSkeleton() {
  return (
    <div className="app-skeleton" aria-hidden="true">
      <div className="app-skeleton-bar" />
      <div className="app-skeleton-bar app-skeleton-bar--short" />
      <div className="app-skeleton-grid">
        {Array.from({ length: 8 }, (_, i) => (
          <div key={i} className="app-skeleton-tile" />
        ))}
      </div>
    </div>
  );
}
