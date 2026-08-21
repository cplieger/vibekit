package server

import (
	"net/http"
	"strings"

	"github.com/cplieger/vibekit/internal/httpreply"
	"github.com/cplieger/vibekit/internal/modeltext"
	"github.com/cplieger/vibekit/internal/vibekit"
	"github.com/cplieger/webhttp/v2"
)

// handleUtilityExplainError explains a tool error in plain language.
func (s *Server) handleUtilityExplainError(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if s.utilityPrompt == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(vibekit.ErrMsgUtilityUnavailable))
		return
	}
	var body struct {
		Error   string `json:"error"`
		Context string `json:"context"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Error == "" {
		httpreply.BadRequest(w, "error is required")
		return
	}
	prompt := "Explain this error in plain language. What went wrong and how to fix it? Be concise (2-3 sentences).\n\n"
	if body.Context != "" {
		prompt += "Context: " + body.Context + "\n\n"
	}
	prompt += "Error: " + body.Error
	result, err := s.utilityPrompt.UtilityPrompt(r.Context(), prompt, vibekit.EffortLow)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("generation failed"))
		return
	}
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: strings.TrimSpace(result)})
}

// handleUtilityResolveConflict proposes a merged version of a 3-way
// merge conflict hunk.
func (s *Server) handleUtilityResolveConflict(w http.ResponseWriter, r *http.Request) {
	if !requirePOST(w, r) {
		return
	}
	if s.utilityPrompt == nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON(vibekit.ErrMsgUtilityUnavailable))
		return
	}
	var body struct {
		Ours    string `json:"ours"`
		Theirs  string `json:"theirs"`
		Context string `json:"context"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Ours == "" && body.Theirs == "" {
		httpreply.BadRequest(w, "ours or theirs is required")
		return
	}
	const sideCap = 4 * 1024
	const ctxCap = 2 * 1024
	if len(body.Ours) > sideCap {
		body.Ours = body.Ours[:sideCap]
	}
	if len(body.Theirs) > sideCap {
		body.Theirs = body.Theirs[:sideCap]
	}
	if len(body.Context) > ctxCap {
		body.Context = body.Context[:ctxCap]
	}
	var sb strings.Builder
	sb.WriteString("Merge these two versions of the code from a 3-way merge conflict.\n")
	sb.WriteString("Return ONLY the merged code. No conflict markers (<<<<<<<, =======, >>>>>>>).\n")
	sb.WriteString("No explanation, no markdown fences, no prose. Preserve the intent of both changes.\n\n")
	if body.Context != "" {
		sb.WriteString("Surrounding context:\n```\n")
		sb.WriteString(body.Context)
		sb.WriteString("\n```\n\n")
	}
	sb.WriteString("Ours:\n```\n")
	sb.WriteString(body.Ours)
	sb.WriteString("\n```\n\nTheirs:\n```\n")
	sb.WriteString(body.Theirs)
	sb.WriteString("\n```\n\nMerged:")
	// Medium effort: merging two divergent code edits is the hardest
	// utility task; a low-effort merge tends to just pick one side.
	result, err := s.utilityPrompt.UtilityPrompt(r.Context(), sb.String(), vibekit.EffortMedium)
	if err != nil {
		webhttp.WriteJSONStatus(w, http.StatusServiceUnavailable, httpreply.ErrorJSON("generation failed"))
		return
	}
	webhttp.WriteJSON(w, map[string]string{jsonKeyOutput: modeltext.StripCodeFence(strings.TrimSpace(result))})
}
