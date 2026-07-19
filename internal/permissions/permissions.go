// Package permissions holds vibekit's one remaining permission control:
// the Supervised-mode default for newly-auto-created chats
// (SupervisedDefault, permissions_read.go). It fails CLOSED to false —
// Supervised mode is opt-in; a corrupt config.json must not suddenly gate
// every write on approval.
//
// Tool-call authorization on v3 (KAS) is owned end to end by kiro-cli's
// native Cedar policy engine, surfaced and edited via GET|POST
// /api/permissions (see internal/policyfile). vibekit runs no classifier
// of its own and never auto-answers a permission request.
package permissions
