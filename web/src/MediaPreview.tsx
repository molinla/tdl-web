import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  displayName,
  formatDuration,
  formatMessageDate,
  formatSize,
  mediaURL,
  probeMediaError,
  progressPct,
  statusLabel,
} from "./api";
import type { Item } from "./types";

const TRANSITION_MS = 450;

type PlayerState = { kind: "video"; item: Item } | { kind: "image"; item: Item };

type Box = { top: number; left: number; width: number; height: number };

type Layout = {
  panelLeft: number;
  panelTop: number;
  panelWidth: number;
  panelHeight: number;
  media: Box;
};

function defaultAspect(kind: PlayerState["kind"]) {
  return kind === "video" ? 9 / 16 : 4 / 3;
}

function computeImageLayout(aspectHeightPerWidth: number): Layout {
  const pad = 24;
  const headH = 52;
  const footH = 48;
  const panelWidth = Math.min(960, window.innerWidth - pad * 2);
  const maxMediaH = Math.max(180, window.innerHeight * 0.68 - headH - footH);

  let mediaW = panelWidth;
  let mediaH = mediaW * aspectHeightPerWidth;
  if (mediaH > maxMediaH) {
    mediaH = maxMediaH;
    mediaW = mediaH / aspectHeightPerWidth;
  }

  const panelHeight = headH + mediaH + footH;
  const panelLeft = (window.innerWidth - panelWidth) / 2;
  const panelTop = (window.innerHeight - panelHeight) / 2;

  return {
    panelLeft,
    panelTop,
    panelWidth,
    panelHeight,
    media: {
      top: panelTop + headH,
      left: panelLeft + (panelWidth - mediaW) / 2,
      width: mediaW,
      height: mediaH,
    },
  };
}

/** Immersive video: one black stage sized to aspect, no outer panel chrome. */
function computeVideoLayout(aspectHeightPerWidth: number): Layout {
  const pad = 16;
  const maxW = window.innerWidth - pad * 2;
  const maxH = window.innerHeight - pad * 2;

  let width = maxW;
  let height = width * aspectHeightPerWidth;
  if (height > maxH) {
    height = maxH;
    width = height / aspectHeightPerWidth;
  }

  const left = (window.innerWidth - width) / 2;
  const top = (window.innerHeight - height) / 2;

  return {
    panelLeft: left,
    panelTop: top,
    panelWidth: width,
    panelHeight: height,
    media: { top, left, width, height },
  };
}

function computeLayout(
  kind: PlayerState["kind"],
  aspectHeightPerWidth: number,
): Layout {
  if (kind === "video") {
    return computeVideoLayout(aspectHeightPerWidth);
  }
  return computeImageLayout(aspectHeightPerWidth);
}

function rectToBox(rect: DOMRectReadOnly): Box {
  return {
    top: rect.top,
    left: rect.left,
    width: rect.width,
    height: rect.height,
  };
}

function resolveOriginRect(itemId: string, fallback: Box): Box {
  const el = document.querySelector<HTMLElement>(
    `[data-preview-source="${itemId}"]`,
  );
  if (!el) return fallback;
  return rectToBox(el.getBoundingClientRect());
}

function boxStyle(box: Box): CSSProperties {
  return {
    top: `${box.top}px`,
    left: `${box.left}px`,
    width: `${box.width}px`,
    height: `${box.height}px`,
  };
}

function PreviewPauseIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M9 6v12M15 6v12"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  );
}

function RingProgressButton({
  pct,
  onClick,
  label,
  interactive = true,
}: {
  pct: number;
  onClick?: () => void;
  label: string;
  interactive?: boolean;
}) {
  const size = 40;
  const stroke = 2;
  const radius = (size - stroke) / 2;
  const circumference = 2 * Math.PI * radius;
  const offset = circumference - (pct / 100) * circumference;
  const center = size / 2;

  const content = (
    <>
      <svg
        className="ring-progress-svg"
        width={size}
        height={size}
        viewBox={`0 0 ${size} ${size}`}
        aria-hidden="true"
      >
        <circle
          className="ring-progress-track"
          cx={center}
          cy={center}
          r={radius}
          fill="none"
          strokeWidth={stroke}
        />
        <circle
          className="ring-progress-fill"
          cx={center}
          cy={center}
          r={radius}
          fill="none"
          strokeWidth={stroke}
          strokeDasharray={circumference}
          strokeDashoffset={offset}
          transform={`rotate(-90 ${center} ${center})`}
        />
      </svg>
      <PreviewPauseIcon />
    </>
  );

  if (!interactive) {
    return (
      <div
        className="preview-icon-btn ring-progress-btn ring-progress-btn--static"
        role="progressbar"
        aria-valuenow={pct}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-label={label}
      >
        {content}
      </div>
    );
  }

  return (
    <button
      type="button"
      className="preview-icon-btn ring-progress-btn"
      onClick={onClick}
      aria-label={label}
    >
      {content}
    </button>
  );
}

function PreviewCloseIcon() {
  return (
    <svg width="20" height="20" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path
        d="M6 6l12 12M18 6L6 18"
        stroke="currentColor"
        strokeWidth="2"
        strokeLinecap="round"
      />
    </svg>
  );
}

export function MediaPreview({
  player,
  originRect,
  thumbSrc,
  aspectRatio,
  closing,
  playError,
  onPlayError,
  onCloseRequest,
  onClosed,
  onPause,
}: {
  player: PlayerState;
  originRect: DOMRectReadOnly;
  thumbSrc: string;
  aspectRatio?: number;
  closing: boolean;
  playError: string;
  onPlayError: (msg: string) => void;
  onCloseRequest: () => void;
  onClosed: () => void;
  onPause: (item: Item) => void;
}) {
  const originBox = useRef<Box>(rectToBox(originRect));
  const [phase, setPhase] = useState<"entering" | "open" | "leaving">(
    "entering",
  );
  const [layout, setLayout] = useState<Layout>(() =>
    computeLayout(
      player.kind,
      aspectRatio ?? defaultAspect(player.kind),
    ),
  );
  const [mediaBox, setMediaBox] = useState<Box>(originBox.current);
  const [chromeVisible, setChromeVisible] = useState(false);
  const [showVideo, setShowVideo] = useState(false);
  const [videoReady, setVideoReady] = useState(false);
  const reducedMotion = useRef(
    typeof window !== "undefined" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;
  const closeHandledRef = useRef(false);
  const videoRef = useRef<HTMLVideoElement>(null);

  const aspect = aspectRatio ?? defaultAspect(player.kind);

  useLayoutEffect(() => {
    originBox.current = rectToBox(originRect);
    const nextLayout = computeLayout(player.kind, aspect);
    setLayout(nextLayout);

    if (reducedMotion.current) {
      setMediaBox(nextLayout.media);
      setPhase("open");
      setChromeVisible(true);
      if (player.kind === "video") setShowVideo(true);
      return;
    }

    setMediaBox(originBox.current);
    setPhase("entering");
    setChromeVisible(false);
    setShowVideo(false);
    setVideoReady(false);

    const id = window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        setMediaBox(nextLayout.media);
        setPhase("open");
      });
    });
    return () => window.cancelAnimationFrame(id);
  }, [player.item.id, player.kind, originRect, aspect]);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onCloseRequest();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onCloseRequest]);

  useEffect(() => {
    if (!closing) return;

    closeHandledRef.current = false;
    videoRef.current?.pause();
    setVideoReady(false);
    setChromeVisible(false);
    setPhase("leaving");
    setMediaBox(resolveOriginRect(player.item.id, originBox.current));

    if (reducedMotion.current) {
      onClosedRef.current();
      return;
    }

    const waitMs = TRANSITION_MS;
    const t = window.setTimeout(() => {
      if (closeHandledRef.current) return;
      closeHandledRef.current = true;
      onClosedRef.current();
    }, waitMs);
    return () => window.clearTimeout(t);
  }, [closing, player.item.id, player.kind]);

  useEffect(() => {
    if (phase !== "open") return;
    if (player.kind === "video") {
      setShowVideo(true);
    }
    const t = window.setTimeout(
      () => setChromeVisible(true),
      reducedMotion.current ? 0 : TRANSITION_MS * 0.55,
    );
    return () => window.clearTimeout(t);
  }, [phase, player.kind]);

  useEffect(() => {
    const onResize = () => {
      if (phase !== "open") return;
      const nextLayout = computeLayout(player.kind, aspect);
      setLayout(nextLayout);
      setMediaBox(nextLayout.media);
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [phase, player.kind, aspect]);

  function handleMediaTransitionEnd(e: React.TransitionEvent) {
    if (phase !== "leaving" || closeHandledRef.current) return;
    if (e.propertyName !== "width" && e.propertyName !== "top") return;
    if (player.kind === "video") return;
    closeHandledRef.current = true;
    onClosedRef.current();
  }

  const item = player.item;
  const isOpen = phase === "open";
  const showVideoPlayer =
    player.kind === "video" && (showVideo || phase === "leaving");

  return (
    <div
      className={[
        "preview-root",
        isOpen || phase === "leaving" ? "preview-root--open" : "",
        player.kind === "video" ? "preview-root--video" : "preview-root--image",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <button
        type="button"
        className={[
          "preview-backdrop",
          isOpen ? "preview-backdrop--open" : "",
          phase === "leaving" ? "preview-backdrop--leaving" : "",
        ]
          .filter(Boolean)
          .join(" ")}
        aria-label="关闭预览"
        onClick={onCloseRequest}
      />

      <div
        className={[
          "preview-media",
          player.kind === "video" ? "preview-media--video" : "",
          isOpen ? "preview-media--open" : "",
          phase === "leaving" ? "preview-media--leaving" : "",
          videoReady && phase === "open" ? "preview-media--video-ready" : "",
        ]
          .filter(Boolean)
          .join(" ")}
        style={boxStyle(mediaBox)}
        onTransitionEnd={handleMediaTransitionEnd}
      >
        {player.kind === "video" ? (
          <div className="preview-media-stack">
            <img
              src={thumbSrc}
              alt={displayName(item)}
              className="preview-media-layer preview-media-layer--poster"
              draggable={false}
            />
            {showVideoPlayer && (
              <video
                ref={videoRef}
                key={item.id}
                className="preview-media-layer preview-media-layer--video"
                src={mediaURL(item.stream_url)}
                controls={phase !== "leaving"}
                autoPlay
                playsInline
                preload="auto"
                onLoadedData={() => setVideoReady(true)}
                onCanPlay={() => setVideoReady(true)}
                onError={() => {
                  const known = item.error?.trim();
                  if (known) {
                    onPlayError(`无法播放：${known}`);
                    return;
                  }
                  void probeMediaError(item.stream_url).then((detail) => {
                    onPlayError(
                      detail
                        ? `无法播放：${detail}`
                        : `无法播放该视频（编码不被浏览器支持，或流地址不可用）。可直接打开：${mediaURL(item.stream_url)}`,
                    );
                  });
                }}
              />
            )}
          </div>
        ) : (
          <img
            src={
              isOpen
                ? mediaURL(item.preview_url || item.thumb_url)
                : thumbSrc
            }
            alt={displayName(item)}
            draggable={false}
          />
        )}
      </div>

      {player.kind === "video" && chromeVisible && (
        <>
          <div className="preview-video-actions">
            {(item.status === "caching" || item.status === "paused") && (
              <RingProgressButton
                pct={progressPct(item)}
                label={
                  item.status === "caching"
                    ? `下载中 ${progressPct(item)}%，点击暂停`
                    : `已暂停 ${progressPct(item)}%`
                }
                interactive={item.status === "caching"}
                onClick={
                  item.status === "caching" ? () => onPause(item) : undefined
                }
              />
            )}
            <button
              type="button"
              className="preview-icon-btn"
              onClick={onCloseRequest}
              aria-label="关闭"
            >
              <PreviewCloseIcon />
            </button>
          </div>
          {playError && (
            <div className="preview-global-error banner error">{playError}</div>
          )}
        </>
      )}

      {player.kind === "image" && (
        <div
          className={[
            "preview-panel",
            chromeVisible ? "preview-panel--visible" : "",
          ]
            .filter(Boolean)
            .join(" ")}
          style={
            {
              top: `${layout.panelTop}px`,
              left: `${layout.panelLeft}px`,
              width: `${layout.panelWidth}px`,
              height: `${layout.panelHeight}px`,
            } as CSSProperties
          }
          onClick={(e) => e.stopPropagation()}
        >
          <div className="preview-head">
            <h3>{displayName(item)}</h3>
            <div className="modal-actions">
              <button className="btn ghost" onClick={onCloseRequest}>
                关闭
              </button>
            </div>
          </div>
          <div
            className="preview-media-slot"
            style={boxStyle({
              top: layout.media.top - layout.panelTop,
              left: layout.media.left - layout.panelLeft,
              width: layout.media.width,
              height: layout.media.height,
            })}
            aria-hidden="true"
          />
          <div className="preview-foot">
            <span>
              {[
                formatMessageDate(item.date),
                formatSize(item.size),
                item.duration ? formatDuration(item.duration) : "",
              ]
                .filter(Boolean)
                .join(" · ")}
            </span>
            <span>
              {playError && item.status === "error"
                ? "错误"
                : statusLabel(item)}
            </span>
          </div>
          {playError && <div className="banner error">{playError}</div>}
        </div>
      )}
    </div>
  );
}
