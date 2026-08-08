const graphemeSegmenter = new Intl.Segmenter(undefined, { granularity: "grapheme" });

export function ellipsizeActionTarget(value: string): string {
  const graphemes = Array.from(graphemeSegmenter.segment(value), ({ segment }) => segment);
  if (graphemes.length <= 32) {
    return value;
  }
  return `${graphemes.slice(0, 31).join("")}…`;
}
