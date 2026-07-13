const scrollTargets = new Map<string, () => void>();

export const RAIL_HEIGHT_RATIO = 0.84;
export const RAIL_LINE_GAP = 8;
export const CATEGORY_LINES_PER_SECTION = 4;

export function registerScrollTarget(id: string, fn: () => void) {
  scrollTargets.set(id, fn);
  return () => {
    scrollTargets.delete(id);
  };
}

export function scrollToItem(id: string) {
  const fn = scrollTargets.get(id);
  if (fn) {
    fn();
    return;
  }
  document.getElementById(`scroll-item-${id}`)?.scrollIntoView({
    behavior: "auto",
    block: "center",
  });
}

export type RailItem = {
  id: string;
  date?: number;
};

export type ScrollBatch = {
  id: string;
  items: RailItem[];
  startItemId: string;
  startDate?: number;
  endDate?: number;
  label: string;
};

export type RailSectionLayout = {
  id: string;
  label: string;
  items: RailItem[];
  batches: ScrollBatch[];
};

function validMessageDate(item: RailItem): number | null {
  const value = item.date;
  if (!value || value <= 0 || !Number.isFinite(value)) return null;
  return value;
}

function dateBounds(items: RailItem[]): {
  min?: number;
  max?: number;
  count: number;
} {
  let min = Number.POSITIVE_INFINITY;
  let max = Number.NEGATIVE_INFINITY;
  let count = 0;

  for (const item of items) {
    const date = validMessageDate(item);
    if (date == null) continue;
    min = Math.min(min, date);
    max = Math.max(max, date);
    count += 1;
  }

  if (count === 0) return { count };
  return { min, max, count };
}

function formatRailDate(unix: number): string {
  const d = new Date(unix * 1000);
  const y = d.getFullYear();
  const mo = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${mo}-${day}`;
}

function batchLabel(items: RailItem[]): string {
  const bounds = dateBounds(items);
  if (bounds.min == null || bounds.max == null) {
    return `${items.length} 项`;
  }

  const from = formatRailDate(bounds.min);
  const to = formatRailDate(bounds.max);
  const range = from === to ? from : `${from} – ${to}`;
  return `${range} · ${items.length} 项`;
}

function makeBatch(
  sectionId: string,
  index: number,
  items: RailItem[],
): ScrollBatch | null {
  const first = items[0];
  if (!first) return null;

  const bounds = dateBounds(items);
  return {
    id: `${sectionId}-batch-${index}`,
    items,
    startItemId: first.id,
    startDate: bounds.min,
    endDate: bounds.max,
    label: batchLabel(items),
  };
}

function splitIntoEvenCount(
  items: RailItem[],
  batchCount: number,
  sectionId: string,
): ScrollBatch[] {
  if (items.length === 0 || batchCount <= 0) return [];
  if (batchCount === 1) {
    const batch = makeBatch(sectionId, 0, items);
    return batch ? [batch] : [];
  }

  if (batchCount >= items.length) {
    return items
      .map((item, index) => makeBatch(sectionId, index, [item]))
      .filter((batch): batch is ScrollBatch => batch != null);
  }

  const batches: ScrollBatch[] = [];
  for (let i = 0; i < batchCount; i += 1) {
    const start = Math.floor((items.length * i) / batchCount);
    const end = Math.floor((items.length * (i + 1)) / batchCount);
    const batch = makeBatch(sectionId, i, items.slice(start, end));
    if (batch) batches.push(batch);
  }
  return batches;
}

function allocateBatchSlots(sections: RailItem[][], totalSlots: number): number[] {
  const slots = sections.map(() => 0);
  const active = sections
    .map((items, index) => ({
      index,
      cap: items.length,
      weight: items.length,
    }))
    .filter((entry) => entry.cap > 0);

  if (!active.length || totalSlots <= 0) return slots;

  let remaining = totalSlots;
  for (const entry of active) {
    if (remaining <= 0) break;
    slots[entry.index] = 1;
    remaining -= 1;
  }

  while (remaining > 0) {
    const candidates = active.filter(
      (entry) => (slots[entry.index] ?? 0) < entry.cap,
    );
    if (!candidates.length) break;

    let pick = candidates[0]?.index ?? 0;
    let bestScore = -1;
    for (const entry of candidates) {
      const score = entry.weight / Math.max(1, slots[entry.index] ?? 1);
      if (score > bestScore) {
        bestScore = score;
        pick = entry.index;
      }
    }
    slots[pick] = (slots[pick] ?? 0) + 1;
    remaining -= 1;
  }

  return slots;
}

function sectionHeight(batchCount: number): number {
  const lineH = 3;
  const gap = RAIL_LINE_GAP;
  const categoryLines = CATEGORY_LINES_PER_SECTION;

  let height = categoryLines * lineH;
  height += Math.max(0, categoryLines - 1) * gap;

  if (batchCount > 0) {
    height += gap;
    height += batchCount * lineH;
    height += Math.max(0, batchCount - 1) * gap;
  }

  return height;
}

function totalRailHeight(batchSlots: number[], sectionCount: number): number {
  let height = 0;
  for (const batchCount of batchSlots) {
    height += sectionHeight(batchCount);
  }
  height += Math.max(0, sectionCount - 1) * RAIL_LINE_GAP;
  return height;
}

function maxBatchSlotsForViewport(
  viewportHeight: number,
  sectionCount: number,
): number {
  const railHeight = Math.max(280, viewportHeight * RAIL_HEIGHT_RATIO);
  const minCategoryHeight = totalRailHeight(
    new Array(sectionCount).fill(0),
    sectionCount,
  );

  let low = 0;
  let high = Math.max(0, Math.floor((railHeight - minCategoryHeight) / 10));
  let best = 0;

  while (low <= high) {
    const mid = Math.floor((low + high) / 2);
    if (minCategoryHeight + mid * 10 <= railHeight) {
      best = mid;
      low = mid + 1;
    } else {
      high = mid - 1;
    }
  }

  return Math.max(sectionCount, best);
}

function fitBatchSlots(
  viewportHeight: number,
  sections: RailItem[][],
): number[] {
  const sectionCount = sections.length;
  const targetTotal = maxBatchSlotsForViewport(viewportHeight, sectionCount);
  const slots = allocateBatchSlots(sections, targetTotal);
  const weights = sections.map((items) => items.length);
  const minSlots = sections.map((items) => (items.length > 0 ? 1 : 0));

  while (totalRailHeight(slots, sectionCount) > viewportHeight * RAIL_HEIGHT_RATIO) {
    const total = slots.reduce((sum, n) => sum + n, 0);
    if (total <= 0) break;

    let pick = -1;
    let bestScore = Number.POSITIVE_INFINITY;
    for (let i = 0; i < slots.length; i += 1) {
      if ((slots[i] ?? 0) <= (minSlots[i] ?? 0)) continue;
      const score = (weights[i] ?? 0) / (slots[i] ?? 1);
      if (score < bestScore) {
        bestScore = score;
        pick = i;
      }
    }
    if (pick < 0) break;
    slots[pick] = Math.max(0, (slots[pick] ?? 0) - 1);
  }

  return slots;
}

export function buildRailLayout(
  viewportHeight: number,
  sections: {
    id: string;
    label: string;
    items: RailItem[];
  }[],
): RailSectionLayout[] {
  const batchSlots = fitBatchSlots(
    viewportHeight,
    sections.map((section) => section.items),
  );

  return sections.map((section, sectionIndex) => ({
    id: section.id,
    label: section.label,
    items: section.items,
    batches: splitIntoEvenCount(
      section.items,
      batchSlots[sectionIndex] ?? 0,
      section.id,
    ),
  }));
}

export function railHeightPx(viewportHeight: number): number {
  return Math.round(Math.max(280, viewportHeight * RAIL_HEIGHT_RATIO));
}
