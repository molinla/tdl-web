import { useEffect, useMemo, useState } from "react";
import {
  buildRailLayout,
  CATEGORY_LINES_PER_SECTION,
  RAIL_HEIGHT_RATIO,
  RAIL_LINE_GAP,
  railHeightPx,
  scrollToItem,
  type RailSectionLayout,
} from "./scrollNavigation";
import type { Item } from "./types";

const SECTION_META = [
  { id: "section-videos", label: "视频" },
  { id: "section-images", label: "图片" },
  { id: "section-files", label: "文件" },
] as const;

type ScrollRailProps = {
  collapsed?: boolean;
  videos: Item[];
  images: Item[];
  files: Item[];
};

type FlatRailLine = {
  key: string;
  globalIndex: number;
  kind: "section" | "batch";
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
  videos,
  images,
  files,
}: ScrollRailProps) {
  const viewportHeight = useViewportHeight();

  const sections = useMemo<RailSectionLayout[]>(
    () =>
      buildRailLayout(viewportHeight, [
        { id: SECTION_META[0].id, label: SECTION_META[0].label, items: videos },
        { id: SECTION_META[1].id, label: SECTION_META[1].label, items: images },
        { id: SECTION_META[2].id, label: SECTION_META[2].label, items: files },
      ]),
    [viewportHeight, videos, images, files],
  );

  const itemBatchMap = useMemo(() => {
    const map = new Map<string, string>();
    for (const section of sections) {
      for (const batch of section.batches) {
        for (const item of batch.items) {
          map.set(item.id, batch.id);
        }
      }
    }
    return map;
  }, [sections]);

  const [activeSectionId, setActiveSectionId] = useState<string>(
    SECTION_META[0].id,
  );
  const [activeItemId, setActiveItemId] = useState<string | null>(null);
  const activeBatchId = activeItemId
    ? (itemBatchMap.get(activeItemId) ?? null)
    : null;

  const flatLines = useMemo(() => {
    const lines: FlatRailLine[] = [];
    let globalIndex = 0;

    for (const section of sections) {
      const sectionActive =
        activeSectionId === section.id &&
        (!activeBatchId ||
          !section.batches.some((batch) => batch.id === activeBatchId));

      for (let i = 0; i < CATEGORY_LINES_PER_SECTION; i += 1) {
        lines.push({
          key: `${section.id}-marker-${i}`,
          globalIndex,
          kind: "section",
          label: section.label,
          active: sectionActive,
          onClick: () => {
            document.getElementById(section.id)?.scrollIntoView({
              behavior: "smooth",
              block: "start",
            });
          },
        });
        globalIndex += 1;
      }

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
  }, [sections, activeSectionId, activeBatchId]);

  useEffect(() => {
    const sectionEls = SECTION_META.map((s) => document.getElementById(s.id)).filter(
      (el): el is HTMLElement => el != null,
    );
    if (!sectionEls.length) return;

    const sectionObserver = new IntersectionObserver(
      (entries) => {
        const visible = entries
          .filter((e) => e.isIntersecting)
          .sort((a, b) => b.intersectionRatio - a.intersectionRatio);
        if (visible[0]?.target.id) {
          const nextId = visible[0].target.id;
          setActiveSectionId((prev) => (prev === nextId ? prev : nextId));
        }
      },
      { rootMargin: "-12% 0px -58% 0px", threshold: [0, 0.15, 0.35, 0.6] },
    );

    for (const el of sectionEls) sectionObserver.observe(el);
    return () => sectionObserver.disconnect();
  }, []);

  useEffect(() => {
    let raf = 0;
    let lastId: string | null = null;

    function updateActiveItem() {
      raf = 0;
      const nodes = document.querySelectorAll<HTMLElement>("[data-scroll-item]");
      const viewportHeight = readViewportHeight();
      const center = viewportHeight * 0.42;
      let bestId: string | null = null;
      let bestDist = Number.POSITIVE_INFINITY;

      nodes.forEach((el) => {
        const rect = el.getBoundingClientRect();
        if (rect.bottom < 0 || rect.top > viewportHeight) return;
        const itemCenter = rect.top + rect.height / 2;
        const dist = Math.abs(itemCenter - center);
        if (dist < bestDist) {
          bestDist = dist;
          bestId = el.dataset.scrollItem ?? null;
        }
      });

      if (bestId !== lastId) {
        lastId = bestId;
        setActiveItemId(bestId);
      }
    }

    function scheduleActiveItemUpdate() {
      if (raf) return;
      raf = window.requestAnimationFrame(updateActiveItem);
    }

    scheduleActiveItemUpdate();
    window.addEventListener("scroll", scheduleActiveItemUpdate, {
      passive: true,
    });
    window.addEventListener("resize", scheduleActiveItemUpdate);
    return () => {
      if (raf) window.cancelAnimationFrame(raf);
      window.removeEventListener("scroll", scheduleActiveItemUpdate);
      window.removeEventListener("resize", scheduleActiveItemUpdate);
    };
  }, [videos, images, files]);

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
      aria-label="内容分区导航"
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
            aria-label={
              line.kind === "section"
                ? `跳转到${line.label}`
                : `跳转到${line.label}`
            }
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
