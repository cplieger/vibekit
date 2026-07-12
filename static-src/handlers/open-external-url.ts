// ---------------------------------------------------------------------------
// open_external_url handler.
//
// The v3 agent (KAS) asks the client to open a URL for the user — most
// often an MCP server's OAuth authorization page. Browsers popup-block a
// window.open() not driven by a user gesture, and this arrives as an SSE
// event, so we never auto-open. Instead we surface a persistent banner with
// a clickable link the user activates (mirrors the mcp_oauth_needed
// "Finish sign-in →" pill). Only http/https URLs are rendered.
//
// The URL is tied to a chat's bridge (the chat whose MCP OAuth flow ran),
// so the banner is keyed to that chat; banner-stack shows it when that chat
// is active. The same URL is also surfaced in Settings → Tools via the MCP
// row's OAuth pill, so this is an additive, view-independent affordance.
// ---------------------------------------------------------------------------

import { onSSE } from "../bus.js";
import { showBanner } from "../banner-stack.js";
import { isSafeURL } from "../url-safety.js";

onSSE("open_external_url", (chatID, p) => {
  if (chatID === "" || typeof p.url !== "string" || !isSafeURL(p.url)) {
    return;
  }
  showBanner(chatID, "open_external_url", "An integration needs you to sign in.", "info", true, {
    label: "Open sign-in page →",
    href: p.url,
  });
});
