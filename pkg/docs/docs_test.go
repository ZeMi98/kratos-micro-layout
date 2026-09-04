package docs

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	khttp "github.com/go-kratos/kratos/v3/transport/http"
	"gopkg.in/yaml.v3"
)

// The spec is injected with a security scheme by regex surgery on generated
// YAML (see withSecurityScheme). These tests guard the fragile part: that the
// result is still valid YAML, that the scheme survives, and that the routes
// http-swagger mounts actually serve the UI, its embedded assets, and the doc.

// parseYAML fails unless body is a well-formed OpenAPI document.
func parseYAML(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(body, &m); err != nil {
		t.Fatalf("served spec is not valid YAML: %v\n----\n%s", err, body)
	}
	return m
}

func mustDig(t *testing.T, m map[string]any, path ...string) any {
	t.Helper()
	var cur any = m
	for _, k := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("path %v: %q is not a map (got %T)", path, k, cur)
		}
		if cur, ok = mm[k]; !ok {
			t.Fatalf("path %v: key %q missing", path, k)
		}
	}
	return cur
}

// TestSpecHasSecurityScheme checks the injected document: valid YAML, a bearer
// scheme under components, a top-level security requirement referencing it, and
// the generated schemas left intact (the scheme is spliced in just above them).
func TestSpecHasSecurityScheme(t *testing.T) {
	doc, err := Spec("user")
	if err != nil {
		t.Fatalf(`Spec("user"): %v`, err)
	}
	m := parseYAML(t, doc)

	scheme := mustDig(t, m, "components", "securitySchemes", schemeName).(map[string]any)
	if scheme["type"] != "http" || scheme["scheme"] != "bearer" {
		t.Errorf("%s scheme = %#v, want type:http scheme:bearer", schemeName, scheme)
	}

	sec, ok := m["security"].([]any)
	if !ok || len(sec) == 0 {
		t.Fatalf("top-level security missing or empty: %#v", m["security"])
	}
	if _, ok := sec[0].(map[string]any)[schemeName]; !ok {
		t.Errorf("security[0] = %#v, want it to reference %q", sec[0], schemeName)
	}

	if _, ok := m["components"].(map[string]any)["schemas"]; !ok {
		t.Error("components.schemas vanished after the security injection")
	}
}

// TestSpecUnknownDomain checks a domain with no embedded document fails loudly
// rather than serving an empty or merged spec — Register panics on this, so a
// typo'd domain in http.go surfaces at startup, not as a broken /swagger.
func TestSpecUnknownDomain(t *testing.T) {
	if _, err := Spec("nonexistent"); err == nil {
		t.Fatal(`Spec("nonexistent") = nil error, want a missing-spec error`)
	}
}

// TestHandleSpecRewritesServers checks the per-request servers rewrite points
// "try it out" at the origin being browsed, keeps the doc valid, drops the
// generated placeholder host, and preserves the security scheme.
func TestHandleSpecRewritesServers(t *testing.T) {
	doc, err := Spec("user")
	if err != nil {
		t.Fatalf(`Spec("user"): %v`, err)
	}
	r := httptest.NewRequest(http.MethodGet, SpecPath, nil)
	r.Host = "api.internal.example.com"
	r.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	handleSpec(w, r, doc)

	body := w.Body.Bytes()
	m := parseYAML(t, body)

	got := mustDig(t, m, "servers").([]any)[0].(map[string]any)["url"]
	if want := "https://api.internal.example.com"; got != want {
		t.Errorf("servers[0].url = %q, want %q", got, want)
	}
	if strings.Contains(string(body), "user.example.com") {
		t.Error("generated placeholder host user.example.com survived the rewrite")
	}
	mustDig(t, m, "components", "securitySchemes", schemeName) // still secured
}

// TestRegisterServesUIAndSpec drives a real kratos server through every route a
// browser hits: the /swagger redirect chain, the UI page and its config, the
// assets served from the embedded swagger-ui-dist (no CDN), and the spec.
func TestRegisterServesUIAndSpec(t *testing.T) {
	srv := khttp.NewServer()
	Register(srv, "user")

	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.RequestURI = target // http-swagger routes on RequestURI's basename
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}

	t.Run("redirect chain", func(t *testing.T) {
		if w := get(UIPath); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != UIPath+"/" {
			t.Errorf("%s -> %d loc=%q, want 301 %s", UIPath, w.Code, w.Header().Get("Location"), UIPath+"/")
		}
		if w := get(UIPath + "/"); w.Code != http.StatusMovedPermanently || w.Header().Get("Location") != UIPath+"/index.html" {
			t.Errorf("%s/ -> %d loc=%q, want 301 %s/index.html", UIPath, w.Code, w.Header().Get("Location"), UIPath)
		}
	})

	t.Run("index page", func(t *testing.T) {
		body := get(UIPath + "/index.html").Body.String()
		for _, want := range []string{
			"SwaggerUIBundle",                 // the UI boots
			strings.TrimPrefix(SpecPath, "/"), // ...against our spec (html/template escapes the leading "/" to "\/")
			"persistAuthorization:", "true",   // token survives a refresh
		} {
			if !strings.Contains(body, want) {
				t.Errorf("index.html missing %q", want)
			}
		}
	})

	t.Run("embedded assets", func(t *testing.T) {
		// Served from swaggo/files' embedded dist, so they must be 200 with the
		// right type and a real body — proving there is no CDN dependency.
		cases := map[string]string{
			UIPath + "/swagger-ui.css":       "text/css",
			UIPath + "/swagger-ui-bundle.js": "javascript",
		}
		for target, ctype := range cases {
			w := get(target)
			if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), ctype) || w.Body.Len() == 0 {
				t.Errorf("%s -> %d ctype=%q len=%d, want 200 %s with a body", target, w.Code, w.Header().Get("Content-Type"), w.Body.Len(), ctype)
			}
		}
	})

	t.Run("spec endpoint", func(t *testing.T) {
		w := get(SpecPath)
		if w.Code != http.StatusOK || !strings.Contains(w.Header().Get("Content-Type"), "yaml") {
			t.Errorf("%s -> %d ctype=%q, want 200 yaml", SpecPath, w.Code, w.Header().Get("Content-Type"))
		}
		if !strings.Contains(w.Body.String(), "securitySchemes") {
			t.Error("served spec is missing the injected securitySchemes")
		}
	})

	t.Run("non-GET rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, UIPath+"/index.html", nil)
		req.RequestURI = UIPath + "/index.html"
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST -> %d, want 405", w.Code)
		}
	})
}
