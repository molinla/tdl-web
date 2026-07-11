import { useMemo, type CSSProperties } from "react";
import { coverURL } from "./api";
import type { Item } from "./types";

const COLUMN_COUNT = 14;
const FRAMES_PER_COL = 16;

function coverPath(item: Item): string | undefined {
  if (item.type === "video") return item.cover || item.thumb_url;
  return item.thumb_url || item.preview_url;
}

function hashId(id: string): number {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) | 0;
  return h;
}

function sortedPool(items: Item[]): Item[] {
  return [...items].sort((a, b) => hashId(a.id) - hashId(b.id));
}

/** Each column draws from its own non-overlapping item slice. */
function pickColumnCoverUrls(allItems: Item[], colIndex: number): string[] {
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

  const urls: string[] = [];
  for (let i = 0; i < FRAMES_PER_COL; i++) {
    const item = source[i % source.length];
    const path = coverPath(item);
    if (path) urls.push(coverURL(path, item.status));
  }
  return urls;
}

function FilmFrame({ src, index }: { src?: string; index: number }) {
  return (
    <div className="film-frame">
      <div className="film-sprocket film-sprocket--side" aria-hidden="true" />
      <div className="film-frame-img">
        {src ? (
          <img
            src={src}
            alt=""
            loading="lazy"
            decoding="async"
            draggable={false}
          />
        ) : (
          <div className="film-frame-empty" />
        )}
      </div>
      <div className="film-sprocket film-sprocket--side" aria-hidden="true" />
      <span className="film-frame-num" aria-hidden="true">
        {String(index + 1).padStart(3, "0")}
      </span>
    </div>
  );
}

function FilmColumn({
  colIndex,
  coverUrls,
}: {
  colIndex: number;
  coverUrls: string[];
}) {
  const frames = useMemo(() => {
    const seq = Array.from({ length: FRAMES_PER_COL }, (_, i) => {
      const url = coverUrls.length ? coverUrls[i % coverUrls.length] : undefined;
      return { key: `a-${i}`, url, index: i };
    });
    const dup = seq.map((f) => ({ ...f, key: `b-${f.key}` }));
    return [...seq, ...dup];
  }, [coverUrls]);

  const reverse = colIndex % 2 === 1;
  const duration = 68 + colIndex * 10;

  return (
    <div
      className={`film-col${reverse ? " film-col--reverse" : ""}`}
      style={{ "--film-duration": `${duration}s` } as CSSProperties}
    >
      <div className="film-col-track">
        {frames.map((f) => (
          <FilmFrame key={f.key} src={f.url} index={f.index} />
        ))}
      </div>
    </div>
  );
}

export function FilmBackground({ items }: { items: Item[] }) {
  const columnCoverUrls = useMemo(
    () =>
      Array.from({ length: COLUMN_COUNT }, (_, i) =>
        pickColumnCoverUrls(items, i),
      ),
    [items],
  );

  return (
    <div className="film-bg-wrap" aria-hidden="true">
      <div className="film-bg">
        {Array.from({ length: COLUMN_COUNT }, (_, i) => (
          <FilmColumn key={i} colIndex={i} coverUrls={columnCoverUrls[i]} />
        ))}
      </div>
      <div className="film-bg-arc" />
    </div>
  );
}
