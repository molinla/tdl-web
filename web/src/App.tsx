import { useWindowVirtualizer, type Virtualizer } from "@tanstack/react-virtual";
import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
  type RefObject,
} from "react";
import {
  cacheItem,
  displayName,
  downloadItems,
  fetchItems,
  formatDuration,
  formatMessageDate,
  formatSize,
  importJSON,
  coverURL,
  pauseItem,
  progressPct,
  statusLabel,
  subscribeEvents,
} from "./api";
import { splitIntoColumns } from "./masonry";
import { FilmBackground } from "./FilmBackground";
import { MediaPreview } from "./MediaPreview";
import { AppSkeleton, StatusBar } from "./StatusBar";
import type { Item, ItemsPayload, RangeType } from "./types";

type PlayerState =
  | { kind: "video"; item: Item }
  | { kind: "image"; item: Item }
  | null;

const masonrySizeCache = new Map<string, number>();
/** Last measured tile box plus decoded cover ratio keep virtual top estimates stable across remounts. */
const masonryBoxCache = new Map<
  string,
  { width: number; height: number }
>();
const coverAspectCache = new Map<string, number>();
const VIRTUAL_BUFFER_SCREENS = 2;

const viewportBufferSubs = new Set<() => void>();
let viewportBufferPx =
  typeof window !== "undefined"
    ? Math.round(window.innerHeight * VIRTUAL_BUFFER_SCREENS)
    : 1200;

if (typeof window !== "undefined") {
  window.addEventListener("resize", () => {
    viewportBufferPx = Math.round(window.innerHeight * VIRTUAL_BUFFER_SCREENS);
    viewportBufferSubs.forEach((fn) => fn());
  });
}

/** Shared viewport-height buffer (used by virtual overscan + cover lazy-load). */
function useViewportBufferPx() {
  const [px, setPx] = useState(viewportBufferPx);

  useEffect(() => {
    const sub = () => setPx(viewportBufferPx);
    viewportBufferSubs.add(sub);
    return () => {
      viewportBufferSubs.delete(sub);
    };
  }, []);

  return px;
}

function useLazyRootMargin() {
  const bufferPx = useViewportBufferPx();
  return `${bufferPx}px 0px`;
}

/** Overscan enough items to cover N viewport heights above/below the window. */
function useVirtualOverscan(estimateSize: number, gap: number) {
  const bufferPx = useViewportBufferPx();
  const calc = useCallback(() => {
    const row = Math.max(estimateSize + gap, 1);
    return Math.max(4, Math.ceil(bufferPx / row));
  }, [bufferPx, estimateSize, gap]);

  const [overscan, setOverscan] = useState(calc);

  useEffect(() => {
    setOverscan(calc());
  }, [calc]);

  return overscan;
}

function useScrollMargin(
  ref: RefObject<HTMLElement | null>,
  layoutKey0?: unknown,
  layoutKey1?: unknown,
  layoutKey2?: unknown,
) {
  const [scrollMargin, setScrollMargin] = useState(0);
  const scrollMarginRef = useRef(0);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = () => {
      const next = el.getBoundingClientRect().top + window.scrollY;
      if (Math.abs(next - scrollMarginRef.current) < 0.5) return;
      scrollMarginRef.current = next;
      setScrollMargin(next);
    };
    update();
    window.addEventListener("resize", update);
    const ro = new ResizeObserver(update);
    ro.observe(el);
    if (document.body) ro.observe(document.body);
    const id = window.requestAnimationFrame(update);
    return () => {
      window.removeEventListener("resize", update);
      ro.disconnect();
      window.cancelAnimationFrame(id);
    };
  }, [ref, layoutKey0, layoutKey1, layoutKey2]);

  return scrollMargin;
}

function useColumnCount(minColumnWidth: number, gap: number) {
  const ref = useRef<HTMLDivElement>(null);
  const [columns, setColumns] = useState(1);
  const [width, setWidth] = useState(0);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const update = (width: number) => {
      setWidth(width);
      setColumns(
        Math.max(1, Math.floor((width + gap) / (minColumnWidth + gap))),
      );
    };
    update(el.clientWidth);
    const ro = new ResizeObserver(([entry]) => {
      update(entry.contentRect.width);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, [gap, minColumnWidth]);

  const columnWidth = useMemo(() => {
    if (width <= 0) return 0;
    return Math.max(1, (width - gap * (columns - 1)) / columns);
  }, [columns, gap, width]);

  return { ref, columns, columnWidth };
}

function cacheCoverAspect(id: string | undefined, img: HTMLImageElement) {
  if (!id || img.naturalWidth <= 0 || img.naturalHeight <= 0) return;
  coverAspectCache.set(id, img.naturalHeight / img.naturalWidth);
}

function readMasonryBox(
  element: Element,
  entry: ResizeObserverEntry | undefined,
) {
  let width = 0;
  let height = 0;
  if (entry?.borderBoxSize?.[0]) {
    const box = entry.borderBoxSize[0];
    width = box.inlineSize;
    height = box.blockSize;
  } else {
    const rect = element.getBoundingClientRect();
    width = rect.width;
    height = rect.height;
  }

  return {
    width: Math.max(0, Math.ceil(width)),
    height: Math.max(0, Math.ceil(height)),
  };
}

function estimateMasonryItemSize(
  item: Item | undefined,
  fallback: number,
  columnWidth: number,
) {
  if (!item) return fallback;

  const ratio = coverAspectCache.get(item.id);
  if (ratio && columnWidth > 0) {
    return Math.max(1, Math.ceil(columnWidth * ratio));
  }

  const measured = masonryBoxCache.get(item.id);
  if (measured && measured.height > 0) {
    if (columnWidth > 0 && measured.width > 0) {
      if (Math.abs(measured.width - columnWidth) <= 1) {
        return measured.height;
      }
      return Math.max(1, Math.ceil((measured.height * columnWidth) / measured.width));
    }
    return measured.height;
  }

  return masonrySizeCache.get(item.id) ?? fallback;
}

function MasonryColumn({
  items,
  gap,
  estimateSize,
  columnWidth,
  scrollMargin,
  renderItem,
}: {
  items: Item[];
  gap: number;
  estimateSize: number;
  columnWidth: number;
  scrollMargin: number;
  renderItem: (item: Item) => ReactNode;
}) {
  const overscan = useVirtualOverscan(estimateSize, gap);
  const getItemKey = useCallback((index: number) => {
    return items[index]?.id ?? index;
  }, [items]);
  const estimateItemSize = useCallback(
    (index: number) =>
      estimateMasonryItemSize(items[index], estimateSize, columnWidth),
    [columnWidth, estimateSize, items],
  );
  const measureMasonryElement = useCallback(
    (
      element: HTMLElement,
      entry: ResizeObserverEntry | undefined,
      instance: Virtualizer<Window, HTMLElement>,
    ) => {
      const index = instance.indexFromElement(element);
      const key = instance.options.getItemKey(index);
      const cached = instance.itemSizeCache.get(key);
      const item = items[index];
      const knownSize = estimateMasonryItemSize(item, estimateSize, columnWidth);

      // When a loaded cover's URL is cache-busted (queued -> caching -> completed),
      // LazyCover briefly renders its fallback while the new src decodes.  Do not
      // let that transient fallback overwrite the real, already-known tile height.
      if (
        item &&
        !element.querySelector(".cover-img--ready") &&
        (coverAspectCache.has(item.id) || masonryBoxCache.has(item.id))
      ) {
        return knownSize;
      }

      const { width, height } = readMasonryBox(element, entry);
      if (height <= 0) {
        return cached ?? knownSize;
      }
      if (item) {
        masonrySizeCache.set(item.id, height);
        masonryBoxCache.set(item.id, { width, height });
      }
      return height;
    },
    [columnWidth, estimateSize, items],
  );
  const virtualizer = useWindowVirtualizer({
    count: items.length,
    estimateSize: estimateItemSize,
    overscan,
    scrollMargin,
    gap,
    getItemKey,
    measureElement: measureMasonryElement,
  });

  useLayoutEffect(() => {
    virtualizer.shouldAdjustScrollPositionOnItemSizeChange = () => false;
  }, [virtualizer]);

  if (items.length === 0) return <div className="masonry-col" />;

  return (
    <div
      className="masonry-col"
      style={{ height: virtualizer.getTotalSize(), position: "relative" }}
    >
      {virtualizer.getVirtualItems().map((vItem) => {
        const item = items[vItem.index];
        if (!item) return null;
        return (
          <div
            key={vItem.key}
            className="masonry-item"
            data-index={vItem.index}
            ref={virtualizer.measureElement}
            style={{
              position: "absolute",
              top: 0,
              left: 0,
              width: "100%",
              transform: `translateY(${vItem.start - scrollMargin}px)`,
            }}
          >
            {renderItem(item)}
          </div>
        );
      })}
    </div>
  );
}

function VirtualMasonry({
  items,
  minColumnWidth,
  gap,
  estimateSize,
  renderItem,
  className,
}: {
  items: Item[];
  minColumnWidth: number;
  gap: number;
  estimateSize: number;
  renderItem: (item: Item) => ReactNode;
  className?: string;
}) {
  const { ref, columns, columnWidth } = useColumnCount(minColumnWidth, gap);
  const buckets = useMemo(
    () => splitIntoColumns(items, columns),
    [items, columns],
  );
  const scrollMargin = useScrollMargin(ref, items.length, columns, gap);

  const style = { "--masonry-gap": `${gap}px` } as CSSProperties;

  return (
    <div ref={ref} className={className} style={style}>
      <div className="masonry-columns">
        {buckets.map((colItems, colIndex) => (
          <MasonryColumn
            key={colIndex}
            items={colItems}
            gap={gap}
            estimateSize={estimateSize}
            columnWidth={columnWidth}
            scrollMargin={scrollMargin}
            renderItem={renderItem}
          />
        ))}
      </div>
    </div>
  );
}

const COVER_RETRY_MS = 2000;
const COVER_PRIORITY_RETRY_MS = 700;
const COVER_TOTAL_MAX = 6;
const COVER_LOW_PRIORITY_MAX = 2;
const COVER_PLACEHOLDER_W = 320;
const COVER_PLACEHOLDER_H = 180;

/** Remember covers that already decoded so virtual remounts do not reload. */
const coverLoadCache = new Set<string>();

type CoverPriority = "high" | "normal";
type CoverWaiter = {
  priority: CoverPriority;
  grant: (release: () => void) => void;
};

let coverActiveTotal = 0;
let coverActiveLow = 0;
const coverHighWaiters: CoverWaiter[] = [];
const coverLowWaiters: CoverWaiter[] = [];

function canStartCover(priority: CoverPriority): boolean {
  if (coverActiveTotal >= COVER_TOTAL_MAX) return false;
  if (priority === "normal" && coverActiveLow >= COVER_LOW_PRIORITY_MAX) {
    return false;
  }
  return true;
}

function startCoverWaiter(waiter: CoverWaiter) {
  coverActiveTotal += 1;
  if (waiter.priority === "normal") coverActiveLow += 1;
  let released = false;
  waiter.grant(() => {
    if (released) return;
    released = true;
    coverActiveTotal = Math.max(0, coverActiveTotal - 1);
    if (waiter.priority === "normal") {
      coverActiveLow = Math.max(0, coverActiveLow - 1);
    }
    flushCoverWaiters();
  });
}

function flushCoverWaiters() {
  while (coverHighWaiters.length > 0 && canStartCover("high")) {
    const next = coverHighWaiters.shift();
    if (next) startCoverWaiter(next);
  }
  while (coverLowWaiters.length > 0 && canStartCover("normal")) {
    const next = coverLowWaiters.shift();
    if (next) startCoverWaiter(next);
  }
}

function acquireCoverSlot(priority: CoverPriority): Promise<() => void> {
  return new Promise((resolve) => {
    const waiter: CoverWaiter = { priority, grant: resolve };
    if (canStartCover(priority)) {
      startCoverWaiter(waiter);
      return;
    }
    if (priority === "high") {
      coverHighWaiters.unshift(waiter);
    } else {
      coverLowWaiters.push(waiter);
    }
  });
}

async function isPlaceholderThumb(url: string): Promise<boolean> {
  try {
    const res = await fetch(url, { method: "HEAD", cache: "no-store" });
    return (res.headers.get("content-type") ?? "").includes("image/svg");
  } catch {
    return false;
  }
}

/** Netflix-style buffering ring for cover loading. */
function NetflixSpinner() {
  return <div className="netflix-spinner" role="status" aria-label="加载中" />;
}

/** Load cover when near viewport; retry while visible if thumb is not ready yet. */
function LazyCover({
  src,
  alt,
  className,
  fallbackClass = "poster-fallback",
  fallbackText = "No Cover",
  coverId,
  coverPriority = "normal",
  previewSourceId,
  previewHidden,
  onLoadingChange,
}: {
  src?: string;
  alt: string;
  className?: string;
  fallbackClass?: string;
  fallbackText?: string;
  coverId?: string;
  coverPriority?: CoverPriority;
  previewSourceId?: string;
  previewHidden?: boolean;
  onLoadingChange?: (id: string, loading: boolean) => void;
}) {
  const ref = useRef<HTMLDivElement>(null);
  const lazyRootMargin = useLazyRootMargin();
  const cachedCover = Boolean(src && coverLoadCache.has(src));
  const [inView, setInView] = useState(false);
  const [ready, setReady] = useState(cachedCover);
  const [imgLoaded, setImgLoaded] = useState(cachedCover);
  const [retryAttempt, setRetryAttempt] = useState(0);
  const releaseRef = useRef<(() => void) | null>(null);
  const retryTimerRef = useRef<number | null>(null);
  const inViewRef = useRef(false);

  const loadSrc = useMemo(() => {
    if (!src) return "";
    if (retryAttempt <= 0) return src;
    const u = new URL(src);
    u.searchParams.set("retry", String(retryAttempt));
    return u.toString();
  }, [src, retryAttempt]);

  function clearRetryTimer() {
    if (retryTimerRef.current != null) {
      window.clearTimeout(retryTimerRef.current);
      retryTimerRef.current = null;
    }
  }

  function releaseCoverSlot() {
    releaseRef.current?.();
    releaseRef.current = null;
  }

  function requestRetry() {
    clearRetryTimer();
    releaseCoverSlot();
    setReady(false);
    setImgLoaded(false);
    if (!inViewRef.current || !src) return;
    retryTimerRef.current = window.setTimeout(() => {
      retryTimerRef.current = null;
      setRetryAttempt((n) => n + 1);
    }, coverPriority === "high" ? COVER_PRIORITY_RETRY_MS : COVER_RETRY_MS);
  }

  useEffect(() => {
    inViewRef.current = inView;
  }, [inView]);

  useEffect(() => {
    if (!src) return;
    if (coverLoadCache.has(src)) {
      setReady(true);
      setImgLoaded(true);
      return;
    }
    setInView(false);
    setReady(false);
    setImgLoaded(false);
    setRetryAttempt(0);
    clearRetryTimer();
    releaseCoverSlot();
  }, [src]);

  useEffect(() => {
    const el = ref.current;
    if (!el || !src) return;

    const io = new IntersectionObserver(
      ([entry]) => setInView(entry.isIntersecting),
      { rootMargin: lazyRootMargin },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [src, lazyRootMargin]);

  useEffect(() => {
    if (!inView) {
      clearRetryTimer();
      if (!imgLoaded) {
        releaseCoverSlot();
        setReady(false);
      }
      return;
    }
    if (!src || ready || retryTimerRef.current != null || previewHidden) return;
    let cancelled = false;
    void acquireCoverSlot(coverPriority).then((release) => {
      if (cancelled || !inViewRef.current || previewHidden) {
        release();
        return;
      }
      releaseRef.current = release;
      setReady(true);
    });
    return () => {
      cancelled = true;
    };
  }, [src, inView, ready, retryAttempt, coverPriority, previewHidden, imgLoaded]);

  useEffect(() => {
    return () => {
      clearRetryTimer();
      releaseCoverSlot();
    };
  }, []);

  async function handleLoad(img: HTMLImageElement) {
    if (
      img.naturalWidth === COVER_PLACEHOLDER_W &&
      img.naturalHeight === COVER_PLACEHOLDER_H &&
      (await isPlaceholderThumb(img.src))
    ) {
      requestRetry();
      return;
    }
    releaseCoverSlot();
    clearRetryTimer();
    cacheCoverAspect(coverId, img);
    if (src) coverLoadCache.add(src);
    setImgLoaded(true);
  }

  const hasSrc = Boolean(loadSrc);
  const cachedReady = Boolean(src && coverLoadCache.has(src));
  const showImg = hasSrc && (ready || cachedReady);
  const isLoading =
    hasSrc && inView && !imgLoaded && !previewHidden && !cachedReady;
  const showLoadingFallback = isLoading;
  const showFallback =
    hasSrc && !imgLoaded && !cachedReady && !previewHidden;
  const cachedAspect = coverId ? coverAspectCache.get(coverId) : undefined;
  const lockedAspectStyle = cachedAspect
    ? ({ aspectRatio: `1 / ${cachedAspect}` } as CSSProperties)
    : undefined;

  useEffect(() => {
    if (!src || !coverLoadCache.has(src)) return;
    setReady(true);
    setImgLoaded(true);
  }, [previewHidden, src]);

  useEffect(() => {
    if (!coverId || !onLoadingChange) return;
    onLoadingChange(coverId, isLoading);
    return () => onLoadingChange(coverId, false);
  }, [coverId, isLoading, onLoadingChange]);

  return (
    <div
      ref={ref}
      className={[
        "cover-shell",
        previewHidden ? "cover-shell--preview-hidden" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      data-preview-source={previewSourceId}
      style={lockedAspectStyle}
    >
      {showImg && (
        <img
          className={[
            "cover-img",
            imgLoaded || cachedReady
              ? "cover-img--ready"
              : "cover-img--pending",
            className,
          ]
            .filter(Boolean)
            .join(" ")}
          src={loadSrc}
          alt={alt}
          loading="lazy"
          decoding="async"
          fetchPriority={coverPriority === "high" ? "high" : "low"}
          onLoad={(e) => void handleLoad(e.currentTarget)}
          onError={() => requestRetry()}
        />
      )}
      {showFallback && (
        <div className={fallbackClass} style={lockedAspectStyle}>
          {showLoadingFallback ? (
            <span className="cover-loading-label">
              <NetflixSpinner />
            </span>
          ) : (
            fallbackText
          )}
        </div>
      )}
    </div>
  );
}

function DownloadDock({ items }: { items: Item[] }) {
  const active = useMemo(() => {
    const caching = items.filter((i) => i.status === "caching");
    const queued = items
      .filter((i) => (i.queue_pos ?? 0) > 0 && i.status !== "caching")
      .sort((a, b) => (a.queue_pos ?? 0) - (b.queue_pos ?? 0));
    return [...caching, ...queued];
  }, [items]);

  if (active.length === 0) return null;

  const shown = active.slice(0, 12);
  const extra = active.length - shown.length;

  return (
    <div className="download-dock" aria-live="polite">
      <div className="download-dock-label">下载队列</div>
      <div className="download-dock-list">
        {shown.map((item) => (
          <div key={item.id} className="download-dock-item" title={displayName(item)}>
            <span className="download-dock-name">{displayName(item)}</span>
            <span className="download-dock-meta">
              {item.status === "caching"
                ? `${progressPct(item)}%`
                : `#${item.queue_pos}`}
            </span>
            {item.status === "caching" && (
              <span
                className="download-dock-bar"
                style={{ width: `${progressPct(item)}%` }}
              />
            )}
          </div>
        ))}
        {extra > 0 && (
          <div className="download-dock-more">+{extra} 项排队</div>
        )}
      </div>
    </div>
  );
}

export default function App() {
  const [items, setItems] = useState<Item[]>([]);
  const [apiReady, setApiReady] = useState(false);
  const [importing, setImporting] = useState(false);
  const [importError, setImportError] = useState("");
  const [importTotal, setImportTotal] = useState(0);
  const [importDone, setImportDone] = useState(0);
  const [importItems, setImportItems] = useState(0);
  const [importPhase, setImportPhase] = useState("");
  const [importSource, setImportSource] = useState("");
  const [importDetail, setImportDetail] = useState("");
  const [downloadingCount, setDownloadingCount] = useState(0);
  const [queuedCount, setQueuedCount] = useState(0);
  const [coverLoadingCount, setCoverLoadingCount] = useState(0);
  const coverLoadingRef = useRef(new Set<string>());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [rangeType, setRangeType] = useState<RangeType>("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [player, setPlayer] = useState<PlayerState>(null);
  const [previewOrigin, setPreviewOrigin] = useState<DOMRect | null>(null);
  const [previewClosing, setPreviewClosing] = useState(false);
  const [playError, setPlayError] = useState("");

  const onCoverLoadingChange = useCallback((id: string, loading: boolean) => {
    const set = coverLoadingRef.current;
    if (loading) set.add(id);
    else set.delete(id);
    setCoverLoadingCount(set.size);
  }, []);

  const applyPayload = (payload: ItemsPayload) => {
    setItems(payload.items ?? []);
    setImporting(payload.importing);
    setImportError(payload.import_error ?? "");
    setImportTotal(payload.import_total ?? 0);
    setImportDone(payload.import_done ?? 0);
    setImportItems(payload.import_items ?? payload.items?.length ?? 0);
    setImportPhase(payload.import_phase ?? "");
    setImportSource(payload.import_source ?? "");
    setImportDetail(payload.import_detail ?? "");
    setDownloadingCount(payload.downloading_count ?? 0);
    setQueuedCount(payload.queued_count ?? 0);
    setApiReady(true);
  };

  useEffect(() => {
    let alive = true;
    fetchItems()
      .then((payload) => {
        if (!alive) return;
        applyPayload(payload);
      })
      .catch((err: Error) => {
        if (alive) {
          setApiReady(true);
          setError(err.message || "无法连接 API，请先启动 tdl web");
        }
      });

    const stop = subscribeEvents((payload) => {
      applyPayload(payload);
      setError("");
    });
    return () => {
      alive = false;
      stop();
    };
  }, []);

  const displayItems = useMemo(() => [...items].reverse(), [items]);
  const videos = useMemo(
    () => displayItems.filter((i) => i.type === "video"),
    [displayItems],
  );
  const images = useMemo(
    () => displayItems.filter((i) => i.type === "image"),
    [displayItems],
  );
  const files = useMemo(
    () => displayItems.filter((i) => i.type === "file"),
    [displayItems],
  );
  const mediaItems = useMemo(() => [...videos, ...images], [videos, images]);
  const done = items.filter((i) => i.status === "completed").length;
  const livePlayer = useMemo(() => {
    if (!player) return null;
    const fresh = items.find((i) => i.id === player.item.id);
    return fresh ? { ...player, item: fresh } : player;
  }, [player, items]);

  // Prefer item.error from SSE as the single play-failure message.
  useEffect(() => {
    if (!livePlayer) return;
    const err = livePlayer.item.error?.trim();
    if (livePlayer.item.status === "error" && err) {
      setPlayError(`无法播放：${err}`);
      return;
    }
    if (livePlayer.item.status !== "error") {
      setPlayError("");
    }
  }, [livePlayer?.item.id, livePlayer?.item.status, livePlayer?.item.error]);

  async function onImport() {
    if (!file) return;
    setBusy(true);
    setError("");
    try {
      await importJSON(file, rangeType, from, to);
      setImporting(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  async function onCache(item: Item) {
    try {
      await cacheItem(item.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function onPause(item: Item) {
    try {
      await pauseItem(item.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  async function onDownloadAll() {
    try {
      await downloadItems([]);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  }

  function getPreviewSourceEl(target: HTMLElement): HTMLElement {
    return (
      (target.closest("[data-preview-source]") as HTMLElement | null) ??
      (target.querySelector("[data-preview-source]") as HTMLElement | null) ??
      target
    );
  }

  function openPlayer(
    kind: "video" | "image",
    item: Item,
    target: HTMLElement,
  ) {
    setPlayError("");
    setPreviewClosing(false);
    setPreviewOrigin(getPreviewSourceEl(target).getBoundingClientRect());
    setPlayer({ kind, item });
  }

  function requestClosePlayer() {
    setPreviewClosing(true);
  }

  async function finalizeClosePlayer() {
    const current = livePlayer;
    setPlayer(null);
    setPreviewOrigin(null);
    setPreviewClosing(false);
    setPlayError("");
    if (current?.kind === "video" && current.item.status === "caching") {
      try {
        await pauseItem(current.item.id);
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      }
    }
  }

  return (
    <>
      {apiReady && mediaItems.length > 0 && (
        <FilmBackground items={mediaItems} />
      )}
      {!(livePlayer?.kind === "video" && !previewClosing) && (
      <header className="topbar">
        <div className="topbar-inner">
          <div className="brand">
            tdl <span>PREVIEW</span>
          </div>
          <div className="stats">
            {items.length} 项 · {done} 已完成
          </div>
        </div>
      </header>
      )}
      <div className="app">

      <StatusBar
        apiReady={apiReady}
        importing={importing}
        importPhase={importPhase}
        importSource={importSource}
        importDetail={importDetail}
        importDone={importDone}
        importTotal={importTotal}
        importItems={importItems}
        downloadingCount={downloadingCount}
        queuedCount={queuedCount}
        coverLoadingCount={coverLoadingCount}
        itemCount={items.length}
      />

      <DownloadDock items={items} />

      <section className="toolbar">
        <div className="field">
          <label>JSON 导出</label>
          <input
            type="file"
            accept=".json,application/json"
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
        </div>
        <div className="field">
          <label>范围类型</label>
          <select
            value={rangeType}
            onChange={(e) => setRangeType(e.target.value as RangeType)}
          >
            <option value="">全部</option>
            <option value="id">消息 ID</option>
            <option value="time">时间戳</option>
          </select>
        </div>
        <div className="field">
          <label>From</label>
          <input
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            placeholder="起始"
            disabled={!rangeType}
          />
        </div>
        <div className="field">
          <label>To</label>
          <input
            value={to}
            onChange={(e) => setTo(e.target.value)}
            placeholder="结束"
            disabled={!rangeType}
          />
        </div>
        <button className="btn" disabled={!file || busy || importing} onClick={onImport}>
          {busy ? "导入中…" : "导入"}
        </button>
        <button
          className="btn ghost"
          disabled={!items.length || importing}
          onClick={onDownloadAll}
        >
          下载全部
        </button>
      </section>

      {importError && <div className="banner error">{importError}</div>}
      {error && !(livePlayer && playError) && (
        <div className="banner error">{error}</div>
      )}

      {!apiReady ? (
        <AppSkeleton />
      ) : (
        <>
      <section className="section">
        <h2>Videos</h2>
        {videos.length === 0 ? (
          <div className="empty">
            {importing ? "正在加载视频列表…" : "暂无视频。导入 JSON 后会显示封面墙。"}
          </div>
        ) : (
          <VirtualMasonry
            className="video-grid masonry-flow"
            items={videos}
            minColumnWidth={280}
            gap={16}
            estimateSize={320}
            renderItem={(item) => (
              <div className="card-wrap">
                <button
                  type="button"
                  className="card"
                  onClick={(e) =>
                    openPlayer("video", item, e.currentTarget)
                  }
                >
                  <StatusBadge item={item} />
                  <div className="card-cover">
                    <LazyCover
                      className="poster"
                      src={coverURL(item.cover || item.thumb_url)}
                      alt={displayName(item)}
                      coverId={item.id}
                      coverPriority="high"
                      previewSourceId={item.id}
                      previewHidden={livePlayer?.item.id === item.id}
                      onLoadingChange={onCoverLoadingChange}
                    />
                    <div className="card-overlay">
                      <div className="card-title">{displayName(item)}</div>
                      <div className="card-meta">
                        <div className="card-sub">
                          {[
                            formatMessageDate(item.date),
                            formatDuration(item.duration),
                            formatSize(item.size),
                          ]
                            .filter(Boolean)
                            .join(" · ")}
                        </div>
                        {item.status === "queued" && (
                          <div className="card-status">{statusLabel(item)}</div>
                        )}
                      </div>
                      {(item.status === "caching" ||
                        item.status === "paused" ||
                        item.progress > 0) &&
                        item.status !== "completed" && (
                          <div className="progress">
                            <span style={{ width: `${progressPct(item)}%` }} />
                          </div>
                        )}
                    </div>
                  </div>
                </button>
              </div>
            )}
          />
        )}
      </section>

      <section className="section">
        <h2>Images</h2>
        {images.length === 0 ? (
          <div className="empty">
            {importing ? "正在加载图片列表…" : "暂无图片。"}
          </div>
        ) : (
          <VirtualMasonry
            className="image-grid masonry-flow"
            items={images}
            minColumnWidth={160}
            gap={12}
            estimateSize={200}
            renderItem={(item) => (
              <button
                type="button"
                className="image-tile"
                onClick={(e) => openPlayer("image", item, e.currentTarget)}
                title={`${displayName(item)} · ${statusLabel(item)}${
                  item.date ? ` · ${formatMessageDate(item.date)}` : ""
                }`}
              >
                <StatusBadge item={item} />
                <LazyCover
                  className=""
                  fallbackClass="tile-fallback"
                  fallbackText="No Image"
                  src={coverURL(item.thumb_url || item.preview_url)}
                  alt={displayName(item)}
                  coverId={item.id}
                  previewSourceId={item.id}
                  previewHidden={livePlayer?.item.id === item.id}
                  onLoadingChange={onCoverLoadingChange}
                />
              </button>
            )}
          />
        )}
      </section>

      <section className="section">
        <h2>Files</h2>
        {files.length === 0 ? (
          <div className="empty">暂无压缩包或其它文件。</div>
        ) : (
          <div className="file-list">
            {files.map((item) => (
              <div key={item.id} className="file-row">
                <div>
                  <strong>{displayName(item)}</strong>
                  <div className="muted">
                    {[
                      formatMessageDate(item.date),
                      formatSize(item.size),
                      statusLabel(item),
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </div>
                  {(item.status === "caching" || item.status === "paused") && (
                    <div className="progress">
                      <span style={{ width: `${progressPct(item)}%` }} />
                    </div>
                  )}
                </div>
                <span className="muted">{item.status}</span>
                <div className="file-actions">
                  {item.status === "caching" ? (
                    <button className="btn ghost" onClick={() => onPause(item)}>
                      暂停
                    </button>
                  ) : (
                    <button
                      className="btn ghost"
                      disabled={item.status === "completed"}
                      onClick={() => onCache(item)}
                    >
                      {item.status === "completed"
                        ? "已落盘"
                        : item.status === "paused"
                          ? "继续下载"
                          : "下载到目录"}
                    </button>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </section>
        </>
      )}

    </div>
      {livePlayer && previewOrigin && (
        <MediaPreview
          player={livePlayer}
          originRect={previewOrigin}
          thumbSrc={coverURL(
            livePlayer.item.cover ||
              livePlayer.item.thumb_url ||
              livePlayer.item.preview_url,
          )}
          aspectRatio={coverAspectCache.get(livePlayer.item.id)}
          closing={previewClosing}
          playError={playError}
          onPlayError={setPlayError}
          onCloseRequest={requestClosePlayer}
          onClosed={() => void finalizeClosePlayer()}
          onPause={onPause}
        />
      )}
    </>
  );
}

function StatusBadge({ item }: { item: Item }) {
  if (item.status === "completed") {
    return null;
  }
  if (item.status === "caching") {
    return <span className="badge busy">下载中 {progressPct(item)}%</span>;
  }
  if (item.status === "error") {
    return <span className="badge busy">错误</span>;
  }
  if ((item.queue_pos ?? 0) > 0) {
    return <span className="badge queue">排队 #{item.queue_pos}</span>;
  }
  if (item.status === "paused") {
    return <span className="badge paused">{progressPct(item)}%</span>;
  }
  if (item.resume_completed) {
    return <span className="badge done">RESUME</span>;
  }
  return null;
}
