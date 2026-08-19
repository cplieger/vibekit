// ---------------------------------------------------------------------------
// File-role rendering for a workspace file the agent presented in an image
// position: `![label](/workspace/out/clip.mp3)`.
//
// NATIVE ELEMENTS ONLY, no dependency. The browser already ships an audio player
// with a transport bar, a timeline and keyboard support, and it degrades to its
// own fallback content when the codec is missing — so the role is expressed by
// choosing the right ELEMENT, not by building a widget.
//
// Three roles, and only two of them are this module's:
//
//   image  ->  the `<img>` the parser already built, with its src rewritten at
//              the file route. Already shipped (utils-url.ts
//              `rewriteWorkspaceImageSrc`); this module returns null for it
//              rather than building a second path.
//   audio  ->  `<audio controls>`.
//   other  ->  a download affordance, which is what the file IS to a reader.
//
// The `![](…)` door is the honest one, and it is deliberately the only one. A
// transcript file reference otherwise exists as linkified prose (which requires a
// `/` and an extension in FILE_EXTS — and FILE_EXTS carries no audio extensions,
// so audio is not linkified at all) or as a tool card's subject, which is an EDIT
// subject rather than a presentation. There is no `resource_link` block and no
// file block on the transcript wire: `vibekit.BlockType` is text / tool_use /
// thinking, full stop. So `![](…)` is the one place the agent CHOSE to present a
// file, which is exactly the signal a card should key on.
// ---------------------------------------------------------------------------

import { el } from "@cplieger/reactive";
import { isPlayableAudio, isViewableImage } from "./file-extensions.js";
import { fileDownloadURL } from "./utils-url.js";

/** Only a workspace-rooted path is ours. A remote URL in an image position is
 *  the author's own business and stays an `<img>`, exactly as it does today. */
const WORKSPACE_PREFIX = "/workspace/";

/** The element a workspace file in an image position deserves — or null when the
 *  `<img>` the parser already built is the right answer (a remote URL, or an
 *  image, whose src rewrite is already in place).
 *
 *  `alt` is the markdown label, which by this point is complete: the parser emits
 *  a link's text when `]` closes and its URL only when `)` does, so the alt has
 *  already landed on the `<img>` by the time a src arrives. */
export function mediaElementFor(src: string, alt: string): HTMLElement | null {
  const path = src.trim();
  if (!path.startsWith(WORKSPACE_PREFIX) || isViewableImage(path)) {
    return null;
  }
  const url = fileDownloadURL(path);
  const label = alt !== "" ? alt : (path.split("/").pop() ?? path);

  if (isPlayableAudio(path)) {
    const audio = el("audio", { className: "media-audio" }) as HTMLAudioElement;
    audio.controls = true;
    // `metadata`, not `auto`: a transcript can hold several clips and none of
    // them was asked for, but the transport bar needs a duration to draw.
    audio.preload = "metadata";
    audio.src = url;
    // Fallback content for a browser that cannot play the codec. It also gives
    // the label somewhere to live, which a void `<img>` never had — the alt text
    // there was reachable only by inspecting the element.
    audio.appendChild(document.createTextNode(label));
    return audio;
  }

  // An anchor is safe HERE and only here. `/api/file/download` answers with
  // `Content-Disposition: attachment`, so the browser saves the file instead of
  // rendering it as a document — and an image extension never reaches this
  // branch, `.svg` included, which is what keeps a script-capable
  // `image/svg+xml` response off the end of a same-origin link. Do not lift this
  // anchor into the image path.
  const link = el("a", { className: "media-download", href: url }, `⬇ ${label}`);
  link.setAttribute("download", "");
  return link;
}
