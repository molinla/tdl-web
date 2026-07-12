import {
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import {
  displayName,
  mediaURL,
  probeMediaError,
  progressPct,
} from "./api";
import type { Item } from "./types";

const TRANSITION_MS = 450;
const FILM_FADE_TRANSITION_MS = 320;

type PlayerState = { kind: "video"; item: Item } | { kind: "image"; item: Item };
type PreviewTransitionMode = "zoom" | "film-fade";

type Box = { top: number; left: number; width: number; height: number };

type Layout = {
  media: Box;
};

function defaultAspect(kind: PlayerState["kind"]) {
  return kind === "video" ? 9 / 16 : 4 / 3;
}

/** Immersive stage: one black canvas sized to aspect, no outer panel chrome. */
function computeImmersiveLayout(aspectHeightPerWidth: number): Layout {
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
    media: { top, left, width, height },
  };
}

function computeLayout(
  _kind: PlayerState["kind"],
  aspectHeightPerWidth: number,
): Layout {
  return computeImmersiveLayout(aspectHeightPerWidth);
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

function boxStyle(box: Box, rotation = 0): CSSProperties {
  return {
    top: `${box.top}px`,
    left: `${box.left}px`,
    width: `${box.width}px`,
    height: `${box.height}px`,
    transform: `rotate(${rotation}deg)`,
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
  originRotation,
  transitionMode = "zoom",
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
  originRotation: number;
  transitionMode?: PreviewTransitionMode;
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
  const originRotationRef = useRef(originRotation);
  const [phase, setPhase] = useState<"entering" | "open" | "leaving">(
    "entering",
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
  const isFilmFade = transitionMode === "film-fade";
  const transitionMs = isFilmFade ? FILM_FADE_TRANSITION_MS : TRANSITION_MS;

  useLayoutEffect(() => {
    originBox.current = rectToBox(originRect);
    originRotationRef.current = originRotation;
    const nextLayout = computeLayout(player.kind, aspect);

    if (isFilmFade) {
      setMediaBox(nextLayout.media);
      setPhase("entering");
      setChromeVisible(false);
      setShowVideo(player.kind === "video");
      setVideoReady(false);

      if (reducedMotion.current) {
        setPhase("open");
        setChromeVisible(true);
        return;
      }

      const id = window.requestAnimationFrame(() => {
        setPhase("open");
      });
      return () => window.cancelAnimationFrame(id);
    }

    setMediaBox(reducedMotion.current ? nextLayout.media : originBox.current);

    if (reducedMotion.current) {
      setPhase("open");
      setChromeVisible(true);
      if (player.kind === "video") setShowVideo(true);
      return;
    }

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
  }, [
    player.item.id,
    player.kind,
    originRect,
    originRotation,
    aspect,
    isFilmFade,
  ]);

  useEffect(() => {
    const root = document.documentElement;
    root.classList.add("body-scroll-locked");
    return () => {
      root.classList.remove("body-scroll-locked");
    };
  }, []);

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
    setChromeVisible(false);
    setPhase("leaving");

    if (isFilmFade) {
      const targetBox = computeLayout(player.kind, aspect).media;
      setMediaBox(targetBox);
      if (reducedMotion.current) {
        onClosedRef.current();
        return;
      }
      const t = window.setTimeout(() => {
        if (closeHandledRef.current) return;
        closeHandledRef.current = true;
        onClosedRef.current();
      }, transitionMs);
      return () => window.clearTimeout(t);
    }

    {
      setMediaBox(resolveOriginRect(player.item.id, originBox.current));
    }

    if (reducedMotion.current) {
      onClosedRef.current();
      return;
    }

    const t = window.setTimeout(() => {
      if (closeHandledRef.current) return;
      closeHandledRef.current = true;
      onClosedRef.current();
    }, transitionMs);
    return () => window.clearTimeout(t);
  }, [aspect, closing, isFilmFade, player.item.id, player.kind, transitionMs]);

  useEffect(() => {
    if (phase !== "open") return;
    if (player.kind === "video") {
      setShowVideo(true);
    }
    const t = window.setTimeout(
      () => setChromeVisible(true),
      reducedMotion.current || isFilmFade ? 0 : transitionMs * 0.55,
    );
    return () => window.clearTimeout(t);
  }, [isFilmFade, phase, player.kind, transitionMs]);

  useEffect(() => {
    const onResize = () => {
      if (phase !== "open") return;
      setMediaBox(computeLayout(player.kind, aspect).media);
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [phase, player.kind, aspect]);

  function handleMediaTransitionEnd(e: React.TransitionEvent) {
    if (phase !== "leaving" || closeHandledRef.current) return;
    if (isFilmFade) return;
    if (e.propertyName !== "width" && e.propertyName !== "top") return;
    closeHandledRef.current = true;
    onClosedRef.current();
  }

  const item = player.item;
  const isOpen = phase === "open";
  const rootOpen = isOpen || phase === "leaving" || isFilmFade;
  const showVideoPlayer =
    player.kind === "video" && (showVideo || phase === "leaving" || isFilmFade);

  return (
    <div
      className={[
        "preview-root",
        rootOpen ? "preview-root--open" : "",
        "preview-root--immersive",
        isFilmFade ? "preview-root--film-fade" : "",
        player.kind === "video" ? "preview-root--video" : "preview-root--image",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <button
        type="button"
        className={[
          "preview-backdrop",
          rootOpen && phase !== "leaving" ? "preview-backdrop--open" : "",
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
          "preview-media--immersive",
          isFilmFade ? "preview-media--film-fade" : "",
          player.kind === "video" ? "preview-media--video" : "preview-media--image",
          isOpen ? "preview-media--open" : "",
          phase === "leaving" ? "preview-media--leaving" : "",
          videoReady && (phase === "open" || phase === "leaving")
            ? "preview-media--video-ready"
            : "",
        ]
          .filter(Boolean)
          .join(" ")}
        style={boxStyle(
          mediaBox,
          isFilmFade || phase === "open" ? 0 : originRotationRef.current,
        )}
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

      <div
        className={[
          "preview-video-actions",
          chromeVisible ? "" : "preview-video-actions--collapsed",
        ]
          .filter(Boolean)
          .join(" ")}
      >
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
      {chromeVisible && playError && (
        <div className="preview-global-error banner error">{playError}</div>
      )}
    </div>
  );
}
