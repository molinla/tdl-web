import assert from "node:assert/strict";
import test from "node:test";
import {
  buildRailLayout,
  railBatchIndexAtPosition,
} from "../src/scrollNavigation.ts";

test("rail batches are balanced by item count", () => {
  const videos = Array.from({ length: 90 }, (_, index) => ({
    id: `video-${index}`,
    date: 2_000_000_000 - index,
  }));
  const images = Array.from({ length: 10 }, (_, index) => ({
    id: `image-${index}`,
    date: 2_000_000_000 - index * 100_000_000,
  }));

  const [videoSection, imageSection] = buildRailLayout(1_000, [
    { id: "videos", label: "Videos", items: videos },
    { id: "images", label: "Images", items: images },
    { id: "files", label: "Files", items: [] },
  ]);

  assert.ok(videoSection && imageSection);
  assert.ok(videoSection.batches.length > imageSection.batches.length);

  for (const section of [videoSection, imageSection]) {
    const sizes = section.batches.map((batch) => batch.items.length);
    assert.ok(Math.max(...sizes) - Math.min(...sizes) <= 1);
  }
});

test("rail batch index only moves forward as section position increases", () => {
  const indices = Array.from({ length: 1_001 }, (_, position) =>
    railBatchIndexAtPosition(position, 1_000, 12),
  );

  assert.equal(indices[0], 0);
  assert.equal(indices.at(-1), 11);
  for (let i = 1; i < indices.length; i += 1) {
    assert.ok((indices[i] ?? 0) >= (indices[i - 1] ?? 0));
  }
});
