import type { Item } from "./types";

export function columnCountForWidth(
  width: number,
  minColumnWidth: number,
  gap: number,
): number {
  if (width <= 0) return 1;
  return Math.max(1, Math.floor((width + gap) / (minColumnWidth + gap)));
}

/** Round-robin split: col0 gets 0,3,6… col1 gets 1,4,7… reading order stays left→right, top→down. */
export function splitIntoColumns(
  items: Item[],
  columnCount: number,
): Item[][] {
  const cols = Math.max(1, columnCount);
  const buckets: Item[][] = Array.from({ length: cols }, () => []);
  items.forEach((item, index) => {
    buckets[index % cols].push(item);
  });
  return buckets;
}
