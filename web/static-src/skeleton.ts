// ---------------------------------------------------------------------------
// Skeleton loading placeholders for perceived performance.
// ---------------------------------------------------------------------------

/** Build a skeleton message group simulating a chat conversation. */
export function chatSkeleton(): HTMLDivElement {
  const wrap = document.createElement("div");
  wrap.className = "skeleton-msg-group";
  // Simulate: user message, tool calls, assistant reply.
  const patterns: Array<{ side: "left" | "right"; widths: string[]; isTool?: boolean }> = [
    { side: "right", widths: ["60%"] },
    { side: "left", widths: ["40%"] },
    { side: "left", widths: ["35%"], isTool: true },
    { side: "left", widths: ["35%"], isTool: true },
    { side: "left", widths: ["80%", "65%", "45%"] },
  ];
  for (const p of patterns) {
    if (p.isTool === true) {
      const tool = document.createElement("div");
      tool.className = "skeleton skeleton-tool";
      tool.style.width = p.widths[0]!;
      wrap.appendChild(tool);
      continue;
    }
    const row = document.createElement("div");
    row.className = "skeleton-row";
    if (p.side === "right") row.style.flexDirection = "row-reverse";
    const avatar = document.createElement("div");
    avatar.className = "skeleton skeleton-avatar";
    const bubble = document.createElement("div");
    bubble.className = "skeleton skeleton-bubble";
    bubble.style.width = "min(70%, 28rem)";
    for (const w of p.widths) {
      const line = document.createElement("div");
      line.className = "skeleton skeleton-line";
      line.style.width = w;
      bubble.appendChild(line);
    }
    row.appendChild(avatar);
    row.appendChild(bubble);
    wrap.appendChild(row);
  }
  return wrap;
}

/** Build a skeleton for the git panel (branch bar + file rows). */
export function gitSkeleton(): HTMLDivElement {
  const wrap = document.createElement("div");
  wrap.className = "skeleton-git-group";
  const bar = document.createElement("div");
  bar.className = "skeleton skeleton-git-row";
  bar.style.width = "60%";
  wrap.appendChild(bar);
  const widths = ["75%", "60%", "85%", "55%"];
  for (let i = 0; i < 4; i++) {
    const row = document.createElement("div");
    row.className = "skeleton skeleton-git-row";
    row.style.width = widths[i]!;
    wrap.appendChild(row);
  }
  return wrap;
}

/** Build a small skeleton for the "loading more" indicator at the top of messages. */
export function loadMoreSkeleton(): HTMLDivElement {
  const wrap = document.createElement("div");
  wrap.className = "skeleton-msg-group";
  wrap.style.paddingBlock = "var(--sp-2)";
  for (let i = 0; i < 3; i++) {
    const row = document.createElement("div");
    row.className = "skeleton-row";
    if (i === 0) row.style.flexDirection = "row-reverse";
    const avatar = document.createElement("div");
    avatar.className = "skeleton skeleton-avatar";
    const bubble = document.createElement("div");
    bubble.className = "skeleton skeleton-bubble";
    bubble.style.width = `${String(40 + i * 15)}%`;
    const line = document.createElement("div");
    line.className = "skeleton skeleton-line";
    line.style.width = "80%";
    bubble.appendChild(line);
    row.appendChild(avatar);
    row.appendChild(bubble);
    wrap.appendChild(row);
  }
  return wrap;
}
