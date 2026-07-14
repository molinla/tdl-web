import type { Item, ItemsPayload, RangeType } from "./types";

const token = new URLSearchParams(window.location.search).get("token") ?? "";

/** Direct API origin for media Range streaming (Vite proxy breaks large video). */
export const API_BASE = (
  import.meta.env.VITE_API_BASE || "http://127.0.0.1:8080"
).replace(/\/$/, "");

function headers(extra?: HeadersInit): HeadersInit {
  const h = new Headers(extra);
  if (token) h.set("X-Web-Token", token);
  return h;
}

function withToken(path: string): string {
  if (!token) return path;
  const u = new URL(path, "http://local.invalid");
  u.searchParams.set("token", token);
  return u.pathname + u.search;
}

function apiPath(path: string): string {
  if (/^https?:\/\//i.test(path)) return path;
  return withToken(path.startsWith("/") ? path : `/${path}`);
}

function apiURL(path: string): string {
  if (/^https?:\/\//i.test(path)) return path;
  return `${API_BASE}${apiPath(path)}`;
}

/** Absolute media URL (covers, streams) via API_BASE so Range requests bypass the Vite proxy. */
export function mediaURL(path?: string): string {
  if (!path) return "";
  return apiURL(path);
}

/** Cover/thumb URL with cache-bust revision (status, progress, retry attempt). */
export function coverURL(path?: string, revision?: string | number): string {
  const url = mediaURL(path);
  if (!url || revision === undefined || revision === "") return url;
  const u = new URL(url);
  u.searchParams.set("v", String(revision));
  return u.toString();
}

export async function fetchItems(): Promise<ItemsPayload> {
  const res = await fetch(apiURL("/api/items"), { headers: headers() });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function importJSON(
  file: File,
  rangeType: RangeType,
  from: string,
  to: string,
): Promise<ItemsPayload> {
  const body = new FormData();
  body.append("file", file);
  if (rangeType) {
    body.append("type", rangeType);
    if (from) body.append("from", from);
    if (to) body.append("to", to);
  }
  const res = await fetch(apiURL("/api/import"), {
    method: "POST",
    headers: headers(),
    body,
  });
  if (!res.ok) throw new Error(await res.text());
  return res.json();
}

export async function cacheItem(id: string): Promise<void> {
  const res = await fetch(apiURL(`/api/items/${id}/cache`), {
    method: "POST",
    headers: headers(),
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function pauseItem(id: string): Promise<void> {
  const res = await fetch(apiURL(`/api/items/${id}/pause`), {
    method: "POST",
    headers: headers(),
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function updateCoverState(
  paused: boolean,
  visibleVideoIds: string[],
  keepalive = false,
): Promise<void> {
  const res = await fetch(apiURL("/api/covers/state"), {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    body: JSON.stringify({
      paused,
      visible_video_ids: visibleVideoIds,
    }),
    keepalive,
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function downloadItems(ids: string[]): Promise<void> {
  const res = await fetch(apiURL("/api/items/download"), {
    method: "POST",
    headers: headers({ "Content-Type": "application/json" }),
    body: JSON.stringify({ ids }),
  });
  if (!res.ok) throw new Error(await res.text());
}

export async function probeMediaError(path?: string): Promise<string> {
  if (!path) return "";
  try {
    const res = await fetch(mediaURL(path), {
      headers: headers({ Range: "bytes=0-0" }),
    });
    if (res.ok || res.status === 206) return "";
    const text = (await res.text()).trim();
    return text || `HTTP ${res.status}`;
  } catch (err) {
    return err instanceof Error ? err.message : String(err);
  }
}

export function subscribeEvents(
  onData: (payload: ItemsPayload) => void,
  onError?: (err: Event) => void,
): () => void {
  const es = new EventSource(apiURL("/api/events"));
  es.onmessage = (ev) => {
    try {
      onData(JSON.parse(ev.data) as ItemsPayload);
    } catch {
      /* ignore malformed */
    }
  };
  es.onerror = (err) => onError?.(err);
  return () => es.close();
}

export function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 ** 2) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 ** 3) return `${(bytes / 1024 ** 2).toFixed(1)} MB`;
  return `${(bytes / 1024 ** 3).toFixed(2)} GB`;
}

export function formatDuration(sec?: number): string {
  if (!sec || sec <= 0) return "";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function progressPct(item: Item): number {
  if (item.status === "completed") return 100;
  if (!item.size) return 0;
  return Math.min(100, Math.round((item.progress / item.size) * 100));
}

export function statusLabel(item: Item): string {
  switch (item.status) {
    case "caching":
      return `下载中 ${progressPct(item)}%`;
    case "paused":
      return `已暂停 ${progressPct(item)}%`;
    case "completed":
      return "已完成";
    case "error":
      return "错误";
    case "queued":
      if (item.queue_pos && item.queue_pos > 0) {
        return `排队 #${item.queue_pos}`;
      }
      return "未下载";
    default:
      if (item.queue_pos && item.queue_pos > 0) {
        return `排队 #${item.queue_pos}`;
      }
      return item.status;
  }
}

export function formatMessageDate(unix?: number): string {
  if (!unix || unix <= 0) return "";
  const d = new Date(unix * 1000);
  const y = d.getFullYear();
  const mo = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  const h = String(d.getHours()).padStart(2, "0");
  const mi = String(d.getMinutes()).padStart(2, "0");
  return `${y}-${mo}-${day} ${h}:${mi}`;
}

/** Strip `{peerId}_{messageId}_` template prefix for UI labels. */
export function displayName(item: Item): string {
  const name = item.name || "";
  const prefix = `${item.peer_id}_${item.message_id}_`;
  if (name.startsWith(prefix)) {
    const rest = name.slice(prefix.length);
    return rest || name;
  }
  // Fallback: leading digits_digits_ even if ids drifted
  const m = name.match(/^\d+_\d+_(.+)$/);
  return m?.[1] || name;
}
