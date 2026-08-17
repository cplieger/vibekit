package server

// The readiness reason for a dead sign-in.
//
// kiro-cli owns the login store and the rotating refresh chain; vibekit only
// asks it for a KAS access token when a session opens (_kiro/auth/getAccessToken,
// internal/hub/bridge_v3_auth.go). When that vend fails, KAS runs
// UNAUTHENTICATED rather than refusing: sessions still open, and then every
// service-backed surface fails. So the fact has to be reported, and readiness is
// where an operator and a monitor both look.
//
// The prefix is load-bearing in TWO directions and neither is cosmetic:
//
//   - It must NOT begin with "kiro-cli". static-src/runtime-health.ts decides a
//     verdict is a kiro-cli INSTALL verdict at all by that prefix, then keys its
//     copy on each install literal in full — so "kiro-cli auth expired" would
//     match the family, miss every key, and render the terminal "the install
//     failed and its retries are exhausted; restart the container" copy. A wrong
//     answer with the right status.
//   - It IS the prefix runtime-health.ts matches for the sign-in family, whose
//     banner offers the login modal instead of Run Diagnostics.
//
// Fixed literal, no interpolation: /api/health is unauthenticated and kiro-cli's
// error text can name a path on the volume. The specific failure is in the log
// line and on the SSE error frame, both of which have somewhere safer to go.
//
// TestAuthReasonIsTheClientContract pins it. Change it here and change
// runtime-health.ts in the same commit.
const reasonSignIn = "sign-in required"
