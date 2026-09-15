// The SSE-Client tag: which browser PROFILE a stream belongs to, so the server's
// presence table can fold every tab of one profile and the push filter can match a
// row to the subscription it silences.
//
// The tag is DERIVED from the push subscription endpoint, identically to
// internal/push/tag.go: base64url(sha256(endpoint)) truncated to 22 characters. The
// endpoint is a capability URL, so the tag is unforgeable without it, and no server
// field has to be persisted to pair the two. A profile with no subscription presents
// a random 22-character tag kept under the same key, so its presence still counts
// and it is simply never a push target.
//
// The derivation is asynchronous (crypto.subtle) and the stream opens during init, so
// the first connect presents whatever this module holds: the derived tag a previous
// boot persisted, or the random fallback. adoptSubscriptionTag runs once the
// subscription resolves and reconnects only when the tag actually changed.

/** The `localStorage` key under which this browser profile's tag lives. */
export const PROFILE_TAG_KEY = "vibekit.sse-client";

/** The header's grammar (webhttp.ValidRequestID); a stored value outside it is
 *  treated as absent, exactly as the server treats the header. */
const TAG_GRAMMAR = /^[A-Za-z0-9_-]{1,64}$/;

/** Characters the derived tag keeps: 22 of base64url carry 132 bits. */
const TAG_LEN = 22;

/** The tag this profile presents on a connect: the persisted one when it is well
 *  formed, else a fresh random one, persisted so the next boot presents the same. */
export function persistedTag(): string {
  const held = readTag();
  if (held !== null) {
    return held;
  }
  const tag = randomTag();
  writeTag(tag);
  return tag;
}

/** The presence tag of a push subscription: the twin of push.TagOf. */
export async function derivedTag(endpoint: string): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(endpoint));
  return base64url(new Uint8Array(digest)).slice(0, TAG_LEN);
}

/** Adopt the tag of the subscription this profile holds. When it differs from the
 *  tag presented so far the new one is persisted and `reconnect` runs once, so the
 *  next hello and every acknowledgement after it carry the derived tag; when it is
 *  the same nothing moves. A null subscription (no push on this profile) keeps the
 *  presented tag. Resolves to the tag in force afterwards. */
export async function adoptSubscriptionTag(
  sub: { readonly endpoint: string } | null,
  presented: string,
  reconnect: (tag: string) => void,
): Promise<string> {
  if (sub === null) {
    return presented;
  }
  const tag = await derivedTag(sub.endpoint);
  if (tag === presented) {
    return tag;
  }
  writeTag(tag);
  reconnect(tag);
  return tag;
}

function readTag(): string | null {
  let held: string | null = null;
  try {
    held = localStorage.getItem(PROFILE_TAG_KEY);
  } catch {
    /* storage refused; a per-document tag still counts this connection */
  }
  return held !== null && TAG_GRAMMAR.test(held) ? held : null;
}

function writeTag(tag: string): void {
  try {
    localStorage.setItem(PROFILE_TAG_KEY, tag);
  } catch {
    /* storage refused; the tag lives for this document */
  }
}

/** 16 random bytes as 22 base64url characters: inside the header's grammar and the
 *  same width as the derived tag, so a row cannot tell the two apart. */
function randomTag(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  return base64url(bytes);
}

function base64url(bytes: Uint8Array): string {
  let bin = "";
  for (const b of bytes) {
    bin += String.fromCharCode(b);
  }
  return btoa(bin).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}
