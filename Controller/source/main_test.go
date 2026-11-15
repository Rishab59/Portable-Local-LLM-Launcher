package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"Models/general", "Runtime/" + runtime.GOOS + "-x64", "Logs"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestSafeModelPathRejectsTraversal(t *testing.T) {
	a := &App{root: testRoot(t)}
	for _, input := range []string{"../secret.gguf", "../../Models/x.gguf", "..\\secret.gguf", filepath.Join(string(filepath.Separator), "tmp", "x.gguf")} {
		if _, err := a.safeModelPath(input); err == nil {
			t.Errorf("safeModelPath(%q) unexpectedly accepted", input)
		}
	}
}

func TestSafeModelPathAllowsNestedRelativePath(t *testing.T) {
	root := testRoot(t)
	a := &App{root: root}
	got, err := a.safeModelPath("general/model.gguf")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "Models", "general", "model.gguf")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestModelsEndpointListsOnlyGGUF(t *testing.T) {
	root := testRoot(t)
	if err := os.WriteFile(filepath.Join(root, "Models", "general", "a.gguf"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Models", "general", "notes.txt"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	a := &App{root: root}
	r := httptest.NewRequest(http.MethodGet, "/api/models", nil)
	w := httptest.NewRecorder()
	a.models(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Models []Model `json:"models"`
		Count  int     `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Count != 1 || len(response.Models) != 1 || response.Models[0].Name != "a.gguf" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestSecurityMiddlewareRejectsForeignOriginAndHost(t *testing.T) {
	app := &App{}
	handler := app.securityMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	cases := []struct {
		name, host, origin string
		want               int
	}{
		{"good", "127.0.0.1:8787", "http://127.0.0.1:8787", http.StatusNoContent},
		{"foreign host", "192.168.1.10:8787", "", http.StatusForbidden},
		{"foreign origin", "127.0.0.1:8787", "https://evil.example", http.StatusForbidden},
		{"host prefix trick", "127.0.0.1.evil:8787", "", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/health", nil)
			r.Host = tc.host
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d", w.Code, tc.want)
			}
		})
	}
}

func TestStartRejectsInvalidBackendBeforeLaunch(t *testing.T) {
	root := testRoot(t)
	model := filepath.Join(root, "Models", "general", "model.gguf")
	if err := os.WriteFile(model, []byte("placeholder"), 0644); err != nil {
		t.Fatal(err)
	}
	a := &App{root: root, runtimeExe: filepath.Join(root, "Runtime", runtime.GOOS+"-x64", "llamafile-0.10.6"), logsDir: filepath.Join(root, "Logs"), servers: map[string]*ManagedServer{}}
	if runtime.GOOS == "windows" {
		a.runtimeExe += ".exe"
	}
	_, err := a.startServer(StartRequest{SettingsProfile: SettingsProfile{ModelPath: "general/model.gguf", GPUBackend: "bogus"}})
	if err == nil || !strings.Contains(err.Error(), "gpuBackend") {
		t.Fatalf("expected backend validation error, got %v", err)
	}
}

func TestMetadataRequiresCuratedCatalogIDForVerifiedLicense(t *testing.T) {
	root := testRoot(t)
	modelPath := filepath.Join(root, "Models", "vision", "model.gguf")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("placeholder"), 0644); err != nil {
		t.Fatal(err)
	}
	a := &App{root: root, registry: map[string]Model{}, registryPath: filepath.Join(root, "Config", "model-registry.json")}
	if err := os.MkdirAll(filepath.Dir(a.registryPath), 0755); err != nil {
		t.Fatal(err)
	}
	catalogFixture := `[ {"id":"qwen3-vl-2b-instruct-q4km","name":"Qwen3-VL","sourceUrl":"https://example.test/model","license":"Apache-2.0","licenseVerified":true,"categories":["vision"]} ]`
	if err := os.WriteFile(filepath.Join(root, "Config", "model-catalog.json"), []byte(catalogFixture), 0644); err != nil {
		t.Fatal(err)
	}
	body := `{"path":"vision/model.gguf","categories":["vision"],"license":"I claim Apache-2.0"}`
	r := httptest.NewRequest(http.MethodPost, "/api/models/metadata", strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	w := httptest.NewRecorder()
	a.updateModelMetadata(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if a.registry["vision/model.gguf"].LicenseVerified {
		t.Fatal("user-entered license must not be marked verified")
	}
	body = `{"path":"vision/model.gguf","categories":["general","vision"],"catalogId":"qwen3-vl-2b-instruct-q4km"}`
	r = httptest.NewRequest(http.MethodPost, "/api/models/metadata", strings.NewReader(body))
	r.Host = "127.0.0.1:8787"
	w = httptest.NewRecorder()
	a.updateModelMetadata(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog metadata status=%d body=%s", w.Code, w.Body.String())
	}
	m := a.registry["vision/model.gguf"]
	if !m.LicenseVerified || m.License != "Apache-2.0" || m.CatalogID == "" {
		t.Fatalf("curated catalog license not applied: %+v", m)
	}
}

func TestInferCategoriesSupportsMultipleTags(t *testing.T) {
	got := inferCategories("vision/Qwen3-VL-thinking-coder.gguf")
	for _, want := range []string{"vision", "reasoning", "coding"} {
		found := false
		for _, c := range got {
			if c == want {
				found = true
			}
		}
		if !found {
			t.Errorf("expected category %q in %v", want, got)
		}
	}
}

func TestSettingsProfilePreservesZeroTemperatureWhenProvided(t *testing.T) {
	root := testRoot(t)
	a := &App{root: root, profiles: map[string]SettingsProfile{"general/model.gguf": {ModelPath: "general/model.gguf", Port: 8080, ContextSize: 8192, GPUBackend: "auto", GPULayers: 999, Parallel: 1, FlashAttention: "auto", Temperature: 0.7, TopP: 0.8, TopK: 20, RepeatPenalty: 1, MaxTokens: 1024}}}
	p := a.profileForStart(StartRequest{SettingsProfile: SettingsProfile{ModelPath: "general/model.gguf", Port: 8081, ContextSize: 8192, GPUBackend: "auto", GPULayers: 999, Parallel: 1, FlashAttention: "auto", Temperature: 0, TopP: 0.8, TopK: 20, MinP: 0, RepeatPenalty: 1, MaxTokens: 1024}})
	if p.Temperature != 0 {
		t.Fatalf("explicit temperature 0 was overwritten: %v", p.Temperature)
	}
	if p.Port != 8081 {
		t.Fatalf("explicit port was overwritten: %d", p.Port)
	}
}

func TestSettingsValidationRejectsBadRanges(t *testing.T) {
	p := defaultProfile("general/model.gguf")
	p.ContextSize = 100
	if err := validateProfile(p); err == nil {
		t.Fatal("expected context validation error")
	}
	p = defaultProfile("general/model.gguf")
	p.GPUBackend = "gpu-fallback"
	if err := validateProfile(p); err == nil {
		t.Fatal("expected backend validation error")
	}
}

func TestCustomRuntimeArgumentsAreAllowlisted(t *testing.T) {
	args, err := parseCustomArgs("--jinja\n--reasoning-format qwen3\n--no-context-shift")
	if err != nil {
		t.Fatal(err)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 arguments, got %v", args)
	}
	for _, bad := range []string{"--port 9999", "--model /tmp/evil.gguf", "--reasoning-format unknown", "--jinja true", "--api-key secret"} {
		if _, err := parseCustomArgs(bad); err == nil {
			t.Errorf("expected %q to be rejected", bad)
		}
	}
}

func TestCustomCategorySlugValidation(t *testing.T) {
	for _, good := range []string{"translation", "topic-2", "3d"} {
		if !isCategorySlug(good) {
			t.Errorf("expected valid category %q", good)
		}
	}
	for _, bad := range []string{"", "has spaces", "../escape", "x_underscore", strings.Repeat("a", 33)} {
		if isCategorySlug(bad) {
			t.Errorf("expected invalid category %q", bad)
		}
	}
}

func TestControlPanelRootServesIndexWithoutRedirectLoop(t *testing.T) {
	root := testRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "ControlPanel"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "ControlPanel", "index.html"), []byte("test panel"), 0644); err != nil {
		t.Fatal(err)
	}
	a := &App{root: root}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "127.0.0.1:8787"
	w := httptest.NewRecorder()
	a.controlPanel().ServeHTTP(w, r)
	if w.Code != http.StatusOK || w.Body.String() != "test panel" {
		t.Fatalf("status=%d body=%q", w.Code, w.Body.String())
	}
}

func TestAppConfigPersistsServerMode(t *testing.T) {
	root := testRoot(t)
	a := &App{appConfigPath: filepath.Join(root, "Config", "app-config.json"), serverMode: "single"}
	if err := os.MkdirAll(filepath.Dir(a.appConfigPath), 0755); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/config", strings.NewReader(`{"serverMode":"concurrent"}`))
	r.Host = "127.0.0.1:8787"
	w := httptest.NewRecorder()
	a.configHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	b, err := os.ReadFile(a.appConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "concurrent") {
		t.Fatalf("saved config missing mode: %s", b)
	}
}

func TestHasCLIOptionRequiresWholeFlag(t *testing.T) {
	help := "--models-dir PATH --api-key KEY --api-key-file FILE -ngl N"
	for _, option := range []string{"--models-dir", "--api-key", "--api-key-file", "-ngl"} {
		if !hasCLIOption(help, option) {
			t.Errorf("expected to find %s", option)
		}
	}
	if hasCLIOption(help, "--model") {
		t.Fatal("--model must not match the --models-dir prefix")
	}
	if hasCLIOption(help, "--api") {
		t.Fatal("--api must not match --api-key")
	}
}

func TestServerChatProxyAddsOwnedServerAPIKey(t *testing.T) {
	const expectedKey = "test-secret-key"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("upstream method=%q", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+expectedKey {
			t.Errorf("Authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"proxy works"}}]}`))
	}))
	defer upstream.Close()
	portText := strings.TrimPrefix(upstream.URL, "http://127.0.0.1:")
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	s := &ManagedServer{ID: "srv-test", Port: port, APIKey: expectedKey, Status: "running"}
	a := &App{servers: map[string]*ManagedServer{"srv-test": s}}
	r := httptest.NewRequest(http.MethodPost, "/api/servers/srv-test/chat", strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	a.serverItem(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "proxy works") {
		t.Fatalf("unexpected proxy response: %s", w.Body.String())
	}
}
