package api

// Wire types for the tools engine (Settings -> Tools). The engine is
// the cplieger/toolbelt library; these aliases keep the shapes in the
// api contract hub so wiregen exports them to the client
// (static-src/wire/) alongside the SSE payloads. The REST layer is
// toolbelt/httpapi, mounted by internal/server; its response envelopes
// are aliased here for the same reason.

import (
	"github.com/cplieger/toolbelt/v2"
	"github.com/cplieger/toolbelt/v2/httpapi"
)

// ToolJob is one tools-engine job: an install/uninstall/disable/
// update/reconcile run on the single-flight queue. Terminal states are
// done, failed, and cancelled.
type ToolJob = toolbelt.Job

// ToolInfo is one tool row in GET /api/tools: the manifest entry
// joined with the engine's install state. Disabled marks a template
// (recorded intent, nothing installed); Lsp marks a language server.
type ToolInfo = toolbelt.ToolInfo

// SystemTool is one image-baked binary surfaced read-only in the UI.
type SystemTool = toolbelt.SystemTool

// ToolsList is the GET /api/tools response body.
type ToolsList = toolbelt.Inventory

// ToolCatalogHit is one catalog search result (GET /api/tools/search).
type ToolCatalogHit = httpapi.SearchHit

// ToolsSearchResponse is the GET /api/tools/search response body.
type ToolsSearchResponse = httpapi.SearchResponse

// ToolJobAccepted is the 202 response body for job-enqueuing tool
// mutations (add, install, update, patch). The job is null when the
// mutation needed none (e.g. adding a disabled template).
type ToolJobAccepted = httpapi.JobResponse

// ToolsJobsResponse is the GET /api/tools/jobs response body.
type ToolsJobsResponse = httpapi.JobsResponse

// ToolRemoveResponse is the 202 response body for DELETE
// /api/tools/{name}; dependents is populated on forced cascades.
type ToolRemoveResponse = httpapi.RemoveResponse
