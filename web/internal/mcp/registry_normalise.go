// Registry response normalisation.
//
// Pure-logic types and function that transform the upstream registry
// JSON into a compact browser-facing shape. Separated from the proxy's
// HTTP handler + caching concerns (registry_proxy.go) because this code
// changes for a different reason: upstream schema evolution.

package mcp

import "encoding/json"

// --- Normalisation ---
//
// The upstream response nests every record in `{server: {...}, _meta: {...}}`
// and carries fields we don't surface (schema URLs, timestamps, OIDC
// metadata). We flatten to one compact shape so the UI doesn't repeat
// the same field-plumbing logic.

// RegistryEntry is the browser-facing shape of one search result.
type RegistryEntry struct {
	Name        string            `json:"name"`
	Title       string            `json:"title,omitempty"`
	Description string            `json:"description,omitempty"`
	Version     string            `json:"version,omitempty"`
	Repository  string            `json:"repository,omitempty"`
	Packages    []RegistryPackage `json:"packages,omitempty"`
	Remotes     []RegistryRemote  `json:"remotes,omitempty"`
}

// RegistryPackage is one install option from a stdio-speaking server.
// Only npm and oci are surfaced; everything else is hidden so the UI
// doesn't offer install paths we can't fulfil on the container.
type RegistryPackage struct {
	RegistryType string           `json:"registry_type"`
	Identifier   string           `json:"identifier"`
	Version      string           `json:"version,omitempty"`
	EnvVars      []RegistryEnvVar `json:"env_vars,omitempty"`
}

// RegistryRemote is one remote transport (http/sse) option.
type RegistryRemote struct {
	Type    string           `json:"type"`
	URL     string           `json:"url"`
	Headers []RegistryHeader `json:"headers,omitempty"`
}

// RegistryEnvVar / RegistryHeader describe a configurable field the user
// must fill in before the server will run.
type RegistryEnvVar struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Format      string `json:"format,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

type RegistryHeader struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Value       string `json:"value,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Secret      bool   `json:"secret,omitempty"`
}

// supportedPackageRegistries defines which package registry types
// vibekit can install. Only npm is supported (via npx -y).
// This is the single source of truth for install-capability gating:
// both normaliseRegistryResponse (which filters registry search results
// for the UI) and extractNpxPackage in prewarm.go (which decides what
// to pre-install) reference this map.
var supportedPackageRegistries = map[string]bool{"npm": true}

// supportedPackageTransports defines which transport types are valid
// for npm packages. Empty string means "default stdio".
// Shared with prewarm.go's extractNpxPackage: a server is only
// prewarm-eligible if its transport is in this set, ensuring the
// invariant "prewarm only targets packages the registry would surface".
var supportedPackageTransports = map[string]bool{"stdio": true, "": true}

// supportedRemoteTypes maps upstream remote type strings to the local
// Transport enum. Only these remote types are surfaced to the UI.
var supportedRemoteTypes = map[string]Transport{
	"streamable-http": TransportHTTP,
	"http":            TransportHTTP,
	"sse":             TransportHTTP,
}

func normaliseRegistryResponse(body []byte) []RegistryEntry {
	var raw struct {
		Servers []struct {
			Server struct {
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Version     string `json:"version"`
				Repository  struct {
					URL string `json:"url"`
				} `json:"repository"`
				Packages []struct {
					RegistryType string `json:"registryType"`
					Identifier   string `json:"identifier"`
					Version      string `json:"version"`
					Transport    struct {
						Type string `json:"type"`
					} `json:"transport"`
					EnvironmentVariables []struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Format      string `json:"format"`
						IsRequired  bool   `json:"isRequired"`
						IsSecret    bool   `json:"isSecret"`
					} `json:"environmentVariables"`
				} `json:"packages"`
				Remotes []struct {
					Type    string `json:"type"`
					URL     string `json:"url"`
					Headers []struct {
						Name        string `json:"name"`
						Description string `json:"description"`
						Value       string `json:"value"`
						IsRequired  bool   `json:"isRequired"`
						IsSecret    bool   `json:"isSecret"`
					} `json:"headers"`
				} `json:"remotes"`
			} `json:"server"`
		} `json:"servers"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return []RegistryEntry{}
	}
	out := make([]RegistryEntry, 0, len(raw.Servers))
	for i := range raw.Servers {
		srv := &raw.Servers[i].Server
		entry := RegistryEntry{
			Name:        srv.Name,
			Title:       srv.Title,
			Description: srv.Description,
			Version:     srv.Version,
			Repository:  srv.Repository.URL,
		}
		for j := range srv.Packages {
			pkg := &srv.Packages[j]
			if !supportedPackageRegistries[pkg.RegistryType] {
				continue
			}
			if !supportedPackageTransports[pkg.Transport.Type] {
				continue
			}
			pe := RegistryPackage{
				RegistryType: pkg.RegistryType,
				Identifier:   pkg.Identifier,
				Version:      pkg.Version,
			}
			for k := range pkg.EnvironmentVariables {
				env := &pkg.EnvironmentVariables[k]
				pe.EnvVars = append(pe.EnvVars, RegistryEnvVar{
					Name:        env.Name,
					Description: env.Description,
					Format:      env.Format,
					Required:    env.IsRequired,
					Secret:      env.IsSecret,
				})
			}
			entry.Packages = append(entry.Packages, pe)
		}
		for j := range srv.Remotes {
			rem := &srv.Remotes[j]
			transport, ok := supportedRemoteTypes[rem.Type]
			if !ok {
				continue
			}
			re := RegistryRemote{Type: string(transport), URL: rem.URL}
			for k := range rem.Headers {
				h := &rem.Headers[k]
				re.Headers = append(re.Headers, RegistryHeader{
					Name:        h.Name,
					Description: h.Description,
					Value:       h.Value,
					Required:    h.IsRequired,
					Secret:      h.IsSecret,
				})
			}
			entry.Remotes = append(entry.Remotes, re)
		}
		// Skip entries with zero usable install paths. Common for schema-
		// only publications or packages using registries we don't support.
		if len(entry.Packages) == 0 && len(entry.Remotes) == 0 {
			continue
		}
		out = append(out, entry)
	}
	return out
}
