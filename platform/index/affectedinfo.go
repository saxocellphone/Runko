package index

import (
	"path"

	"github.com/saxocellphone/runko/platform/affected"
)

// AffectedProjectInfos maps the indexed tree to affected.Compute's input
// shape - the ONE place the mapping lives, so every consumer (merge gate,
// land revalidation, webhooks, REST/RPC affected, runko-ci) feeds the
// closure identical facts. §13.3.1's fields ride along: Consumes edges and
// the provider's contract surface (the rpc path dir, the OpenAPI document,
// and the manifest itself - fail closed, a manifest edit can move the
// surface).
func AffectedProjectInfos(indexed []IndexedProject) []affected.ProjectInfo {
	out := make([]affected.ProjectInfo, len(indexed))
	for i, ip := range indexed {
		info := affected.ProjectInfo{
			Name:                 ip.Name,
			Path:                 ip.Path,
			DeclaredDependencies: ip.DeclaredDependencies,
			Consumes:             ip.Consumes,
		}
		if ip.ContractDir != "" || ip.OpenAPIPath != "" {
			info.ContractPaths = append(info.ContractPaths, path.Join(ip.Path, "PROJECT.yaml"))
			if ip.ContractDir != "" {
				info.ContractPaths = append(info.ContractPaths, ip.ContractDir)
			}
			if ip.OpenAPIPath != "" {
				info.ContractPaths = append(info.ContractPaths, ip.OpenAPIPath)
			}
		}
		out[i] = info
	}
	return out
}
