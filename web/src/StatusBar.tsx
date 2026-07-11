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
  coverLoadingCount: number;
  itemCount: number;
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
  coverLoadingCount,
  itemCount,
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
    message = "就绪";
    const parts: string[] = [];
    if (itemCount > 0) parts.push(`${itemCount} 项`);
    if (downloadingCount > 0) parts.push(`${downloadingCount} 项下载中`);
    if (queuedCount > 0) parts.push(`${queuedCount} 项排队`);
    if (coverLoadingCount > 0) parts.push(`${coverLoadingCount} 个封面加载中`);
    progress = parts.join(" · ");
  }

  const pillClass = sourcePillClass(importSource);
  const showPill = Boolean(source) && mode === "importing";

  return (
    <div
      className={["status-bar", mode !== "ready" ? "status-bar--active" : ""]
        .filter(Boolean)
        .join(" ")}
      aria-live="polite"
    >
      <div className="status-bar-main">
        {mode === "connecting" && <span className="status-bar-spinner" />}
        <span className="status-bar-message">{message}</span>
        {showPill && (
          <span className={["status-pill", pillClass].join(" ")}>{source}</span>
        )}
      </div>
      {progress && <div className="status-bar-progress">{progress}</div>}
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
