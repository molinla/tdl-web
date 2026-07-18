import {
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
} from "react";
import { Pause, Play, SkipBack, SkipForward, X } from "lucide-react";
import {
  displayName,
  formatMessageDate,
  mediaURL,
  probeMediaError,
  progressPct,
} from "./api";
import type { Item } from "./types";

const TRANSITION_MS = 450;
const FILM_FADE_TRANSITION_MS = 320;
const CHROME_VISIBLE_MS = 3000;

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
  const pad = 0;
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

function formatClock(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "0:00";
  const total = Math.floor(seconds);
  const s = String(total % 60).padStart(2, "0");
  const m = Math.floor(total / 60) % 60;
  const h = Math.floor(total / 3600);
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${s}` : `${m}:${s}`;
}

function releaseVideo(video: HTMLVideoElement | null | undefined) {
  if (!video) return;
  video.pause();
  video.removeAttribute("src");
  video.load();
}

function PreviewPauseIcon() {
  return <Pause size={20} strokeWidth={2} aria-hidden="true" />;
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

export function MediaPreview({
  player,
  originRect,
  originRotation,
  transitionMode = "zoom",
  thumbSrc,
  aspectRatio,
  closing,
  mediaItems,
  playError,
  onPlayError,
  onNavigate,
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
  mediaItems: Item[];
  playError: string;
  onPlayError: (msg: string) => void;
  onNavigate: (item: Item) => void;
  onCloseRequest: () => void;
  onClosed: () => void;
  onPause: (item: Item) => void;
}) {
  const originBox = useRef<Box>(rectToBox(originRect));
  const originRotationRef = useRef(originRotation);
  const initializedRef = useRef(false);
  const phaseRef = useRef<"entering" | "open" | "leaving">("entering");
  const [phase, setPhase] = useState<"entering" | "open" | "leaving">(
    "entering",
  );
  const [mediaBox, setMediaBox] = useState<Box>(originBox.current);
  const [chromeVisible, setChromeVisible] = useState(false);
  const [showVideo, setShowVideo] = useState(false);
  const [videoReady, setVideoReady] = useState(false);
  const [playing, setPlaying] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const reducedMotion = useRef(
    typeof window !== "undefined" &&
      window.matchMedia("(prefers-reduced-motion: reduce)").matches,
  );
  const onClosedRef = useRef(onClosed);
  onClosedRef.current = onClosed;
  const closeHandledRef = useRef(false);
  const videoRef = useRef<HTMLVideoElement>(null);
  const hideChromeTimerRef = useRef<number | null>(null);

  const aspect = aspectRatio ?? defaultAspect(player.kind);
  const isFilmFade = transitionMode === "film-fade";
  const transitionMs = isFilmFade ? FILM_FADE_TRANSITION_MS : TRANSITION_MS;

  const item = player.item;
  const isOpen = phase === "open";
  const rootOpen = isOpen || phase === "leaving" || isFilmFade;
  const showVideoPlayer =
    player.kind === "video" && (showVideo || phase === "leaving" || isFilmFade);
  const mediaIndex = useMemo(
    () => mediaItems.findIndex((media) => media.id === item.id),
    [item.id, mediaItems],
  );
  const canNavigate = mediaItems.length > 1;

  function clearChromeTimer() {
    if (hideChromeTimerRef.current != null) {
      window.clearTimeout(hideChromeTimerRef.current);
      hideChromeTimerRef.current = null;
    }
  }

  function showChrome() {
    if (phaseRef.current !== "open") return;
    setChromeVisible(true);
    clearChromeTimer();
    hideChromeTimerRef.current = window.setTimeout(() => {
      hideChromeTimerRef.current = null;
      setChromeVisible(false);
    }, CHROME_VISIBLE_MS);
  }

  function resetPlaybackRate() {
    if (videoRef.current) videoRef.current.playbackRate = 1;
  }

  function syncVideoTime(video = videoRef.current) {
    if (!video) return;
    setCurrentTime(video.currentTime || 0);
    setDuration(Number.isFinite(video.duration) ? video.duration : 0);
  }

  function playVideo(video = videoRef.current) {
    if (!video || player.kind !== "video" || phaseRef.current !== "open") return;
    void video.play().catch(() => setPlaying(false));
  }

  function togglePlay() {
    const video = videoRef.current;
    if (!video) return;
    showChrome();
    if (video.paused) {
      playVideo(video);
    } else {
      video.pause();
    }
  }

  function seek(value: string) {
    const video = videoRef.current;
    const next = Number(value);
    if (!Number.isFinite(next)) return;
    setCurrentTime(next);
    if (video) video.currentTime = next;
    showChrome();
  }

  function navigate(delta: number) {
    if (!canNavigate) return;
    const start = mediaIndex >= 0 ? mediaIndex : 0;
    const next = mediaItems[(start + delta + mediaItems.length) % mediaItems.length];
    if (next) {
      resetPlaybackRate();
      onNavigate(next);
      showChrome();
    }
  }

  useEffect(() => {
    phaseRef.current = phase;
  }, [phase]);

  useLayoutEffect(() => {
    const nextLayout = computeLayout(player.kind, aspect);
    setVideoReady(false);
    setPlaying(false);
    setCurrentTime(0);
    setDuration(0);
    resetPlaybackRate();

    if (initializedRef.current && phaseRef.current === "open") {
      setMediaBox(nextLayout.media);
      setShowVideo(player.kind === "video");
      return () => releaseVideo(videoRef.current);
    }

    initializedRef.current = true;
    originBox.current = rectToBox(originRect);
    originRotationRef.current = originRotation;

    if (isFilmFade) {
      setMediaBox(nextLayout.media);
      setPhase("entering");
      setChromeVisible(false);
      setShowVideo(player.kind === "video");

      if (reducedMotion.current) {
        setPhase("open");
        return () => releaseVideo(videoRef.current);
      }

      const id = window.requestAnimationFrame(() => {
        setPhase("open");
      });
      return () => {
        window.cancelAnimationFrame(id);
        releaseVideo(videoRef.current);
      };
    }

    setMediaBox(reducedMotion.current ? nextLayout.media : originBox.current);

    if (reducedMotion.current) {
      setPhase("open");
      if (player.kind === "video") setShowVideo(true);
      return () => releaseVideo(videoRef.current);
    }

    setPhase("entering");
    setChromeVisible(false);
    setShowVideo(false);

    const id = window.requestAnimationFrame(() => {
      window.requestAnimationFrame(() => {
        setMediaBox(nextLayout.media);
        setPhase("open");
      });
    });
    return () => {
      window.cancelAnimationFrame(id);
      releaseVideo(videoRef.current);
    };
  }, [player.item.id, player.kind, originRect, originRotation, aspect, isFilmFade]);

  useEffect(() => {
    const root = document.documentElement;
    root.classList.add("body-scroll-locked");
    return () => {
      root.classList.remove("body-scroll-locked");
    };
  }, []);

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        onCloseRequest();
        return;
      }
      if (e.key !== "ArrowRight" || player.kind !== "video") return;
      if (videoRef.current) videoRef.current.playbackRate = 2;
      showChrome();
    };
    const onKeyUp = (e: KeyboardEvent) => {
      if (e.key === "ArrowRight") resetPlaybackRate();
    };
    window.addEventListener("keydown", onKeyDown);
    window.addEventListener("keyup", onKeyUp);
    window.addEventListener("blur", resetPlaybackRate);
    return () => {
      window.removeEventListener("keydown", onKeyDown);
      window.removeEventListener("keyup", onKeyUp);
      window.removeEventListener("blur", resetPlaybackRate);
      resetPlaybackRate();
    };
  }, [onCloseRequest, player.kind, player.item.id]);

  useEffect(() => {
    if (showVideoPlayer) return;
    releaseVideo(videoRef.current);
  }, [showVideoPlayer]);

  useEffect(() => {
    return () => {
      clearChromeTimer();
      resetPlaybackRate();
      releaseVideo(videoRef.current);
    };
  }, []);

  useEffect(() => {
    if (phase !== "open") {
      clearChromeTimer();
      setChromeVisible(false);
      return;
    }
    if (player.kind === "video") setShowVideo(true);
    showChrome();
  }, [phase, player.kind, player.item.id]);

  useEffect(() => {
    if (phase === "open" && player.kind === "video" && videoReady) {
      playVideo();
    }
  }, [phase, player.kind, player.item.id, videoReady]);

  useEffect(() => {
    if (!closing) return;

    closeHandledRef.current = false;
    clearChromeTimer();
    resetPlaybackRate();
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

    setMediaBox(resolveOriginRect(player.item.id, originBox.current));

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
    const onResize = () => {
      if (phaseRef.current !== "open") return;
      setMediaBox(computeLayout(player.kind, aspect).media);
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
  }, [player.kind, aspect]);

  function handleMediaTransitionEnd(e: React.TransitionEvent) {
    if (phase !== "leaving" || closeHandledRef.current) return;
    if (isFilmFade) return;
    if (e.propertyName !== "width" && e.propertyName !== "top") return;
    closeHandledRef.current = true;
    onClosedRef.current();
  }

  const progressMax = duration || 1;
  const progressValue = Math.min(currentTime, progressMax);
  const progressPercent =
    duration > 0 ? Math.min(100, Math.max(0, (currentTime / duration) * 100)) : 0;
  const date = formatMessageDate(item.date);

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
      onMouseMove={showChrome}
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

      <button
        type="button"
        className="preview-icon-btn preview-close-btn"
        onClick={onCloseRequest}
        aria-label="关闭"
      >
        <X size={44} strokeWidth={1.7} aria-hidden="true" />
      </button>

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
                autoPlay
                playsInline
                preload="metadata"
                onLoadedMetadata={(e) => syncVideoTime(e.currentTarget)}
                onLoadedData={(e) => {
                  syncVideoTime(e.currentTarget);
                  setVideoReady(true);
                  playVideo(e.currentTarget);
                }}
                onCanPlay={(e) => {
                  syncVideoTime(e.currentTarget);
                  setVideoReady(true);
                  playVideo(e.currentTarget);
                }}
                onTimeUpdate={(e) => syncVideoTime(e.currentTarget)}
                onPlay={() => setPlaying(true)}
                onPause={() => setPlaying(false)}
                onEnded={() => setPlaying(false)}
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
          "preview-controller",
          player.kind === "image" ? "preview-controller--image" : "",
          chromeVisible ? "" : "preview-controller--collapsed",
        ]
          .filter(Boolean)
          .join(" ")}
      >
        <div className="preview-title-card">
          <h3>{displayName(item)}</h3>
          {date && <div className="preview-title-date">{date}</div>}
          {(item.author || item.forwarded_from || item.saved_from) && (
            <div className="preview-message-source">
              {[
                item.author,
                item.forwarded_from
                  ? `转发自 ${item.forwarded_from}`
                  : "",
                item.saved_from && item.saved_from !== item.forwarded_from
                  ? `来自 ${item.saved_from}`
                  : "",
              ]
                .filter(Boolean)
                .join(" · ")}
            </div>
          )}
          {item.text?.trim() && (
            <div className="preview-message-text">{item.text}</div>
          )}
        </div>
        {player.kind === "video" && (
          <div className="preview-progress-row">
            <span>{formatClock(currentTime)}</span>
            <div
              className="preview-progress-hit"
              onPointerDown={(e) => {
                if (e.button !== 0) return;
                const rect = e.currentTarget.getBoundingClientRect();
                const ratio = Math.min(
                  1,
                  Math.max(0, (e.clientX - rect.left) / rect.width),
                );
                seek(String(ratio * progressMax));
              }}
            >
              <input
                className="preview-progress"
                type="range"
                min={0}
                max={progressMax}
                step="0.1"
                value={progressValue}
                style={
                  {
                    "--preview-progress-pct": `${progressPercent}%`,
                  } as CSSProperties
                }
                onChange={(e) => seek(e.currentTarget.value)}
                aria-label="播放进度"
              />
            </div>
            <span>{formatClock(duration)}</span>
          </div>
        )}
        <div className="preview-controller-buttons">
          <button
            type="button"
            className="preview-controller-btn"
            disabled={!canNavigate}
            onClick={() => navigate(-1)}
            aria-label="上一个"
          >
            <SkipBack size={24} fill="currentColor" aria-hidden="true" />
          </button>
          {player.kind === "video" && (
            <button
              type="button"
              className="preview-controller-btn preview-controller-btn--play"
              onClick={togglePlay}
              aria-label={playing ? "暂停" : "播放"}
            >
              {playing ? (
                <Pause size={28} fill="currentColor" aria-hidden="true" />
              ) : (
                <Play size={28} fill="currentColor" aria-hidden="true" />
              )}
            </button>
          )}
          <button
            type="button"
            className="preview-controller-btn"
            disabled={!canNavigate}
            onClick={() => navigate(1)}
            aria-label="下一个"
          >
            <SkipForward size={24} fill="currentColor" aria-hidden="true" />
          </button>
        </div>
      </div>

      {(item.status === "caching" || item.status === "paused") && (
        <div className="preview-download-actions">
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
        </div>
      )}
      {chromeVisible && playError && (
        <div className="preview-global-error banner error">{playError}</div>
      )}
    </div>
  );
}
