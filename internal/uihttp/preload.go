package uihttp

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"html"
	"io/fs"
	"strings"
)

type assetChunk struct {
	File    string   `json:"file"`
	Imports []string `json:"imports"`
	CSS     []string `json:"css"`
}

// Discover route dependencies from the build, rather than after executing the
// entry and route modules on a cold navigation.
func routeDocuments(assets fs.FS) (map[string][]byte, error) {
	data, err := fs.ReadFile(assets, "manifest.json")
	if err != nil {
		return nil, err
	}
	var manifest map[string]assetChunk
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		return nil, err
	}
	documents := make(map[string][]byte)
	for key, roots := range map[string][]string{
		"slis":       {"src/slis/SlisPage.tsx"},
		"slos":       {"src/slos/SlosPage.tsx"},
		"slo-detail": {"src/slos/SlosPage.tsx", "src/components/charts/TimelineChartView.tsx"},
	} {
		var links strings.Builder
		seen := make(map[string]bool)
		var visit func(string)
		visit = func(name string) {
			if seen[name] {
				return
			}
			seen[name] = true
			chunk, ok := manifest[name]
			if !ok {
				return
			}
			links.WriteString(`<link rel="modulepreload" crossorigin href="/` + html.EscapeString(chunk.File) + `">`)
			for _, css := range chunk.CSS {
				if !seen[css] {
					seen[css] = true
					links.WriteString(`<link rel="stylesheet" href="/` + html.EscapeString(css) + `">`)
				}
			}
			for _, dependency := range chunk.Imports {
				visit(dependency)
			}
		}
		for _, root := range roots {
			if _, ok := manifest[root]; !ok {
				return nil, fmt.Errorf("ui: route module absent from build manifest: %s", root)
			}
			visit(root)
		}
		documents[key] = bytes.Replace(index, []byte("<head>"), []byte("<head>"+links.String()), 1)
	}
	return documents, nil
}

func routeDocumentKey(path string) string {
	if sloDetailPath.MatchString(path) {
		return "slo-detail"
	}
	if path == "/slos" || path == "/slos/new" || serviceDetailPath.MatchString(path) {
		return "slos"
	}
	if path == "/slis" || path == "/slis/new" || sliDetailPath.MatchString(path) {
		return "slis"
	}
	return ""
}
