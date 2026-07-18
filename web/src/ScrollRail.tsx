import { useEffect, useMemo, useRef, useState } from "react";
import {
  buildRailLayout,
  RAIL_HEIGHT_RATIO,
  RAIL_LINE_GAP,
  railBatchIndexAtPosition,
  railHeightPx,
  scrollToItem,
  type RailSectionLayout,
} from "./scrollNavigation";
import type { Item } from "./types";

const SECTION_META = [
  { id: "section-media", label: "媒体" },
] as const;

type ScrollRailProps = {
  collapsed?: boolean;
  items: Item[];
};

type FlatRailLine = {
  key: string;
  globalIndex: number;
  kind: "batch";
  label: string;
  active: boolean;
  onClick: () => void;
};

function readViewportHeight() {
  if (typeof window === "undefined") return 800;
  return Math.round(window.visualViewport?.height ?? window.innerHeight);
}

function useViewportHeight() {
  const [height, setHeight] = useState(readViewportHeight);

  useEffect(() => {
    const update = () => {
      const next = readViewportHeight();
      setHeight((prev) => (prev === next ? prev : next));
    };

    window.addEventListener("resize", update);
    window.visualViewport?.addEventListener("resize", update);
    window.visualViewport?.addEventListener("scroll", update);

    return () => {
      window.removeEventListener("resize", update);
      window.visualViewport?.removeEventListener("resize", update);
      window.visualViewport?.removeEventListener("scroll", update);
    };
  }, []);

  return height;
}

export function ScrollRail({
  collapsed = false,
  items,
}: ScrollRailProps) {
  const viewportHeight = useViewportHeight();

  const sections = useMemo<RailSectionLayout[]>(
    () =>
      buildRailLayout(viewportHeight, [
        { id: SECTION_META[0].id, label: SECTION_META[0].label, items },
      ]),
    [viewportHeight, items],
  );

  const [activeBatchId, setActiveBatchId] = useState<string | null>(null);
  const activePositionRef = useRef<{
    sectionId: string;
    batchIndex: number | null;
  }>({ sectionId: SECTION_META[0].id, batchIndex: null });
  const lastScrollYRef = useRef(
    typeof window === "undefined" ? 0 : window.scrollY,
  );

  const flatLines = useMemo(() => {
    const lines: FlatRailLine[] = [];
    let globalIndex = 0;

    for (const section of sections) {
      section.batches.forEach((batch, batchIndex) => {
        lines.push({
          key: batch.id,
          globalIndex,
          kind: "batch",
          label: batch.label || `${batchIndex + 1} · ${batch.items.length} 项`,
          active: activeBatchId === batch.id,
          onClick: () => scrollToItem(batch.startItemId),
        });
        globalIndex += 1;
      });
    }

    return lines;
  }, [sections, activeBatchId]);

  useEffect(() => {
    let raf = 0;

    function updateActiveLine() {
      raf = 0;
      const viewportHeight = readViewportHeight();
      const anchor = viewportHeight * 0.42;
      let selected:
        | {
            section: RailSectionLayout;
            rect: DOMRect;
          }
        | undefined;
      let bestDist = Number.POSITIVE_INFINITY;

      for (const section of sections) {
        const el = document.getElementById(section.id);
        if (!el) continue;
        const rect = el.getBoundingClientRect();
        const dist =
          anchor < rect.top
            ? rect.top - anchor
            : anchor > rect.bottom
              ? anchor - rect.bottom
              : 0;
        if (dist < bestDist) {
          bestDist = dist;
          selected = { section, rect };
        }
      }
      if (!selected) return;

      const { section, rect } = selected;
      let batchIndex = railBatchIndexAtPosition(
        anchor - rect.top,
        rect.height,
        section.batches.length,
      );
      const scrollY = window.scrollY;
      const scrollDelta = scrollY - lastScrollYRef.current;
      if (Math.abs(scrollDelta) >= 1) lastScrollYRef.current = scrollY;

      const previous = activePositionRef.current;
      if (
        previous.sectionId === section.id &&
        previous.batchIndex != null &&
        batchIndex != null
      ) {
        const previousIndex = Math.min(
          previous.batchIndex,
          section.batches.length - 1,
        );
        batchIndex =
          scrollDelta >= 1
            ? Math.max(previousIndex, batchIndex)
            : scrollDelta <= -1
              ? Math.min(previousIndex, batchIndex)
              : previousIndex;
      }

      activePositionRef.current = { sectionId: section.id, batchIndex };
      const batchId =
        batchIndex == null ? null : (section.batches[batchIndex]?.id ?? null);
      setActiveBatchId((prev) => (prev === batchId ? prev : batchId));
    }

    function scheduleActiveLineUpdate() {
      if (raf) return;
      raf = window.requestAnimationFrame(updateActiveLine);
    }

    scheduleActiveLineUpdate();
    window.addEventListener("scroll", scheduleActiveLineUpdate, {
      passive: true,
    });
    window.addEventListener("resize", scheduleActiveLineUpdate);
    return () => {
      if (raf) window.cancelAnimationFrame(raf);
      window.removeEventListener("scroll", scheduleActiveLineUpdate);
      window.removeEventListener("resize", scheduleActiveLineUpdate);
    };
  }, [sections]);

  const railStyle = {
    height: `${railHeightPx(viewportHeight)}px`,
    ["--scroll-rail-line-gap" as string]: `${RAIL_LINE_GAP}px`,
    ["--scroll-rail-height-ratio" as string]: String(RAIL_HEIGHT_RATIO),
  };

  return (
    <nav
      className={[
        "scroll-rail",
        collapsed ? "scroll-rail--collapsed" : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-label="媒体批次导航"
      style={railStyle}
    >
      <div className="scroll-rail-track">
        {flatLines.map((line) => (
          <button
            key={line.key}
            type="button"
            className="scroll-rail-line-btn"
            data-kind={line.kind}
            data-active={line.active ? "true" : undefined}
            onClick={line.onClick}
            aria-label={`跳转到${line.label}`}
            aria-current={line.active ? "true" : undefined}
            data-label={line.label}
          >
            <span className="scroll-rail-line" aria-hidden="true" />
            <span className="scroll-rail-label">{line.label}</span>
          </button>
        ))}
      </div>
    </nav>
  );
}
