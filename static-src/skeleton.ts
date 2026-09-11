import { el } from "@cplieger/reactive";

import { KEY_ATTR } from "./reconcile.js";

// ---------------------------------------------------------------------------
// Skeleton loading placeholders for perceived performance.
// ---------------------------------------------------------------------------

/** Mount a placeholder into `host`, or refuse. THE ONE DOOR every container
 *  placeholder mounts through, so a surface whose own arm is wrong shows no
 *  placeholder rather than stacking one under the content.
 *
 *  `content` names what counts as content IN THIS HOST — the surfaces disagree.
 *  `mount: "replace"` also clears what the guard cannot see (an error row, a
 *  banner). Returns the teardown, or a no-op on a refusal, so a caller hands it
 *  to `skeletonTiming` either way. */
export function paintPlaceholder(
  host: Element | null,
  build: () => Element,
  opts?: { readonly content?: string; readonly mount?: "replace" | "append" },
): () => void {
  // Both refusals in one test: an absent host answers `undefined` and a populated
  // one answers an Element, so only an empty host reaches the paint.
  if (host?.querySelector(opts?.content ?? `[${KEY_ATTR}]`) !== null) {
    return () => {
      /* no host, or one already holding content */
    };
  }
  const node = build();
  if ((opts?.mount ?? "replace") === "append") {
    host.appendChild(node);
  } else {
    host.replaceChildren(node);
  }
  return () => {
    node.remove();
  };
}

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

/** Placeholder for the editor's document pane: the file's opening declaration
 *  line, a blank line, then body lines.
 *
 *  Every bar occupies exactly ONE of the pane's line boxes, so N bars stand on the
 *  first N real lines and the file landing moves nothing. The geometry is the
 *  pane's own (`.editor-skeleton` in 30-utilities.css measures in `em`, and the
 *  mount sits inside `#editor-code`, which inherits the mono metrics), so a change
 *  to that font-size carries the placeholder with it. */
export function editorDocSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "editor-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  wrap.appendChild(skelBar("editor-skel-title", "38%"));
  // Widths only: a source file's shape is what makes this read as a document
  // rather than a block, and an empty string is the blank line between the
  // declaration and the body.
  for (const width of ["", "72%", "54%", "83%", "41%", "", "66%", "78%", "49%", "60%"]) {
    const line = el("div", { className: "editor-skel-line" });
    if (width !== "") {
      line.classList.add("skeleton");
      line.style.width = width;
    }
    wrap.appendChild(line);
  }
  return wrap;
}

/** Placeholder rows for the file browser's listing: a name plus the size and date
 *  the meta column carries, one row per entry.
 *
 *  The row wears `.fb-row` itself, so its height, padding, gap and bottom rule are
 *  a real row's rather than a copy of them, and adopting the listing moves nothing.
 *  The `.fb-check` column is RESERVED without a bar: a placeholder checkbox says
 *  nothing, and dropping the column instead would step the icon and every name
 *  left by 1.5rem the moment the listing lands. */
export function fileRowsSkeleton(): HTMLDivElement {
  const wrap = el("div", {
    className: "fb-skeleton",
    "aria-hidden": "true",
  }) as HTMLDivElement;
  for (const width of ["62%", "38%", "71%", "45%", "56%", "33%", "68%", "49%"]) {
    const row = el("div", { className: "fb-row fb-row-skel" });
    const name = el("div", { className: "fb-skel-name" });
    name.appendChild(skelBar("skeleton-line", width));
    row.append(
      el("div", { className: "fb-skel-check" }),
      skelBar("fb-skel-icon", "1.25rem"),
      name,
      skelBar("skeleton-line fb-skel-meta", "3rem"),
      skelBar("skeleton-line fb-skel-meta", "9rem"),
    );
    wrap.appendChild(row);
  }
  return wrap;
}

function skelBar(className: string, width: string): HTMLElement {
  const bar = el("div", { className: `skeleton ${className}` });
  bar.style.width = width;
  return bar;
}
