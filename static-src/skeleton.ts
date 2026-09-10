import { el } from "@cplieger/reactive";

// ---------------------------------------------------------------------------
// Skeleton loading placeholders for perceived performance.
// ---------------------------------------------------------------------------

/** The transcript placeholder's element id. The renderer drops it by this id
 *  when real turns land, so the placeholder and the conversation can never share
 *  the container. */
export const CHAT_SKELETON_ID = "chat-skeleton";

/** Build a skeleton message group simulating a chat conversation. */
export function chatSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-msg-group",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  // Carries its id from here rather than from the caller (unlike
  // `load-more-skeleton`, which scroll.ts stamps) because TWO modules address it:
  // chat.ts mounts it and messages.ts drops it the moment real turns land. A
  // literal in both would be a coupling that can drift silently.
  wrap.id = CHAT_SKELETON_ID;
  // Simulate: user message, tool calls, assistant reply.
  const patterns: { side: "left" | "right"; widths: string[]; isTool?: boolean }[] = [
    { side: "right", widths: ["60%"] },
    { side: "left", widths: ["40%"] },
    { side: "left", widths: ["35%"], isTool: true },
    { side: "left", widths: ["35%"], isTool: true },
    { side: "left", widths: ["80%", "65%", "45%"] },
  ];
  for (const p of patterns) {
    if (p.isTool === true) {
      const tool = el("div", { className: "skeleton skeleton-tool" });
      // eslint-disable-next-line @typescript-eslint/no-non-null-assertion
      tool.style.width = p.widths[0]!;
      wrap.appendChild(tool);
      continue;
    }
    const row = el("div", { className: "skeleton-row" });
    if (p.side === "right") {
      row.style.flexDirection = "row-reverse";
    }
    const avatar = el("div", { className: "skeleton skeleton-avatar" });
    const bubble = el("div", { className: "skeleton skeleton-bubble" });
    bubble.style.width = "min(70%, 28rem)";
    for (const w of p.widths) {
      const line = el("div", { className: "skeleton skeleton-line" });
      line.style.width = w;
      bubble.appendChild(line);
    }
    row.appendChild(avatar);
    row.appendChild(bubble);
    wrap.appendChild(row);
  }
  return wrap;
}

/** Build a small skeleton for the "loading more" indicator at the top of messages. */
export function loadMoreSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "skeleton-msg-group",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  wrap.style.paddingBlock = "var(--sp-2)";
  for (let i = 0; i < 3; i++) {
    const row = el("div", { className: "skeleton-row" });
    if (i === 0) {
      row.style.flexDirection = "row-reverse";
    }
    const avatar = el("div", { className: "skeleton skeleton-avatar" });
    const bubble = el("div", { className: "skeleton skeleton-bubble" });
    bubble.style.width = `${String(40 + i * 15)}%`;
    const line = el("div", { className: "skeleton skeleton-line" });
    line.style.width = "80%";
    bubble.appendChild(line);
    row.appendChild(avatar);
    row.appendChild(bubble);
    wrap.appendChild(row);
  }
  return wrap;
}

/** Placeholder per-repo sections for the git view's two tabs.
 *
 *  ONE painter for both, because both tabs answer the same question while they
 *  load — "a per-repo list is coming" — and both paint into the same
 *  `.git-repo-section` shape afterwards. Two copies of that would be two things
 *  to keep in step with a section header that has one definition.
 *
 *  Geometry mirrors a real section header (forge icon, repo name, a trailing
 *  count) because a placeholder's job is to reserve the shape the content takes
 *  rather than to draw attention. `aria-hidden` because both mounts are
 *  `aria-live="polite"`: announcing placeholder bars, and then a count that ticks
 *  once per repository, is pure noise.
 *
 *  `label` is the PR tab's fan-out counter and is what separates a slow refresh
 *  from a wedged one; the Changes tab issues ONE request, so it has no count to
 *  report and passes none. The returned `label` element is handed back rather
 *  than looked up again, because the caller updates it in place as the fan-out
 *  reports and a querySelector would tie that to the class name. */
export function gitRepoSkeleton(opts: { readonly label?: string; readonly widths: string[] }): {
  readonly wrap: HTMLDivElement;
  readonly label: HTMLElement | null;
} {
  const wrap = el("div", {
    className: "git-repo-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  let label: HTMLElement | null = null;
  if (opts.label !== undefined) {
    label = el("div", { className: "git-repo-skel-label" }, opts.label);
    wrap.appendChild(label);
  }
  for (const width of opts.widths) {
    const section = el("div", { className: "git-repo-section git-repo-skel-section" });
    section.append(
      el("div", { className: "skeleton git-repo-skel-icon" }),
      skelBar("git-repo-skel-name", width),
      skelBar("git-repo-skel-meta", "4rem"),
    );
    wrap.appendChild(section);
  }
  return { wrap, label };
}

function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}
