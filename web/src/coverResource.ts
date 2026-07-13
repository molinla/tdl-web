import { LRUMap } from "./lru";

const COVER_RESOURCE_CACHE_MAX = 500;

export type CoverResource = {
  image: HTMLImageElement;
  status: "loading" | "ready" | "error";
  promise: Promise<void>;
};

function evictCoverResource(_url: string, entry: CoverResource) {
  if (entry.status === "loading") return;
  entry.image.onload = null;
  entry.image.onerror = null;
  entry.image.src = "";
}

const coverResources = new LRUMap<string, CoverResource>(
  COVER_RESOURCE_CACHE_MAX,
  evictCoverResource,
);

/** Return one low-priority background image request for a cover URL. */
export function ensureCoverResource(url: string): CoverResource {
  const cached = coverResources.get(url);
  if (cached) return cached;

  const image = new Image();
  image.decoding = "async";
  image.fetchPriority = "low";
  const entry: CoverResource = {
    image,
    status: "loading",
    promise: Promise.resolve(),
  };
  entry.promise = new Promise<void>((resolve) => {
    image.onload = () => {
      entry.status = "ready";
      image.onload = null;
      image.onerror = null;
      resolve();
    };
    image.onerror = () => {
      entry.status = "error";
      image.onload = null;
      image.onerror = null;
      resolve();
    };
  });
  image.src = url;
  coverResources.set(url, entry);
  return entry;
}
