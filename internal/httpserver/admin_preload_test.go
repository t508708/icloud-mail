package httpserver

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestAdminRoutePreloadsOnlyLocalDestinationDependencies(t *testing.T) {
	root := fstest.MapFS{".vite/manifest.json": {Data: []byte(`{
		"src/views/AccountsView.vue":{"file":"assets/accounts.js","imports":["shared"],"css":["assets/accounts.css"]},
		"src/views/PoolView.vue":{"file":"assets/pool.js"},
		"shared":{"file":"assets/shared.js","imports":["shared","outside"]},
		"outside":{"file":"https://example.test/script.js","css":["assets/../outside.css"]}
	}`)}}
	hints := adminRoutePreloads(root)
	want := hints["AccountsView"]
	for _, file := range []string{"./assets/accounts.js", "./assets/shared.js", "./assets/accounts.css"} {
		if strings.Count(want, file) != 1 {
			t.Fatalf("missing or repeated preload %s: %s", file, want)
		}
	}
	for _, unexpected := range []string{"pool.js", "https:", "outside.css"} {
		if strings.Contains(want, unexpected) {
			t.Fatalf("unexpected preload %s", unexpected)
		}
	}
	env := newAdminAPITestEnv(t)
	env.server.adminSPA = &adminSPA{index: []byte(testAdminSPAIndex), preloads: hints}
	env.server.cfg.AdminPath = "/custom/admin"
	router, err := env.server.Router()
	if err != nil {
		t.Fatal(err)
	}
	response := serveAdminRouterRequest(router, http.MethodGet, "/custom/admin/", nil, nil)
	if !strings.Contains(response.Body.String(), `<base href="/custom/admin/">`) || !strings.Contains(response.Body.String(), want) {
		t.Fatal("route preload missing from HTML")
	}
	if adminRoutePreloads(fstest.MapFS{}) != nil {
		t.Fatal("old bundle must work without manifest")
	}
}
