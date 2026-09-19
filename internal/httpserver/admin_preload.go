package httpserver

import (
	"encoding/json"
	"html"
	"io/fs"
	"strings"
)

type viteChunk struct {
	File    string   `json:"file"`
	Imports []string `json:"imports"`
	CSS     []string `json:"css"`
}

// Vite's own manifest supplies destination-only hints in the initial HTML,
// removing the entry-JS -> route-JS discovery waterfall. No data is prefetched.
func adminRoutePreloads(root fs.FS) map[string]string {
	raw, err := fs.ReadFile(root, ".vite/manifest.json")
	if err != nil {
		return nil
	} // Older frontend bundles remain supported.
	var chunks map[string]viteChunk
	if json.Unmarshal(raw, &chunks) != nil {
		return nil
	}
	result := make(map[string]string)
	for key := range chunks {
		if !strings.HasPrefix(key, "src/views/") {
			continue
		}
		var out strings.Builder
		seen := make(map[string]bool)
		var visit func(string)
		emit := func(file, rel, as string) {
			if seen[file] || !fs.ValidPath(file) || !strings.HasPrefix(file, "assets/") {
				return
			}
			seen[file] = true
			out.WriteString(`<link rel="` + rel + `" href="./` + html.EscapeString(file) + `"` + as + ` crossorigin>`)
		}
		visit = func(name string) {
			if seen["chunk:"+name] {
				return
			}
			seen["chunk:"+name] = true
			chunk, ok := chunks[name]
			if !ok {
				return
			}
			if strings.HasSuffix(chunk.File, ".js") {
				emit(chunk.File, "modulepreload", "")
			}
			for _, css := range chunk.CSS {
				if strings.HasSuffix(css, ".css") {
					emit(css, "preload", ` as="style"`)
				}
			}
			for _, child := range chunk.Imports {
				visit(child)
			}
		}
		visit(key)
		result[strings.TrimSuffix(strings.TrimPrefix(key, "src/views/"), ".vue")] = out.String()
	}
	return result
}

func adminViewForPath(path string) string {
	path = strings.Trim(path, "/")
	switch path {
	case "":
		return "AccountsView"
	case "login":
		return "LoginView"
	case "aliases":
		return "AliasesView"
	case "pool":
		return "PoolView"
	case "audit":
		return "AuditView"
	case "logs":
		return "LogsView"
	case "security":
		return "SecurityView"
	case "accounts/new":
		return "AccountFormView"
	}
	if strings.HasPrefix(path, "accounts/") {
		parts := strings.Split(path, "/")
		if len(parts) == 2 {
			return "AccountDetailView"
		}
		if len(parts) == 3 && parts[2] == "edit" {
			return "AccountFormView"
		}
	}
	return ""
}
