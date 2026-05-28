// ---------------------------------------------------------------------------
// Pure geometry helpers for virtual scrolling (stateless, unit-testable
// without DOM). Extracted from follow.ts.
// ---------------------------------------------------------------------------

export interface VirtualWindow {
  startLine: number;
  endLine: number;
  paddingTopPx: number;
  paddingBottomPx: number;
}

export function computeVirtualWindow(
  totalLines: number,
  lineHeight: number,
  scrollTop: number,
  viewportHeight: number,
  bufferLines: number,
): VirtualWindow {
  const startLine = Math.max(0, Math.floor(scrollTop / lineHeight) - bufferLines);
  const visibleCount = Math.ceil(viewportHeight / lineHeight) + bufferLines * 2;
  const endLine = Math.min(totalLines, startLine + visibleCount);
  const totalHeight = totalLines * lineHeight;
  return {
    startLine,
    endLine,
    paddingTopPx: startLine * lineHeight,
    paddingBottomPx: Math.max(0, totalHeight - endLine * lineHeight),
  };
}

export function computeScrollTarget(
  line: number,
  lineHeight: number,
  viewportHeight: number,
): number {
  const targetTop = (line - 1) * lineHeight;
  return Math.max(0, targetTop - viewportHeight / 2);
}
