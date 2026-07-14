import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type MouseEvent as ReactMouseEvent,
  type PointerEvent as ReactPointerEvent,
} from "react";
import { coverURL } from "./api";
import { ensureCoverResource, type CoverResource } from "./coverResource";
import type { Item } from "./types";

const COLUMN_COUNT = 14;
const FRAMES_PER_COL = 16;
const FILM_ACCELERATE_MS = 1200;
const FILM_OBSCURED_HOLD_MS = 1400;
const FILM_DECELERATE_MS = 2600;
const FILM_INITIAL_ENTER_MS = 5000;
const FILM_FAST_SPEED_MULTIPLIER = 48;
const FILM_ROTATION = -Math.PI / 4;
const FILM_BASE_OPACITY = 0.17;
const FILM_OBSCURED_OPACITY = 0.2;
const FILM_BLUR_PX = 26;
const MAX_DPR = 1.5;
const FILM_CACHE_PIXEL_BUDGET = 8_000_000;
const FILM_CACHE_MIN_SCALE = 0.25;

type FilmFrameData = {
  item: Item;
  url: string;
};

type FilmLayerData = {
  signature: string;
  columnFrames: FilmFrameData[][];
  coverCount: number;
  urls: string[];
};

type RenderedFilmColumn = {
  canvas: HTMLCanvasElement;
};

type RenderedFilmLayer = {
  signature: string;
  columns: Array<RenderedFilmColumn | null>;
};

type FilmMotionPhase = "idle" | "accelerating" | "obscured" | "decelerating";
type FilmInitialPhase = "waiting" | "entering" | "done";

type CanvasSize = {
  width: number;
  height: number;
  dpr: number;
};

type FilmMetrics = {
  filmSize: number;
  columnGap: number;
  columnWidth: number;
  sprocketWidth: number;
  frameHeight: number;
  pitch: number;
  cycle: number;
};

export type FilmClickDetail = {
  item: Item;
  frameKey: string;
  originRect: DOMRectReadOnly;
};

function coverPath(item: Item): string | undefined {
  if (item.type === "video") return item.cover || item.thumb_url;
  return item.thumb_url || item.preview_url;
}

function hashId(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i += 1) h = (h * 31 + id.charCodeAt(i)) | 0;
  return h;
}

function sortedPool(items: Item[]): Item[] {
  return [...items].sort((a, b) => hashId(a.id) - hashId(b.id));
}

function sourceSignature(items: Item[]): string {
  return items
    .map((item) => item.id)
    .sort()
    .join("|");
}

/** Each column draws from its own non-overlapping item slice. */
function pickColumnFrames(allItems: Item[], colIndex: number): FilmFrameData[] {
  const withCover = allItems.filter((i) => coverPath(i));
  if (!withCover.length) return [];

  const half = Math.ceil(COLUMN_COUNT / 2);
  const videos = withCover.filter((i) => i.type === "video");
  const images = withCover.filter((i) => i.type === "image");

  let pool: Item[];
  let slot: number;
  let slots: number;

  if (colIndex < half) {
    pool = videos.length > 0 ? videos : withCover;
    slot = colIndex;
    slots = half;
  } else {
    pool = images.length > 0 ? images : withCover;
    slot = colIndex - half;
    slots = COLUMN_COUNT - half;
  }

  const partitioned = sortedPool(pool).filter((_, i) => i % slots === slot);
  const source =
    partitioned.length > 0
      ? partitioned
      : [sortedPool(pool)[slot % pool.length]];

  const frames: FilmFrameData[] = [];
  for (let i = 0; i < FRAMES_PER_COL; i += 1) {
    const item = source[i % source.length];
    const path = coverPath(item);
    if (path) frames.push({ item, url: coverURL(path) });
  }
  return frames;
}

function buildFilmLayer(items: Item[]): FilmLayerData {
  const columnFrames = Array.from({ length: COLUMN_COUNT }, (_, i) =>
    pickColumnFrames(items, i),
  );
  const urls = Array.from(
    new Set(columnFrames.flatMap((frames) => frames.map((frame) => frame.url))),
  );

  return {
    signature: sourceSignature(items),
    columnFrames,
    coverCount: columnFrames.reduce((sum, frames) => sum + frames.length, 0),
    urls,
  };
}

function mod(value: number, size: number): number {
  return ((value % size) + size) % size;
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}

function easeOutCubic(t: number): number {
  return 1 - Math.pow(1 - clamp(t, 0, 1), 3);
}

function easeInOutCubic(t: number): number {
  const p = clamp(t, 0, 1);
  return p < 0.5 ? 4 * p * p * p : 1 - Math.pow(-2 * p + 2, 3) / 2;
}

function frameKeyFor(layer: FilmLayerData, colIndex: number, frameIndex: number): string {
  return `${layer.signature}:${colIndex}:${frameIndex}`;
}

function computeMetrics(width: number, height: number): FilmMetrics {
  const filmSize = Math.max(width, height) * 2;
  const columnGap = 5;
  const columnWidth = (filmSize - columnGap * (COLUMN_COUNT - 1)) / COLUMN_COUNT;
  const sprocketWidth = Math.max(4, Math.min(8, columnWidth * 0.035));
  const imageWidth = Math.max(1, columnWidth - sprocketWidth * 2);
  const frameHeight = imageWidth * (16 / 9) + 4;
  const pitch = frameHeight + columnGap;

  return {
    filmSize,
    columnGap,
    columnWidth,
    sprocketWidth,
    frameHeight,
    pitch,
    cycle: pitch * FRAMES_PER_COL,
  };
}

function renderedLayerSizeKey(size: CanvasSize): string {
  return `${size.width}x${size.height}`;
}

function renderedLayerScale(
  layer: FilmLayerData,
  metrics: FilmMetrics,
): number {
  const pixels = layer.columnFrames.reduce(
    (sum, frames) =>
      sum + metrics.columnWidth * metrics.pitch * frames.length,
    0,
  );
  if (pixels <= FILM_CACHE_PIXEL_BUDGET) return 1;
  return clamp(
    Math.sqrt(FILM_CACHE_PIXEL_BUDGET / pixels),
    FILM_CACHE_MIN_SCALE,
    1,
  );
}

function objectFitCoverRect(
  image: HTMLImageElement,
  dx: number,
  dy: number,
  dw: number,
  dh: number,
) {
  const iw = image.naturalWidth || image.width;
  const ih = image.naturalHeight || image.height;
  if (iw <= 0 || ih <= 0) return null;

  const scale = Math.max(dw / iw, dh / ih);
  const sw = dw / scale;
  const sh = dh / scale;
  return {
    sx: (iw - sw) / 2,
    sy: (ih - sh) / 2,
    sw,
    sh,
    dx,
    dy,
    dw,
    dh,
  };
}

function contentRectToScreenRect(
  rect: { x: number; y: number; width: number; height: number },
  metrics: FilmMetrics,
  size: CanvasSize,
): DOMRectReadOnly {
  const cos = Math.cos(FILM_ROTATION);
  const sin = Math.sin(FILM_ROTATION);
  const localX = rect.x + rect.width / 2 - metrics.filmSize / 2;
  const localY = rect.y + rect.height / 2 - metrics.filmSize / 2;
  const centerX = size.width / 2 + localX * cos - localY * sin;
  const centerY = size.height / 2 + localX * sin + localY * cos;
  return new DOMRect(
    centerX - rect.width / 2,
    centerY - rect.height / 2,
    rect.width,
    rect.height,
  );
}

export function FilmBackground({
  items,
  onItemClick,
}: {
  items: Item[];
  onItemClick?: (detail: FilmClickDetail) => void;
}) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null);
  const activeLayerRef = useRef<FilmLayerData>(buildFilmLayer(items));
  const pendingLayerRef = useRef<FilmLayerData | null>(null);
  const activeRenderedLayerRef = useRef<RenderedFilmLayer | null>(null);
  const pendingRenderedLayerRef = useRef<RenderedFilmLayer | null>(null);
  const activeRenderTokenRef = useRef(0);
  const pendingRenderTokenRef = useRef(0);
  const pendingLayerReadyRef = useRef(false);
  const motionPhaseRef = useRef<FilmMotionPhase>("idle");
  const motionPhaseStartRef = useRef(0);
  const initialPhaseRef = useRef<FilmInitialPhase>("waiting");
  const initialStartRef = useRef(0);
  const initialTokenRef = useRef(0);
  const offsetsRef = useRef<number[]>(Array(COLUMN_COUNT).fill(0));
  const speedMultiplierRef = useRef(1);
  const sideHoverRef = useRef(false);
  const lastPointerRef = useRef<{ x: number; y: number } | null>(null);
  const rafRef = useRef<number | null>(null);
  const lastFrameRef = useRef<number | null>(null);
  const canvasSizeRef = useRef<CanvasSize | null>(null);
  const obscureTimerRef = useRef<number | null>(null);
  const holdTimerRef = useRef<number | null>(null);
  const endTimerRef = useRef<number | null>(null);
  const initialEndTimerRef = useRef<number | null>(null);
  const [reducedMotion, setReducedMotion] = useState(false);
  const reducedMotionRef = useRef(false);
  const nextLayer = useMemo(() => buildFilmLayer(items), [items]);

  function ensureImage(url: string): CoverResource {
    return ensureCoverResource(url);
  }

  function preloadLayer(layer: FilmLayerData): Promise<void> {
    if (layer.urls.length === 0) return Promise.resolve();
    return Promise.all(layer.urls.map((url) => ensureImage(url).promise)).then(
      () => undefined,
    );
  }

  function yieldToIdle(): Promise<void> {
    return new Promise((resolve) => {
      if (typeof window.requestIdleCallback === "function") {
        window.requestIdleCallback(() => resolve());
      } else {
        window.setTimeout(resolve, 0);
      }
    });
  }

  function releaseRenderedColumns(
    columns: Array<RenderedFilmColumn | null>,
  ) {
    for (const column of columns) {
      if (!column) continue;
      column.canvas.width = 0;
      column.canvas.height = 0;
    }
  }

  function scheduleRenderedLayerRelease(layer: RenderedFilmLayer | null) {
    if (!layer) return;
    const release = () => releaseRenderedColumns(layer.columns);
    if (typeof window.requestIdleCallback === "function") {
      window.requestIdleCallback(release);
    } else {
      window.setTimeout(release, 0);
    }
  }

  async function buildRenderedLayer(
    layer: FilmLayerData,
    tokenRef: { current: number },
  ): Promise<RenderedFilmLayer | null> {
    const token = (tokenRef.current += 1);
    await preloadLayer(layer);
    if (tokenRef.current !== token) return null;

    const canvas = canvasRef.current;
    if (!canvas) return null;
    const size = canvasSizeRef.current ?? resizeCanvas(canvas);
    const sizeKey = renderedLayerSizeKey(size);
    const metrics = computeMetrics(size.width, size.height);
    const scale = renderedLayerScale(layer, metrics);
    const columns: Array<RenderedFilmColumn | null> = Array(
      COLUMN_COUNT,
    ).fill(null);

    for (let colIndex = 0; colIndex < COLUMN_COUNT; colIndex += 1) {
      await yieldToIdle();
      if (tokenRef.current !== token) {
        scheduleRenderedLayerRelease({ signature: layer.signature, columns });
        return null;
      }
      const frames = layer.columnFrames[colIndex] ?? [];
      if (frames.length > 0) {
        const cycle = metrics.pitch * frames.length;
        const column = document.createElement("canvas");
        column.width = Math.max(1, Math.round(metrics.columnWidth * scale));
        column.height = Math.max(1, Math.round(cycle * scale));
        const ctx = column.getContext("2d");
        if (ctx) {
          ctx.setTransform(scale, 0, 0, scale, 0, 0);
          for (let frameIndex = 0; frameIndex < frames.length; frameIndex += 1) {
            drawFrame(
              ctx,
              frames[frameIndex],
              frameIndex,
              0,
              frameIndex * metrics.pitch,
              metrics,
            );
          }
          columns[colIndex] = { canvas: column };
        }
      }
    }

    if (
      tokenRef.current !== token ||
      renderedLayerSizeKey(canvasSizeRef.current ?? size) !== sizeKey
    ) {
      scheduleRenderedLayerRelease({ signature: layer.signature, columns });
      return null;
    }
    return { signature: layer.signature, columns };
  }

  function clearMotionTimers() {
    if (obscureTimerRef.current != null) {
      window.clearTimeout(obscureTimerRef.current);
      obscureTimerRef.current = null;
    }
    if (holdTimerRef.current != null) {
      window.clearTimeout(holdTimerRef.current);
      holdTimerRef.current = null;
    }
    if (endTimerRef.current != null) {
      window.clearTimeout(endTimerRef.current);
      endTimerRef.current = null;
    }
  }

  function setMotionPhase(phase: FilmMotionPhase) {
    motionPhaseRef.current = phase;
    motionPhaseStartRef.current = performance.now();
  }

  function swapPendingLayer() {
    const pending = pendingLayerRef.current;
    const rendered = pendingRenderedLayerRef.current;
    if (
      !pending ||
      !rendered ||
      pending.coverCount <= 0 ||
      !pendingLayerReadyRef.current
    ) {
      return false;
    }

    const previousRendered = activeRenderedLayerRef.current;
    activeLayerRef.current = pending;
    activeRenderedLayerRef.current = rendered;
    pendingLayerRef.current = null;
    pendingRenderedLayerRef.current = null;
    pendingLayerReadyRef.current = false;
    if (previousRendered !== rendered) {
      scheduleRenderedLayerRelease(previousRendered);
    }
    return true;
  }

  function scheduleDeceleration() {
    if (holdTimerRef.current != null) window.clearTimeout(holdTimerRef.current);
    if (endTimerRef.current != null) window.clearTimeout(endTimerRef.current);

    holdTimerRef.current = window.setTimeout(() => {
      holdTimerRef.current = null;
      setMotionPhase("decelerating");
      endTimerRef.current = window.setTimeout(() => {
        endTimerRef.current = null;
        setMotionPhase("idle");
      }, FILM_DECELERATE_MS);
    }, FILM_OBSCURED_HOLD_MS);
  }

  function trySwapAndRecover() {
    if (motionPhaseRef.current !== "obscured") return;
    if (!swapPendingLayer()) return;
    scheduleDeceleration();
  }

  function enterObscuredPhase() {
    if (obscureTimerRef.current != null) {
      window.clearTimeout(obscureTimerRef.current);
      obscureTimerRef.current = null;
    }
    if (holdTimerRef.current != null) {
      window.clearTimeout(holdTimerRef.current);
      holdTimerRef.current = null;
    }
    if (endTimerRef.current != null) {
      window.clearTimeout(endTimerRef.current);
      endTimerRef.current = null;
    }
    setMotionPhase("obscured");
    trySwapAndRecover();
  }

  function startMotionCycle() {
    if (motionPhaseRef.current === "idle") {
      clearMotionTimers();
      setMotionPhase("accelerating");
      obscureTimerRef.current = window.setTimeout(
        enterObscuredPhase,
        FILM_ACCELERATE_MS,
      );
      return;
    }

    if (motionPhaseRef.current === "accelerating") return;
    enterObscuredPhase();
  }

  function startInitialEnter() {
    if (initialPhaseRef.current !== "waiting") return;
    initialPhaseRef.current = "entering";
    initialStartRef.current = performance.now();
    if (initialEndTimerRef.current != null) {
      window.clearTimeout(initialEndTimerRef.current);
    }
    initialEndTimerRef.current = window.setTimeout(() => {
      initialEndTimerRef.current = null;
      initialPhaseRef.current = "done";
    }, FILM_INITIAL_ENTER_MS);
  }

  function waitForInitialLayer(layer: FilmLayerData) {
    const token = (initialTokenRef.current += 1);
    if (reducedMotionRef.current) {
      initialPhaseRef.current = "done";
    } else {
      initialPhaseRef.current = "waiting";
    }
    void buildRenderedLayer(layer, activeRenderTokenRef).then((rendered) => {
      if (
        !rendered ||
        initialTokenRef.current !== token ||
        activeLayerRef.current.signature !== layer.signature
      ) {
        return;
      }
      const previous = activeRenderedLayerRef.current;
      activeRenderedLayerRef.current = rendered;
      if (previous !== rendered) {
        scheduleRenderedLayerRelease(previous);
      }
      if (!reducedMotionRef.current) startInitialEnter();
    });
  }

  function resizeCanvas(
    canvas: HTMLCanvasElement,
    cssWidth = canvas.clientWidth,
    cssHeight = canvas.clientHeight,
  ): CanvasSize {
    const width = Math.max(1, Math.round(cssWidth || window.innerWidth));
    const height = Math.max(1, Math.round(cssHeight || window.innerHeight));
    const dpr = Math.min(window.devicePixelRatio || 1, MAX_DPR);
    const pixelWidth = Math.round(width * dpr);
    const pixelHeight = Math.round(height * dpr);
    if (canvas.width !== pixelWidth || canvas.height !== pixelHeight) {
      canvas.width = pixelWidth;
      canvas.height = pixelHeight;
    }
    const size = { width, height, dpr };
    canvasSizeRef.current = size;
    return size;
  }

  function isSidePoint(clientX: number) {
    const gutter = Math.max(0, (window.innerWidth - 1400) / 2);
    return (
      gutter > 0 &&
      (clientX <= gutter || clientX >= window.innerWidth - gutter)
    );
  }

  function columnIntroDelta(colIndex: number, metrics: FilmMetrics, now: number) {
    if (initialPhaseRef.current === "done") return 0;
    if (initialPhaseRef.current === "waiting") {
      return colIndex % 2 === 0 ? -metrics.filmSize * 1.2 : metrics.filmSize * 1.2;
    }
    const progress = (now - initialStartRef.current) / FILM_INITIAL_ENTER_MS;
    const eased = easeOutCubic(progress);
    const start = colIndex % 2 === 0 ? -metrics.filmSize * 1.2 : metrics.filmSize * 1.2;
    return lerp(start, 0, eased);
  }

  function hitTest(clientX: number, clientY: number) {
    if (initialPhaseRef.current === "waiting") return null;
    const canvas = canvasRef.current;
    if (!canvas) return null;
    const size = canvasSizeRef.current ?? resizeCanvas(canvas);
    const metrics = computeMetrics(size.width, size.height);
    const layer = activeLayerRef.current;
    if (layer.coverCount <= 0) return null;

    const dx = clientX - size.width / 2;
    const dy = clientY - size.height / 2;
    const cos = Math.cos(-FILM_ROTATION);
    const sin = Math.sin(-FILM_ROTATION);
    const filmX = dx * cos - dy * sin;
    const filmY = dx * sin + dy * cos;
    const contentX = filmX + metrics.filmSize / 2;
    const contentY = filmY + metrics.filmSize / 2;
    if (
      contentX < 0 ||
      contentY < 0 ||
      contentX > metrics.filmSize ||
      contentY > metrics.filmSize
    ) {
      return null;
    }

    const colStep = metrics.columnWidth + metrics.columnGap;
    const colIndex = Math.floor(contentX / colStep);
    if (colIndex < 0 || colIndex >= COLUMN_COUNT) return null;
    const xInCol = contentX - colIndex * colStep;
    if (xInCol < 0 || xInCol > metrics.columnWidth) return null;

    const frames = layer.columnFrames[colIndex] ?? [];
    if (!frames.length) return null;

    const now = performance.now();
    const trackY = contentY - columnIntroDelta(colIndex, metrics, now);
    const cycle = metrics.pitch * frames.length;
    const inCycle = mod(trackY + offsetsRef.current[colIndex], cycle);
    const frameIndex = Math.floor(inCycle / metrics.pitch);
    const yInFrame = inCycle - frameIndex * metrics.pitch;
    if (yInFrame > metrics.frameHeight) return null;

    const cycleBase = Math.floor((trackY + offsetsRef.current[colIndex] - inCycle) / cycle) * cycle;
    const frameY =
      cycleBase +
      frameIndex * metrics.pitch -
      offsetsRef.current[colIndex] +
      columnIntroDelta(colIndex, metrics, now);
    const frameX = colIndex * colStep;
    const frame = frames[frameIndex];
    if (!frame || frame.item.type === "file") return null;

    return {
      item: frame.item,
      frameKey: frameKeyFor(layer, colIndex, frameIndex),
      originRect: contentRectToScreenRect(
        {
          x: frameX,
          y: frameY,
          width: metrics.columnWidth,
          height: metrics.frameHeight,
        },
        metrics,
        size,
      ),
    };
  }

  function updateSideHover(clientX: number, clientY: number) {
    lastPointerRef.current = { x: clientX, y: clientY };
    sideHoverRef.current = isSidePoint(clientX);
    return sideHoverRef.current ? hitTest(clientX, clientY) : null;
  }

  function handlePointerMove(event: ReactPointerEvent<HTMLElement>) {
    updateSideHover(event.clientX, event.clientY);
  }

  function handlePointerLeave() {
    sideHoverRef.current = false;
  }

  function handleSideClick(event: ReactMouseEvent<HTMLElement>) {
    const hit = updateSideHover(event.clientX, event.clientY);
    if (!hit) return;
    onItemClick?.(hit);
  }

  function updateSideHoverFromLastPointer() {
    const point = lastPointerRef.current;
    sideHoverRef.current = point ? isSidePoint(point.x) : false;
  }

  function motionVisuals(now: number) {
    const phase = motionPhaseRef.current;
    const elapsed = now - motionPhaseStartRef.current;
    if (phase === "accelerating") {
      const p = easeInOutCubic(elapsed / FILM_ACCELERATE_MS);
      return {
        blur: FILM_BLUR_PX * p,
        opacity: lerp(FILM_BASE_OPACITY, FILM_OBSCURED_OPACITY, p),
        speed: lerp(1, FILM_FAST_SPEED_MULTIPLIER, p),
      };
    }
    if (phase === "obscured") {
      return {
        blur: FILM_BLUR_PX,
        opacity: FILM_OBSCURED_OPACITY,
        speed: FILM_FAST_SPEED_MULTIPLIER,
      };
    }
    if (phase === "decelerating") {
      const p = easeOutCubic(elapsed / FILM_DECELERATE_MS);
      return {
        blur: lerp(FILM_BLUR_PX, 0, p),
        opacity: lerp(FILM_OBSCURED_OPACITY, FILM_BASE_OPACITY, p),
        speed: lerp(FILM_FAST_SPEED_MULTIPLIER, 1, p),
      };
    }
    return { blur: 0, opacity: FILM_BASE_OPACITY, speed: 1 };
  }

  function drawSprocket(
    ctx: CanvasRenderingContext2D,
    x: number,
    y: number,
    width: number,
    height: number,
  ) {
    ctx.fillStyle = "rgba(8, 8, 8, 0.95)";
    ctx.fillRect(x, y, width, height);
    ctx.fillStyle = "rgba(229, 9, 20, 0.16)";
    for (let yy = y + 4; yy < y + height; yy += 12) {
      ctx.fillRect(x, yy, width, 2);
    }
  }

  function drawFrame(
    ctx: CanvasRenderingContext2D,
    frame: FilmFrameData,
    frameIndex: number,
    x: number,
    y: number,
    metrics: FilmMetrics,
  ) {
    ctx.fillStyle = "rgba(12, 12, 12, 0.92)";
    ctx.fillRect(x, y, metrics.columnWidth, metrics.frameHeight);
    ctx.fillStyle = "rgba(229, 9, 20, 0.12)";
    ctx.fillRect(x, y, metrics.columnWidth, 2);
    ctx.fillRect(x, y + metrics.frameHeight - 2, metrics.columnWidth, 2);

    drawSprocket(ctx, x, y, metrics.sprocketWidth, metrics.frameHeight);
    drawSprocket(
      ctx,
      x + metrics.columnWidth - metrics.sprocketWidth,
      y,
      metrics.sprocketWidth,
      metrics.frameHeight,
    );

    const imageX = x + metrics.sprocketWidth;
    const imageY = y + 2;
    const imageW = metrics.columnWidth - metrics.sprocketWidth * 2;
    const imageH = metrics.frameHeight - 4;
    const imageOpacity = 0.85;

    ctx.fillStyle = "rgba(24, 24, 24, 0.9)";
    ctx.fillRect(imageX, imageY, imageW, imageH);

    const cached = ensureImage(frame.url);
    if (cached?.status === "ready") {
      const fit = objectFitCoverRect(cached.image, imageX, imageY, imageW, imageH);
      if (fit && imageOpacity > 0.001) {
        ctx.save();
        ctx.globalAlpha *= imageOpacity;
        ctx.beginPath();
        ctx.rect(imageX, imageY, imageW, imageH);
        ctx.clip();
        ctx.translate(imageX + imageW / 2, imageY + imageH / 2);
        ctx.drawImage(
          cached.image,
          fit.sx,
          fit.sy,
          fit.sw,
          fit.sh,
          -imageW / 2,
          -imageH / 2,
          imageW,
          imageH,
        );
        ctx.restore();
      }
    }

    ctx.fillStyle = "rgba(255, 255, 255, 0.25)";
    ctx.font = "10px sans-serif";
    ctx.textAlign = "right";
    ctx.textBaseline = "bottom";
    ctx.fillText(
      String(frameIndex + 1).padStart(3, "0"),
      x + metrics.columnWidth - 8,
      y + metrics.frameHeight - 5,
    );
  }

  function drawRenderedColumns(
    ctx: CanvasRenderingContext2D,
    layer: FilmLayerData,
    rendered: RenderedFilmLayer | null,
    metrics: FilmMetrics,
    now: number,
  ): boolean {
    if (!rendered || rendered.signature !== layer.signature) return false;
    for (let colIndex = 0; colIndex < COLUMN_COUNT; colIndex += 1) {
      const frames = layer.columnFrames[colIndex] ?? [];
      if (frames.length > 0 && !rendered.columns[colIndex]) return false;
    }

    const colStep = metrics.columnWidth + metrics.columnGap;
    for (let colIndex = 0; colIndex < COLUMN_COUNT; colIndex += 1) {
      const frames = layer.columnFrames[colIndex] ?? [];
      const column = rendered.columns[colIndex];
      if (!column || frames.length === 0) continue;

      const x = colIndex * colStep;
      const cycle = metrics.pitch * frames.length;
      const offset = mod(offsetsRef.current[colIndex], cycle);
      const introDelta = columnIntroDelta(colIndex, metrics, now);
      let y = -offset + introDelta;
      while (y > 0) y -= cycle;
      while (y + cycle < 0) y += cycle;
      for (; y < metrics.filmSize; y += cycle) {
        ctx.drawImage(
          column.canvas,
          0,
          0,
          column.canvas.width,
          column.canvas.height,
          x,
          y,
          metrics.columnWidth,
          cycle,
        );
      }
    }
    return true;
  }

  function draw(now: number, size: CanvasSize) {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;

    ctx.setTransform(size.dpr, 0, 0, size.dpr, 0, 0);
    ctx.clearRect(0, 0, size.width, size.height);

    if (initialPhaseRef.current === "waiting") return;

    const layer = activeLayerRef.current;
    if (layer.coverCount <= 0) return;

    const metrics = computeMetrics(size.width, size.height);
    ctx.save();
    ctx.translate(size.width / 2, size.height / 2);
    ctx.rotate(FILM_ROTATION);
    ctx.translate(-metrics.filmSize / 2, -metrics.filmSize / 2);
    ctx.beginPath();
    ctx.rect(0, 0, metrics.filmSize, metrics.filmSize);
    ctx.clip();

    ctx.fillStyle = "rgba(229, 9, 20, 0.025)";
    ctx.fillRect(0, 0, metrics.filmSize, metrics.filmSize);

    if (
      !drawRenderedColumns(
        ctx,
        layer,
        activeRenderedLayerRef.current,
        metrics,
        now,
      )
    ) {
      const colStep = metrics.columnWidth + metrics.columnGap;
      for (let colIndex = 0; colIndex < COLUMN_COUNT; colIndex += 1) {
        const frames = layer.columnFrames[colIndex] ?? [];
        if (!frames.length) continue;

        const x = colIndex * colStep;
        const cycle = metrics.pitch * frames.length;
        const offset = mod(offsetsRef.current[colIndex], cycle);
        const introDelta = columnIntroDelta(colIndex, metrics, now);
        const first = Math.floor(
          (-metrics.frameHeight - introDelta + offset) / metrics.pitch,
        );

        for (let n = first; ; n += 1) {
          const y = n * metrics.pitch - offset + introDelta;
          if (y > metrics.filmSize) break;
          if (y + metrics.frameHeight < 0) continue;
          const frameIndex = mod(n, frames.length);
          const frame = frames[frameIndex];
          if (!frame) continue;
          drawFrame(ctx, frame, frameIndex, x, y, metrics);
        }
      }
    }

    ctx.restore();
  }

  useEffect(() => {
    reducedMotionRef.current = reducedMotion;
    if (reducedMotion) {
      initialPhaseRef.current = "done";
      pendingLayerRef.current = null;
      scheduleRenderedLayerRelease(pendingRenderedLayerRef.current);
      pendingRenderedLayerRef.current = null;
      pendingRenderTokenRef.current += 1;
      pendingLayerReadyRef.current = false;
      clearMotionTimers();
      setMotionPhase("idle");
      speedMultiplierRef.current = 0;
    }
  }, [reducedMotion]);

  useEffect(() => {
    const media = window.matchMedia("(prefers-reduced-motion: reduce)");
    const updateReducedMotion = () => setReducedMotion(media.matches);
    updateReducedMotion();
    media.addEventListener("change", updateReducedMotion);
    return () => media.removeEventListener("change", updateReducedMotion);
  }, []);

  useEffect(() => {
    const current = activeLayerRef.current;
    if (nextLayer.coverCount === 0 && current.coverCount > 0) return;

    if (current.signature === nextLayer.signature) {
      activeLayerRef.current = nextLayer;
      return;
    }

    if (reducedMotionRef.current || initialPhaseRef.current !== "done") {
      activeLayerRef.current = nextLayer;
      scheduleRenderedLayerRelease(activeRenderedLayerRef.current);
      activeRenderedLayerRef.current = null;
      pendingLayerRef.current = null;
      scheduleRenderedLayerRelease(pendingRenderedLayerRef.current);
      pendingRenderedLayerRef.current = null;
      pendingRenderTokenRef.current += 1;
      pendingLayerReadyRef.current = false;
      waitForInitialLayer(nextLayer);
      return;
    }

    pendingLayerRef.current = nextLayer;
    scheduleRenderedLayerRelease(pendingRenderedLayerRef.current);
    pendingRenderedLayerRef.current = null;
    pendingLayerReadyRef.current = false;
    void buildRenderedLayer(nextLayer, pendingRenderTokenRef).then((rendered) => {
      if (
        !rendered ||
        pendingLayerRef.current?.signature !== nextLayer.signature
      ) {
        return;
      }
      pendingRenderedLayerRef.current = rendered;
      pendingLayerReadyRef.current = true;
      trySwapAndRecover();
    });
    startMotionCycle();
  }, [nextLayer]);

  useEffect(() => {
    if (initialPhaseRef.current === "waiting") {
      waitForInitialLayer(activeLayerRef.current);
    }
  }, []);

  useEffect(() => {
    const updateFromPointer = (event: PointerEvent) => {
      lastPointerRef.current = { x: event.clientX, y: event.clientY };
      updateSideHover(event.clientX, event.clientY);
    };
    const handlePointerOut = (event: PointerEvent) => {
      if (!event.relatedTarget) {
        lastPointerRef.current = null;
        sideHoverRef.current = false;
      }
    };
    window.addEventListener("pointermove", updateFromPointer);
    window.addEventListener("pointerdown", updateFromPointer);
    window.addEventListener("pointerout", handlePointerOut);
    return () => {
      window.removeEventListener("pointermove", updateFromPointer);
      window.removeEventListener("pointerdown", updateFromPointer);
      window.removeEventListener("pointerout", handlePointerOut);
    };
  }, []);

  const tabVisibleRef = useRef(
    typeof document !== "undefined"
      ? document.visibilityState === "visible"
      : true,
  );

  useEffect(() => {
    const onVisibilityChange = () => {
      tabVisibleRef.current = document.visibilityState === "visible";
      if (tabVisibleRef.current) lastFrameRef.current = null;
    };
    document.addEventListener("visibilitychange", onVisibilityChange);
    return () =>
      document.removeEventListener("visibilitychange", onVisibilityChange);
  }, []);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const rebuildRenderedLayers = () => {
      const active = activeLayerRef.current;
      if (initialPhaseRef.current === "waiting") {
        waitForInitialLayer(active);
      } else {
        void buildRenderedLayer(active, activeRenderTokenRef).then(
          (rendered) => {
            if (
              rendered &&
              activeLayerRef.current.signature === active.signature
            ) {
              const previous = activeRenderedLayerRef.current;
              activeRenderedLayerRef.current = rendered;
              if (previous !== rendered) {
                scheduleRenderedLayerRelease(previous);
              }
            }
          },
        );
      }

      const pending = pendingLayerRef.current;
      if (!pending) return;
      scheduleRenderedLayerRelease(pendingRenderedLayerRef.current);
      pendingRenderedLayerRef.current = null;
      pendingLayerReadyRef.current = false;
      void buildRenderedLayer(pending, pendingRenderTokenRef).then(
        (rendered) => {
          if (
            !rendered ||
            pendingLayerRef.current?.signature !== pending.signature
          ) {
            return;
          }
          pendingRenderedLayerRef.current = rendered;
          pendingLayerReadyRef.current = true;
          trySwapAndRecover();
        },
      );
    };
    const update = (
      cssWidth = canvas.clientWidth,
      cssHeight = canvas.clientHeight,
    ) => {
      const previous = canvasSizeRef.current;
      const size = resizeCanvas(canvas, cssWidth, cssHeight);
      if (
        previous &&
        previous.width === size.width &&
        previous.height === size.height
      ) {
        return;
      }
      rebuildRenderedLayers();
    };
    const observer = new ResizeObserver(([entry]) => {
      if (entry) {
        update(entry.contentRect.width, entry.contentRect.height);
      }
    });
    const updateFromWindow = () => update();

    update();
    observer.observe(canvas);
    window.addEventListener("resize", updateFromWindow);
    return () => {
      observer.disconnect();
      window.removeEventListener("resize", updateFromWindow);
      canvasSizeRef.current = null;
    };
  }, []);

  useEffect(() => {
    const tick = (now: number) => {
      if (!tabVisibleRef.current) {
        rafRef.current = window.requestAnimationFrame(tick);
        return;
      }
      const canvas = canvasRef.current;
      if (!canvas) {
        rafRef.current = window.requestAnimationFrame(tick);
        return;
      }
      const size = canvasSizeRef.current ?? resizeCanvas(canvas);
      const last = lastFrameRef.current ?? now;
      const dt = Math.min((now - last) / 1000, 0.05);
      lastFrameRef.current = now;
      updateSideHoverFromLastPointer();

      const visuals = reducedMotionRef.current
        ? { blur: 0, opacity: FILM_BASE_OPACITY, speed: 0 }
        : motionVisuals(now);
      const targetSpeed = sideHoverRef.current ? 0 : visuals.speed;
      const ease = 1 - Math.exp(-dt / 0.22);
      speedMultiplierRef.current +=
        (targetSpeed - speedMultiplierRef.current) * ease;

      if (!reducedMotionRef.current && initialPhaseRef.current !== "waiting") {
        const metrics = computeMetrics(size.width, size.height);
        for (let i = 0; i < COLUMN_COUNT; i += 1) {
          const frames = activeLayerRef.current.columnFrames[i] ?? [];
          if (!frames.length) continue;
          const cycle = metrics.pitch * frames.length;
          const baseDuration = 68 + i * 10;
          const direction = i % 2 === 1 ? -1 : 1;
          offsetsRef.current[i] = mod(
            offsetsRef.current[i] +
              direction *
                (cycle / baseDuration) *
                speedMultiplierRef.current *
                dt,
            cycle,
          );
        }
      }

      let opacity = visuals.opacity;
      if (initialPhaseRef.current === "waiting") {
        opacity = 0;
      } else if (initialPhaseRef.current === "entering") {
        const p = (now - initialStartRef.current) / FILM_INITIAL_ENTER_MS;
        opacity *= easeOutCubic(p / 0.18);
      }
      canvas.style.opacity = String(opacity);
      canvas.style.filter = visuals.blur > 0.1 ? `blur(${visuals.blur.toFixed(1)}px)` : "none";

      draw(now, size);
      rafRef.current = window.requestAnimationFrame(tick);
    };

    rafRef.current = window.requestAnimationFrame(tick);
    return () => {
      if (rafRef.current != null) {
        window.cancelAnimationFrame(rafRef.current);
        rafRef.current = null;
      }
      lastFrameRef.current = null;
    };
  }, []);

  useEffect(() => {
    return () => {
      initialTokenRef.current += 1;
      activeRenderTokenRef.current += 1;
      pendingRenderTokenRef.current += 1;
      releaseRenderedColumns(
        activeRenderedLayerRef.current?.columns ?? [],
      );
      releaseRenderedColumns(
        pendingRenderedLayerRef.current?.columns ?? [],
      );
      activeRenderedLayerRef.current = null;
      pendingRenderedLayerRef.current = null;
      clearMotionTimers();
      if (initialEndTimerRef.current != null) {
        window.clearTimeout(initialEndTimerRef.current);
      }
      if (rafRef.current != null) {
        window.cancelAnimationFrame(rafRef.current);
      }
    };
  }, []);

  return (
    <div
      className={[
        "film-bg-wrap",
        onItemClick ? "film-bg-wrap--interactive" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-hidden="true"
    >
      <canvas ref={canvasRef} className="film-bg-canvas" />
      {onItemClick && (
        <>
          <div
            className="film-bg-side-hit film-bg-side-hit--left"
            onPointerEnter={handlePointerMove}
            onPointerMove={handlePointerMove}
            onPointerLeave={handlePointerLeave}
            onClick={handleSideClick}
          />
          <div
            className="film-bg-side-hit film-bg-side-hit--right"
            onPointerEnter={handlePointerMove}
            onPointerMove={handlePointerMove}
            onPointerLeave={handlePointerLeave}
            onClick={handleSideClick}
          />
        </>
      )}
      <div className="film-bg-arc" />
    </div>
  );
}
