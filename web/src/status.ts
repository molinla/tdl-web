export type ImportPhase =
  | "idle"
  | "parse_json"
  | "meta_cache"
  | "resolve_peer"
  | "build_list"
  | "scan_disk"
  | "fetch_messages"
  | "queue_images"
  | "resume_downloads";

export type ImportSource =
  | "idle"
  | "json"
  | "cache"
  | "disk"
  | "telegram"
  | "download";

const phaseLabels: Record<string, string> = {
  idle: "就绪",
  parse_json: "读取 JSON 导出",
  meta_cache: "加载列表缓存",
  resolve_peer: "解析 Telegram 会话",
  build_list: "从 JSON 生成列表",
  scan_disk: "检查本地已下载",
  fetch_messages: "从 Telegram 拉取消息",
  queue_images: "排队下载图片",
  resume_downloads: "续传未完成下载",
};

const sourceLabels: Record<string, string> = {
  idle: "",
  json: "本地 JSON",
  cache: "本地缓存",
  disk: "本地磁盘",
  telegram: "Telegram",
  download: "下载",
};

export function phaseLabel(phase: string | undefined, detail?: string): string {
  if (detail?.trim()) return detail.trim();
  if (!phase || phase === "idle") return phaseLabels.idle;
  return phaseLabels[phase] ?? phase;
}

export function sourceLabel(source: string | undefined): string {
  if (!source || source === "idle") return "";
  return sourceLabels[source] ?? source;
}

export function sourcePillClass(source: string | undefined): string {
  switch (source) {
    case "telegram":
      return "pill-telegram";
    case "download":
      return "pill-download";
    case "json":
    case "cache":
      return "pill-local";
    case "disk":
      return "pill-disk";
    default:
      return "pill-neutral";
  }
}
