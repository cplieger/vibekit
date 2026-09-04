// ---------------------------------------------------------------------------
// Who is signed in, and the third answer that is neither yes nor no.
//
// `GET /api/whoami` answers a three-state union — `signed_in{email}`,
// `signed_out`, `unavailable{reason}` — and the whole point of the third arm is
// that it must NOT read as a sign-out. The client used to read only `email` off
// a `{email, error}` shape, so a whoami hiccup took the not-authenticated branch
// and returned WITHOUT releasing the splash: measured on 3 of 88 reads, which
// sat behind an opaque overlay over a hidden app forever.
//
// One module because two callers ask the same question — the boot chain and the
// login modal's success path — and a second transcription of "which arm means
// come up anyway" is where the two would diverge. It maps and nothing else: what
// to RENDER for an unavailable identity is the boot chain's decision.
// ---------------------------------------------------------------------------

import { apiGetTyped } from "./api-client.js";
import { decodeWhoamiResponse } from "./wire/decoders.gen.js";

/** The three answers, plus the one the transport can produce.
 *
 *  A discriminated union rather than `{email, error}`: the arms are mutually
 *  exclusive and a reader's branch over them is total, which is what makes
 *  "unavailable" impossible to mistake for "signed out" at a call site. */
export type IdentityVerdict =
  | { state: "signed_in"; email: string }
  | { state: "signed_out" }
  | { state: "unavailable"; reason: string };

/** The reason a request that never reached the union carries.
 *
 *  A null response means the fetch failed or the body did not decode, which is
 *  the same CLAIM as the server's own `unavailable`: vibekit does not know who is
 *  signed in. Distinguished only by the reason, because the two have different
 *  remedies and a retry banner should be able to say which happened. */
const REASON_UNREACHABLE = "vibekit could not be reached";

/** Read the current identity. Never throws and never rejects: every failure IS
 *  the `unavailable` arm, so no caller has to re-derive the union from an
 *  exception. */
export async function resolveIdentity(): Promise<IdentityVerdict> {
  const d = await apiGetTyped("/api/whoami", decodeWhoamiResponse);
  if (d === null) {
    return { state: "unavailable", reason: REASON_UNREACHABLE };
  }
  switch (d.state) {
    case "signed_in":
      return { state: "signed_in", email: d.email ?? "" };
    case "signed_out":
      return { state: "signed_out" };
    case "unavailable":
      return { state: "unavailable", reason: d.reason ?? REASON_UNREACHABLE };
  }
}

/** The email a verdict names, or "" for the two arms that name nobody.
 *
 *  `unavailable` returning "" is what an initial read wants — there is no
 *  identity to show yet — and is why the login path does NOT use this: a login
 *  that just succeeded followed by an `unavailable` must leave the row it filled
 *  in alone rather than blanking it. See `emailToAdopt`. */
export function emailOf(v: IdentityVerdict): string {
  return v.state === "signed_in" ? v.email : "";
}

/** The email to write after a login, or `null` for "leave the row alone".
 *
 *  Only the `signed_in` arm carries one, and the other two must not blank a value
 *  the successful login already put on screen: a page that signs in and then hits
 *  a 5 s whoami timeout would otherwise clear the sidebar's email and read as a
 *  sign-out one frame after signing in. */
export function emailToAdopt(v: IdentityVerdict): string | null {
  return v.state === "signed_in" ? v.email : null;
}
