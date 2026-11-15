package main

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultControllerPort = 8787
	defaultServerPort     = 8080
	maxRequestBytes       = 1 << 20
)

type App struct {
	root           string
	runtimeExe     string
	runtimeVersion string
	runtimeFlags   map[string]bool
	logsDir        string
	mu             sync.Mutex
	servers        map[string]*ManagedServer
	registryMu     sync.Mutex
	registry       map[string]Model
	registryPath   string
	settingsMu     sync.Mutex
	profiles       map[string]SettingsProfile
	settingsPath   string
	appConfigPath  string
	appConfigMu    sync.Mutex
	serverMode     string
	httpServer     *http.Server
	shutdownOnce   sync.Once
}

type ManagedServer struct {
	ID          string    `json:"id"`
	ModelPath   string    `json:"modelPath"`
	ModelName   string    `json:"modelName"`
	MMProjPath  string    `json:"mmprojPath,omitempty"`
	Port        int       `json:"port"`
	ContextSize int       `json:"contextSize"`
	GPUBackend  string    `json:"gpuBackend"`
	GPULayers   int       `json:"gpuLayers"`
	Status      string    `json:"status"`
	ExitError   string    `json:"exitError,omitempty"`
	APIKey      string    `json:"apiKey"`
	PID         int       `json:"pid,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	LogPath     string    `json:"-"`
	cmd         *exec.Cmd
	logFile     *os.File
	done        chan struct{}
}

type StartRequest struct {
	SettingsProfile
	AutoPort bool `json:"autoPort,omitempty"`
}

type Model struct {
	Path                   string   `json:"path"`
	Name                   string   `json:"name"`
	SizeBytes              int64    `json:"sizeBytes"`
	Modified               string   `json:"modified"`
	Role                   string   `json:"role"`
	Categories             []string `json:"categories"`
	License                string   `json:"license,omitempty"`
	LicenseVerified        bool     `json:"licenseVerified"`
	SourceURL              string   `json:"sourceUrl,omitempty"`
	CatalogID              string   `json:"catalogId,omitempty"`
	MMProjPath             string   `json:"mmprojPath,omitempty"`
	Notes                  string   `json:"notes,omitempty"`
	Architecture           string   `json:"architecture,omitempty"`
	ArchitectureRecognized bool     `json:"architectureRecognized"`
	MetadataReadable       bool     `json:"metadataReadable"`
	Quantization           string   `json:"quantization,omitempty"`
}

type PortResult struct {
	Port      int   `json:"port"`
	Available bool  `json:"available"`
	Suggested []int `json:"suggested,omitempty"`
}

type ControllerConfig struct {
	Host                     string `json:"host"`
	Port                     int    `json:"port"`
	ReadHeaderTimeoutSeconds int    `json:"readHeaderTimeoutSeconds"`
	ReadTimeoutSeconds       int    `json:"readTimeoutSeconds"`
	WriteTimeoutSeconds      int    `json:"writeTimeoutSeconds"`
	IdleTimeoutSeconds       int    `json:"idleTimeoutSeconds"`
}

func loadControllerConfig(path string) (ControllerConfig, error) {
	c := ControllerConfig{Host: "127.0.0.1", Port: defaultControllerPort, ReadHeaderTimeoutSeconds: 5, ReadTimeoutSeconds: 15, WriteTimeoutSeconds: 30, IdleTimeoutSeconds: 60}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.Host != "127.0.0.1" && c.Host != "localhost" && c.Host != "::1" {
		return c, fmt.Errorf("host must be loopback (127.0.0.1, localhost, or ::1)")
	}
	if c.Port < 1 || c.Port > 65535 {
		return c, fmt.Errorf("port must be between 1 and 65535")
	}
	if c.ReadHeaderTimeoutSeconds < 1 || c.ReadHeaderTimeoutSeconds > 120 || c.ReadTimeoutSeconds < 1 || c.ReadTimeoutSeconds > 600 || c.WriteTimeoutSeconds < 1 || c.WriteTimeoutSeconds > 600 || c.IdleTimeoutSeconds < 1 || c.IdleTimeoutSeconds > 600 {
		return c, fmt.Errorf("HTTP timeout values must be between 1 and 600 seconds (header timeout max 120)")
	}
	return c, nil
}

func main() {
	rootFlag := ""
	controllerPort := defaultControllerPort
	portOverride := false
	controllerHost := "127.0.0.1"
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--root":
			if i+1 < len(os.Args) {
				rootFlag = os.Args[i+1]
				i++
			}
		case "--port":
			if i+1 < len(os.Args) {
				n, err := strconv.Atoi(os.Args[i+1])
				if err == nil {
					controllerPort = n
					portOverride = true
				}
				i++
			}
		case "--version":
			fmt.Println("PortableAI Controller 0.1.0")
			return
		case "--help", "-h":
			fmt.Println("PortableAI Controller 0.1.0\nUsage: portableai-controller [--root PATH] [--port PORT]\nThe controller binds only to 127.0.0.1.")
			return
		}
	}
	root, err := resolveRoot(rootFlag)
	if err != nil {
		log.Fatal(err)
	}
	// Initialize logging before runtime validation so startup failures are always recorded.
	logsDir := filepath.Join(root, "Logs")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		log.Fatalf("cannot create controller log directory %s: %v", logsDir, err)
	}
	lf, err := os.OpenFile(filepath.Join(logsDir, "controller.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		log.Fatalf("cannot open controller log: %v", err)
	}
	defer lf.Close()
	log.SetOutput(io.MultiWriter(os.Stderr, lf))
	log.Printf("PortableAI controller starting; root=%s", root)
	controllerCfg, cfgErr := loadControllerConfig(filepath.Join(root, "Config", "controller.json"))
	if cfgErr != nil {
		log.Fatalf("invalid controller config: %v", cfgErr)
	}
	if controllerHost == "127.0.0.1" {
		controllerHost = controllerCfg.Host
	}
	if !portOverride {
		controllerPort = controllerCfg.Port
	}
	app, err := newApp(root)
	if err != nil {
		log.Fatalf("controller initialization failed: %v", err)
	}
	addr := net.JoinHostPort(controllerHost, strconv.Itoa(controllerPort))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("cannot start controller on %s: %v; choose another controller port with --port", addr, err)
	}
	log.Printf("PortableAI Controller listening at http://%s (root: %s)", addr, root)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", app.health)
	mux.HandleFunc("/api/models", app.models)
	mux.HandleFunc("/api/models/refresh", app.refreshModels)
	mux.HandleFunc("/api/models/metadata", app.updateModelMetadata)
	mux.HandleFunc("/api/catalog", app.catalog)
	mux.Handle("/", app.controlPanel())
	mux.HandleFunc("/api/servers", app.serverCollection)
	mux.HandleFunc("/api/servers/", app.serverItem)
	mux.HandleFunc("/api/ports/check", app.checkPort)
	mux.HandleFunc("/api/hardware", app.hardware)
	mux.HandleFunc("/api/settings", app.settingsHandler)
	mux.HandleFunc("/api/config", app.configHandler)
	mux.HandleFunc("/api/shutdown", app.shutdownHandler)
	server := &http.Server{Handler: app.securityMiddleware(mux), ReadHeaderTimeout: time.Duration(controllerCfg.ReadHeaderTimeoutSeconds) * time.Second, ReadTimeout: time.Duration(controllerCfg.ReadTimeoutSeconds) * time.Second, WriteTimeout: time.Duration(controllerCfg.WriteTimeoutSeconds) * time.Second, IdleTimeout: time.Duration(controllerCfg.IdleTimeoutSeconds) * time.Second}
	app.httpServer = server
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Printf("shutdown signal received; stopping owned servers")
		app.stopAllServers()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	if err := server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	app.stopAllServers()
}

func hasCLIOption(helpText, option string) bool {
	for from := 0; from < len(helpText); {
		rel := strings.Index(helpText[from:], option)
		if rel < 0 {
			return false
		}
		idx := from + rel
		beforeOK := idx == 0 || strings.ContainsRune(" \t\r\n,([", rune(helpText[idx-1]))
		end := idx + len(option)
		afterOK := end == len(helpText) || strings.ContainsRune(" \t\r\n,=:[", rune(helpText[end]))
		if beforeOK && afterOK {
			return true
		}
		from = idx + len(option)
	}
	return false
}

func resolveRoot(explicit string) (string, error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return abs, validateRoot(abs)
	}
	candidates := []string{}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		candidates = append(candidates, filepath.Clean(filepath.Join(d, "..", "..")), filepath.Clean(filepath.Join(d, "..")), d)
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
		candidates = append(candidates, filepath.Dir(cwd))
	}
	for _, candidate := range candidates {
		if abs, err := filepath.Abs(candidate); err == nil && validateRoot(abs) == nil {
			return abs, nil
		}
	}
	return "", fmt.Errorf("could not locate PortableAI root (expected Models and Runtime folders); pass --root PATH")
}

func validateRoot(root string) error {
	if st, err := os.Stat(filepath.Join(root, "Models")); err != nil || !st.IsDir() {
		return fmt.Errorf("%s does not contain a Models directory", root)
	}
	if st, err := os.Stat(filepath.Join(root, "Runtime")); err != nil || !st.IsDir() {
		return fmt.Errorf("%s does not contain a Runtime directory", root)
	}
	return nil
}

func newApp(root string) (*App, error) {
	osName := runtime.GOOS
	if osName == "darwin" {
		osName = "macos"
	}
	archName := runtime.GOARCH
	if archName == "amd64" {
		archName = "x64"
	}
	platformDir := osName + "-" + archName
	exeName := "llamafile-0.10.6"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	runtimeExe := filepath.Join(root, "Runtime", platformDir, exeName)
	if _, err := os.Stat(runtimeExe); err != nil {
		return nil, fmt.Errorf("runtime not found at %s; place the matching llamafile binary there manually", runtimeExe)
	}
	versionOut, _ := exec.Command(runtimeExe, "--version").CombinedOutput()
	helpOut, helpErr := exec.Command(runtimeExe, "--help").CombinedOutput()
	if len(helpOut) == 0 {
		return nil, fmt.Errorf("could not inspect runtime help at %s: %v", runtimeExe, helpErr)
	}
	helpText := string(helpOut)
	flags := map[string]bool{}
	for _, flag := range []string{"--model", "--offline", "--server", "--host", "--port", "--ctx-size", "--gpu", "-ngl", "--api-key", "--mmproj", "--threads", "--threads-batch", "--batch-size", "--ubatch-size", "--parallel", "--flash-attn", "--no-mmap", "--mlock", "--cache-type-k", "--cache-type-v", "--image-min-tokens", "--image-max-tokens", "--jinja", "--no-context-shift", "--no-warmup", "--verbose", "--metrics", "--reasoning-format"} {
		flags[flag] = hasCLIOption(helpText, flag)
	}
	for _, required := range []string{"--model", "--offline", "--server", "--host", "--port", "--ctx-size", "--gpu", "-ngl"} {
		if !flags[required] {
			return nil, fmt.Errorf("runtime at %s does not advertise required option %s; use a compatible llamafile build", runtimeExe, required)
		}
	}
	logsDir := filepath.Join(root, "Logs")
	configDir := filepath.Join(root, "Config")
	if err := os.MkdirAll(logsDir, 0755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return nil, err
	}
	app := &App{root: root, runtimeExe: runtimeExe, runtimeVersion: strings.TrimSpace(string(versionOut)), runtimeFlags: flags, logsDir: logsDir, servers: map[string]*ManagedServer{}, registry: map[string]Model{}, registryPath: filepath.Join(configDir, "model-registry.json"), profiles: map[string]SettingsProfile{}, settingsPath: filepath.Join(configDir, "settings.json"), appConfigPath: filepath.Join(configDir, "app-config.json"), serverMode: "single"}
	if err := app.loadRegistry(); err != nil {
		return nil, err
	}
	if err := app.loadSettings(); err != nil {
		return nil, err
	}
	if err := app.loadAppConfig(); err != nil {
		return nil, err
	}
	return app, nil
}

func (a *App) securityMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		hostname, _, splitErr := net.SplitHostPort(host)
		if splitErr != nil || (hostname != "127.0.0.1" && hostname != "localhost" && hostname != "[::1]") {
			http.Error(w, "Host must be loopback", http.StatusForbidden)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u := strings.TrimSuffix(origin, "/")
			host := r.Host
			if u != "http://"+host {
				http.Error(w, "Cross-origin request blocked", http.StatusForbidden)
				return
			}
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self' http://127.0.0.1:*; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func (a *App) controlPanel() http.Handler {
	root := filepath.Join(a.root, "ControlPanel")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			writeError(w, 405, "method not allowed")
			return
		}
		if strings.HasPrefix(r.URL.Path, "/Docs/") {
			name := strings.TrimPrefix(r.URL.Path, "/Docs/")
			if name == "" || strings.Contains(name, "..") || !strings.EqualFold(filepath.Ext(name), ".md") {
				http.Error(w, "invalid documentation path", http.StatusBadRequest)
				return
			}
			docPath := filepath.Join(a.root, "Docs", filepath.Clean(filepath.FromSlash(name)))
			if _, err := os.Stat(docPath); err != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
			http.ServeFile(w, r, docPath)
			return
		}
		if r.URL.Path == "/" {
			index := filepath.Join(root, "index.html")
			if _, err := os.Stat(index); err != nil {
				http.Error(w, "ControlPanel/index.html is missing beside the controller.", http.StatusNotFound)
				return
			}
			http.ServeFile(w, r, index)
			return
		}
		cleanPath := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(r.URL.Path, "/")))
		if cleanPath == "." || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if _, err := os.Stat(filepath.Join(root, cleanPath)); err != nil {
			http.Error(w, "Control panel file is missing. Ensure ControlPanel/index.html, styles.css, and app.js are present beside the controller.", http.StatusNotFound)
			return
		}
		http.FileServer(http.Dir(root)).ServeHTTP(w, r)
	})
}

func (a *App) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "version": "0.1.0", "root": a.root, "runtime": a.runtimeExe, "runtimeVersion": a.runtimeVersion, "runtimeCapabilities": a.runtimeFlags, "platform": runtime.GOOS, "architecture": runtime.GOARCH})
}

func (a *App) models(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	items, err := a.scanModels()
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"models": items, "count": len(items), "note": "Local discovery only; no files are downloaded or modified. Projector files are marked role=projector."})
}

func (a *App) serverCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.mu.Lock()
		list := make([]*ManagedServer, 0, len(a.servers))
		for _, s := range a.servers {
			list = append(list, copyPublicServer(s))
		}
		a.mu.Unlock()
		sort.Slice(list, func(i, j int) bool { return list[i].Port < list[j].Port })
		writeJSON(w, 200, map[string]any{"servers": list})
	case http.MethodPost:
		var req StartRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, "invalid JSON request: "+err.Error())
			return
		}
		s, err := a.startServer(req)
		if err != nil {
			writeError(w, 400, err.Error())
			return
		}
		a.mu.Lock()
		public := copyPublicServer(s)
		a.mu.Unlock()
		writeJSON(w, 201, public)
	default:
		writeError(w, 405, "method not allowed")
	}
}

func (a *App) serverItem(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/servers/"), "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, 404, "server ID required")
		return
	}
	id := parts[0]
	a.mu.Lock()
	s := a.servers[id]
	var public *ManagedServer
	if s != nil {
		public = copyPublicServer(s)
	}
	a.mu.Unlock()
	if s == nil {
		writeError(w, 404, "controller does not own a server with that ID")
		return
	}
	if len(parts) == 2 && parts[1] == "logs" && r.Method == http.MethodGet {
		data, err := os.ReadFile(s.LogPath)
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	// Proxy only the two fixed local endpoints the dashboard needs. This keeps
	// browser requests same-origin and attaches the selected server's key here.
	if len(parts) == 2 && parts[1] == "chat" && r.Method == http.MethodPost {
		a.proxyServerRequest(w, r, s, "/v1/chat/completions", http.MethodPost)
		return
	}
	if len(parts) == 2 && parts[1] == "health" && r.Method == http.MethodGet {
		a.proxyServerRequest(w, r, s, "/health", http.MethodGet)
		return
	}
	if len(parts) == 2 && (parts[1] == "chat" || parts[1] == "health") {
		writeError(w, 405, "method not allowed")
		return
	}
	if len(parts) != 1 {
		writeError(w, 404, "not found")
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, 200, public)
		return
	}
	if r.Method == http.MethodDelete {
		if err := a.stopServer(id); err != nil {
			writeError(w, 500, err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"stopped": true, "id": id})
		return
	}
	writeError(w, 405, "method not allowed")
}

// proxyServerRequest forwards a tightly scoped request to a controller-owned
// loopback server and supplies that server's API key without exposing it in
// cross-origin browser requests. No user-provided host, port, or path is used.
func (a *App) proxyServerRequest(w http.ResponseWriter, r *http.Request, s *ManagedServer, path, method string) {
	var body io.Reader
	if method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 128<<20)
		data, err := io.ReadAll(r.Body)
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "chat request is too large or could not be read")
			return
		}
		if !json.Valid(data) {
			writeError(w, http.StatusBadRequest, "chat request must contain valid JSON")
			return
		}
		body = bytes.NewReader(data)
	}
	endpoint := fmt.Sprintf("http://127.0.0.1:%d%s", s.Port, path)
	upstream, err := http.NewRequestWithContext(r.Context(), method, endpoint, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not create local runtime request")
		return
	}
	if method == http.MethodPost {
		upstream.Header.Set("Content-Type", "application/json")
	}
	if s.APIKey != "" {
		upstream.Header.Set("Authorization", "Bearer "+s.APIKey)
	}
	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(upstream)
	if err != nil {
		writeError(w, http.StatusBadGateway, "local model server request failed: "+err.Error())
		return
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20+1))
	if err != nil {
		writeError(w, http.StatusBadGateway, "could not read local model server response")
		return
	}
	if len(data) > 16<<20 {
		writeError(w, http.StatusBadGateway, "local model server response exceeded the size limit")
		return
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(data)
}

func (a *App) shutdownHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, 405, "method not allowed")
		return
	}
	writeJSON(w, 202, map[string]any{"shuttingDown": true, "message": "Owned model servers are being stopped; controller will exit."})
	a.shutdownOnce.Do(func() {
		go func() {
			a.stopAllServers()
			time.Sleep(100 * time.Millisecond)
			if a.httpServer != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = a.httpServer.Shutdown(ctx)
			}
		}()
	})
}
func (a *App) stopAllServers() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.servers))
	for id := range a.servers {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		if err := a.stopServer(id); err != nil {
			log.Printf("failed to stop owned server %s: %v", id, err)
		}
	}
}

func (a *App) checkPort(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	p, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || p < 1 || p > 65535 {
		writeError(w, 400, "port must be between 1 and 65535")
		return
	}
	available := portAvailable(p)
	result := PortResult{Port: p, Available: available}
	if !available {
		result.Suggested = findFreePorts(p+1, 5)
	}
	writeJSON(w, 200, result)
}

func portAvailable(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}
func findFreePorts(start, count int) []int {
	out := []int{}
	if start < 1 {
		start = 1
	}
	for p := start; p <= 65535 && len(out) < count; p++ {
		if portAvailable(p) {
			out = append(out, p)
		}
	}
	return out
}

type GPUInfo struct {
	Name       string `json:"name"`
	Vendor     string `json:"vendor"`
	VRAMMB     int64  `json:"vramMB,omitempty"`
	UsedVRAMMB int64  `json:"usedVRAMMB,omitempty"`
	VRAMSource string `json:"vramSource,omitempty"`
}
type HardwareInfo struct {
	OS                string    `json:"os"`
	Architecture      string    `json:"architecture"`
	CPU               string    `json:"cpu"`
	LogicalProcessors int       `json:"logicalProcessors"`
	PhysicalCores     int       `json:"physicalCores,omitempty"`
	RAMBytes          uint64    `json:"ramBytes,omitempty"`
	AvailableRAMBytes uint64    `json:"availableRamBytes,omitempty"`
	GPUs              []GPUInfo `json:"gpus"`
	Notes             []string  `json:"notes"`
}

func (a *App) hardware(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, 405, "method not allowed")
		return
	}
	h := detectHardware()
	writeJSON(w, 200, h)
}

func detectHardware() HardwareInfo {
	h := HardwareInfo{OS: runtime.GOOS, Architecture: runtime.GOARCH, CPU: "Unknown CPU", LogicalProcessors: runtime.NumCPU(), GPUs: []GPUInfo{}, Notes: []string{}}
	if runtime.GOOS == "windows" {
		script := "$ErrorActionPreference='Stop'; $c=Get-CimInstance Win32_Processor | Select-Object -First 1 Name,NumberOfCores,NumberOfLogicalProcessors; $m=Get-CimInstance Win32_ComputerSystem; $o=Get-CimInstance Win32_OperatingSystem; $g=Get-CimInstance Win32_VideoController | Select-Object Name,AdapterRAM; [pscustomobject]@{cpu=$c.Name;cores=$c.NumberOfCores;logical=$c.NumberOfLogicalProcessors;ram=[uint64]$m.TotalPhysicalMemory;freeRAM=([uint64]$o.FreePhysicalMemory*1024);gpus=@($g)} | ConvertTo-Json -Compress -Depth 4"
		if out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output(); err == nil {
			var v struct {
				CPU     string `json:"cpu"`
				Cores   int    `json:"cores"`
				Logical int    `json:"logical"`
				RAM     uint64 `json:"ram"`
				FreeRAM uint64 `json:"freeRAM"`
				GPUs    []struct {
					Name       string `json:"Name"`
					AdapterRAM uint64 `json:"AdapterRAM"`
				} `json:"gpus"`
			}
			if json.Unmarshal(out, &v) == nil {
				if v.CPU != "" {
					h.CPU = v.CPU
				}
				h.PhysicalCores = v.Cores
				if v.Logical > 0 {
					h.LogicalProcessors = v.Logical
				}
				h.RAMBytes = v.RAM
				h.AvailableRAMBytes = v.FreeRAM
				for _, g := range v.GPUs {
					if strings.TrimSpace(g.Name) == "" {
						continue
					}
					vendor := gpuVendor(g.Name)
					mb := int64(g.AdapterRAM / (1024 * 1024))
					h.GPUs = append(h.GPUs, GPUInfo{Name: g.Name, Vendor: vendor, VRAMMB: mb, VRAMSource: "OS-reported estimate; may be inaccurate"})
				}
			}
		} else {
			h.Notes = append(h.Notes, "PowerShell hardware query failed; some hardware fields may be unavailable.")
		}
	} else if runtime.GOOS == "linux" {
		if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "model name") {
					if i := strings.Index(line, ":"); i >= 0 {
						h.CPU = strings.TrimSpace(line[i+1:])
						break
					}
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) > 1 {
						if kb, e := strconv.ParseUint(fields[1], 10, 64); e == nil {
							h.RAMBytes = kb * 1024
						}
					}
					break
				}
			}
		}
		if b, err := os.ReadFile("/proc/meminfo"); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(line, "MemAvailable:") {
					fields := strings.Fields(line)
					if len(fields) > 1 {
						if kb, e := strconv.ParseUint(fields[1], 10, 64); e == nil {
							h.AvailableRAMBytes = kb * 1024
						}
					}
					break
				}
			}
		}
		h.GPUs = append(h.GPUs, detectLinuxGPUs()...)
	} else if runtime.GOOS == "darwin" {
		if out, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
			h.CPU = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
			h.RAMBytes, _ = strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
		}
		h.GPUs = append(h.GPUs, detectMacGPUs()...)
	}
	if out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total,memory.used", "--format=csv,noheader,nounits").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			fields := strings.Split(line, ",")
			if len(fields) < 3 {
				continue
			}
			total, e1 := strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
			used, e2 := strconv.ParseInt(strings.TrimSpace(fields[2]), 10, 64)
			if e1 != nil || e2 != nil {
				continue
			}
			name := strings.TrimSpace(fields[0])
			replaced := false
			for i := range h.GPUs {
				if strings.EqualFold(h.GPUs[i].Name, name) && strings.EqualFold(h.GPUs[i].Vendor, "NVIDIA") {
					h.GPUs[i].Name = name
					h.GPUs[i].VRAMMB = total
					h.GPUs[i].UsedVRAMMB = used
					h.GPUs[i].VRAMSource = "nvidia-smi"
					replaced = true
					break
				}
			}
			if !replaced {
				for i := range h.GPUs {
					if strings.EqualFold(h.GPUs[i].Vendor, "NVIDIA") && h.GPUs[i].VRAMSource != "nvidia-smi" {
						h.GPUs[i].Name = name
						h.GPUs[i].VRAMMB = total
						h.GPUs[i].UsedVRAMMB = used
						h.GPUs[i].VRAMSource = "nvidia-smi"
						replaced = true
						break
					}
				}
			}
			if !replaced {
				h.GPUs = append(h.GPUs, GPUInfo{Name: name, Vendor: "NVIDIA", VRAMMB: total, UsedVRAMMB: used, VRAMSource: "nvidia-smi"})
			}
		}
	} else {
		h.Notes = append(h.Notes, "nvidia-smi unavailable; NVIDIA VRAM details may be missing.")
	}
	if len(h.GPUs) == 0 {
		h.Notes = append(h.Notes, "No GPU details were detected. Integrated/shared GPU memory and vendor-specific VRAM may not be reported accurately by the operating system.")
	}
	return h
}
func detectMacGPUs() []GPUInfo {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType", "-json").Output()
	if err != nil {
		return nil
	}
	var parsed map[string][]map[string]any
	if json.Unmarshal(out, &parsed) != nil {
		return nil
	}
	rows := parsed["SPDisplaysDataType"]
	gpus := []GPUInfo{}
	for _, row := range rows {
		name, _ := row["_name"].(string)
		if name == "" {
			name, _ = row["sppci_model"].(string)
		}
		if name == "" {
			name = "Apple/Metal GPU"
		}
		vendor := "Unknown"
		if strings.Contains(strings.ToLower(name), "apple") {
			vendor = "Apple"
		}
		if v, ok := row["spdisplays_vendor"].(string); ok {
			vendor = gpuVendor(v)
		}
		vramText, _ := row["spdisplays_vram"].(string)
		vram := parseMemoryTextMB(vramText)
		g := GPUInfo{Name: name, Vendor: vendor, VRAMMB: vram, VRAMSource: "system_profiler; Apple unified memory may not be reported as dedicated VRAM"}
		if vram == 0 {
			if shared, ok := row["spdisplays_vram_shared"].(string); ok && shared != "" {
				g.VRAMSource = "shared/unified memory reported: " + shared
			}
		}
		gpus = append(gpus, g)
	}
	return gpus
}
func parseMemoryTextMB(s string) int64 {
	fields := strings.Fields(strings.ToLower(strings.ReplaceAll(s, ",", "")))
	if len(fields) < 2 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	switch strings.TrimSpace(fields[1]) {
	case "gb", "gib":
		return int64(v * 1024)
	case "mb", "mib":
		return int64(v)
	case "kb", "kib":
		return int64(v / 1024)
	default:
		return 0
	}
}

func detectLinuxGPUs() []GPUInfo {
	paths, _ := filepath.Glob("/sys/class/drm/card[0-9]*/device/vendor")
	out := []GPUInfo{}
	for _, path := range paths {
		base := filepath.Dir(path)
		vendorBytes, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		vendorID := strings.ToLower(strings.TrimSpace(string(vendorBytes)))
		vendor := "Unknown"
		switch vendorID {
		case "0x10de":
			vendor = "NVIDIA"
		case "0x1002":
			vendor = "AMD"
		case "0x8086":
			vendor = "Intel / integrated"
		case "0x106b":
			vendor = "Apple"
		}
		deviceBytes, _ := os.ReadFile(filepath.Join(base, "device"))
		deviceID := strings.TrimSpace(string(deviceBytes))
		name := vendor + " GPU"
		if slot, e := os.ReadFile(filepath.Join(base, "uevent")); e == nil {
			for _, line := range strings.Split(string(slot), "\n") {
				if strings.HasPrefix(line, "PCI_SLOT_NAME=") {
					addr := strings.TrimPrefix(line, "PCI_SLOT_NAME=")
					if outBytes, e := exec.Command("lspci", "-s", addr).Output(); e == nil && strings.TrimSpace(string(outBytes)) != "" {
						name = strings.TrimSpace(string(outBytes))
					}
					break
				}
			}
		}
		if name == vendor+" GPU" {
			name += " (" + vendorID + ":" + deviceID + ")"
		}
		g := GPUInfo{Name: name, Vendor: vendor, VRAMSource: "Linux sysfs; may be unavailable for integrated GPUs"}
		if b, e := os.ReadFile(filepath.Join(base, "mem_info_vram_total")); e == nil {
			if n, e := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); e == nil {
				g.VRAMMB = n / (1024 * 1024)
			}
		}
		if b, e := os.ReadFile(filepath.Join(base, "mem_info_vram_used")); e == nil {
			if n, e := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64); e == nil {
				g.UsedVRAMMB = n / (1024 * 1024)
			}
		}
		if g.VRAMMB == 0 {
			g.VRAMSource = "VRAM not reported by sysfs; shared memory may be used"
		}
		out = append(out, g)
	}
	return out
}

func gpuVendor(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "nvidia") || strings.Contains(n, "geforce") || strings.Contains(n, "quadro"):
		return "NVIDIA"
	case strings.Contains(n, "amd") || strings.Contains(n, "radeon") || strings.Contains(n, "advanced micro devices"):
		return "AMD"
	case strings.Contains(n, "apple"):
		return "Apple"
	case strings.Contains(n, "intel"):
		return "Intel / integrated"
	default:
		return "Unknown"
	}
}

func (a *App) startServer(req StartRequest) (*ManagedServer, error) {
	req.SettingsProfile = a.profileForStart(req)
	if err := validateProfile(req.SettingsProfile); err != nil {
		return nil, err
	}
	modelPath, err := a.safeModelPath(req.ModelPath)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(filepath.Ext(modelPath), ".gguf") {
		return nil, errors.New("model must be a local .gguf file inside Models")
	}
	if st, err := os.Stat(modelPath); err != nil || st.IsDir() {
		return nil, errors.New("model file not found")
	}
	mmproj := ""
	if req.MMProjPath != "" {
		mmproj, err = a.safeModelPath(req.MMProjPath)
		if err != nil {
			return nil, fmt.Errorf("projector: %w", err)
		}
		if st, err := os.Stat(mmproj); err != nil || st.IsDir() || !strings.EqualFold(filepath.Ext(mmproj), ".gguf") {
			return nil, errors.New("projector must be an existing .gguf file inside Models")
		}
	}
	port := req.Port
	if port == 0 {
		port = defaultServerPort
	}
	if port < 1 || port > 65535 {
		return nil, errors.New("port must be between 1 and 65535")
	}

	ctx := req.ContextSize
	if ctx == 0 {
		ctx = 8192
	}
	if ctx < 512 || ctx > 1048576 {
		return nil, errors.New("contextSize must be between 512 and 1048576")
	}
	backend := strings.ToLower(strings.TrimSpace(req.GPUBackend))
	if backend == "" {
		backend = "auto"
	}
	if backend != "auto" && backend != "nvidia" && backend != "amd" && backend != "apple" && backend != "disable" {
		return nil, errors.New("gpuBackend must be auto, nvidia, amd, apple, or disable")
	}
	layers := req.GPULayers
	if layers == 0 {
		if backend == "disable" {
			layers = 0
		} else {
			layers = 999
		}
	}
	if layers < 0 || layers > 999 {
		return nil, errors.New("gpuLayers must be between 0 and 999")
	}
	if backend == "disable" {
		layers = 0
	}
	if !portAvailable(port) {
		if req.AutoPort {
			suggestions := findFreePorts(port+1, 1)
			if len(suggestions) == 0 {
				return nil, errors.New("requested port is occupied and no alternative was found")
			}
			port = suggestions[0]
		} else {
			return nil, fmt.Errorf("port %d is occupied; check GET /api/ports/check?port=%d or retry with autoPort=true", port, port)
		}
	}
	// Only use runtime API-key protection when the selected runtime advertises it.
	// llamafile 0.10.6 builds may not support --api-key; in that case the server
	// remains bound to loopback and the UI omits the Authorization header.
	apiKey := ""
	if a.runtimeFlags["--api-key"] {
		keyBytes := make([]byte, 32)
		if _, err := cryptorand.Read(keyBytes); err != nil {
			return nil, fmt.Errorf("could not generate server API key: %w", err)
		}
		apiKey = base64.RawURLEncoding.EncodeToString(keyBytes)
	}
	requestedFlags := []struct {
		flag string
		used bool
	}{
		{"--mmproj", mmproj != ""}, {"--threads", req.CPUThreads > 0}, {"--threads-batch", req.ThreadsBatch > 0}, {"--batch-size", req.BatchSize > 0}, {"--ubatch-size", req.UBatchSize > 0}, {"--parallel", req.Parallel > 1}, {"--flash-attn", req.FlashAttention == "on" || req.FlashAttention == "off"}, {"--no-mmap", req.NoMmap}, {"--mlock", req.Mlock}, {"--cache-type-k", req.CacheTypeK != ""}, {"--cache-type-v", req.CacheTypeV != ""}, {"--image-min-tokens", req.ImageMinTokens > 0}, {"--image-max-tokens", req.ImageMaxTokens > 0},
	}
	for _, item := range requestedFlags {
		if item.used && !a.runtimeFlags[item.flag] {
			return nil, fmt.Errorf("the selected runtime does not support %s; change this setting or use a compatible runtime", item.flag)
		}
	}
	customArgs, err := parseCustomArgs(req.CustomArgs)
	if err != nil {
		return nil, err
	}
	for _, arg := range customArgs {
		if strings.HasPrefix(arg, "--") && !a.runtimeFlags[arg] {
			return nil, fmt.Errorf("runtime does not advertise custom argument %s", arg)
		}
	}
	args := []string{"--model", modelPath, "--offline", "--server", "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--ctx-size", strconv.Itoa(ctx), "--gpu", backend, "-ngl", strconv.Itoa(layers)}
	if apiKey != "" {
		args = append(args, "--api-key", apiKey)
	}
	if req.CPUThreads > 0 {
		args = append(args, "--threads", strconv.Itoa(req.CPUThreads))
	}
	if req.ThreadsBatch > 0 {
		args = append(args, "--threads-batch", strconv.Itoa(req.ThreadsBatch))
	}
	if req.BatchSize > 0 {
		args = append(args, "--batch-size", strconv.Itoa(req.BatchSize))
	}
	if req.UBatchSize > 0 {
		args = append(args, "--ubatch-size", strconv.Itoa(req.UBatchSize))
	}
	if req.Parallel > 1 {
		args = append(args, "--parallel", strconv.Itoa(req.Parallel))
	}
	if req.FlashAttention == "on" || req.FlashAttention == "off" {
		args = append(args, "--flash-attn", req.FlashAttention)
	}
	if req.NoMmap {
		args = append(args, "--no-mmap")
	}
	if req.Mlock {
		args = append(args, "--mlock")
	}
	if req.CacheTypeK != "" {
		args = append(args, "--cache-type-k", req.CacheTypeK)
	}
	if req.CacheTypeV != "" {
		args = append(args, "--cache-type-v", req.CacheTypeV)
	}
	if req.ImageMinTokens > 0 {
		args = append(args, "--image-min-tokens", strconv.Itoa(req.ImageMinTokens))
	}
	if req.ImageMaxTokens > 0 {
		args = append(args, "--image-max-tokens", strconv.Itoa(req.ImageMaxTokens))
	}
	args = append(args, customArgs...)
	if mmproj != "" {
		args = append(args, "--mmproj", mmproj)
	}
	id := fmt.Sprintf("srv-%d-%d", time.Now().UnixNano(), port)
	logPath := filepath.Join(a.logsDir, id+".log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(a.runtimeExe, args...)
	cmd.Dir = filepath.Dir(a.runtimeExe)
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		_ = lf.Close()
		return nil, fmt.Errorf("failed to launch runtime: %w", err)
	}
	s := &ManagedServer{ID: id, ModelPath: filepath.ToSlash(req.ModelPath), ModelName: filepath.Base(modelPath), MMProjPath: filepath.ToSlash(req.MMProjPath), Port: port, ContextSize: ctx, GPUBackend: backend, GPULayers: layers, Status: "starting", APIKey: apiKey, PID: cmd.Process.Pid, StartedAt: time.Now(), LogPath: logPath, cmd: cmd, logFile: lf, done: make(chan struct{})}
	a.mu.Lock()
	a.servers[id] = s
	a.mu.Unlock()
	go func() {
		err := cmd.Wait()
		a.mu.Lock()
		if err != nil {
			s.ExitError = err.Error()
		}
		if s.Status != "stopped" {
			s.Status = "exited"
		}
		_ = s.logFile.Close()
		close(s.done)
		a.mu.Unlock()
	}()
	// A short readiness poll catches immediate runtime errors and bind failures without claiming that model loading is complete.
	go a.monitorStartup(s)
	return s, nil
}

func (a *App) safeModelPath(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("modelPath is required")
	}
	normalized := strings.ReplaceAll(strings.TrimSpace(rel), "\\", "/")
	if strings.HasPrefix(normalized, "/") || (len(normalized) >= 2 && normalized[1] == ':') {
		return "", errors.New("model path must be relative to Models and cannot escape that directory")
	}
	clean := filepath.Clean(filepath.FromSlash(normalized))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("model path must be relative to Models and cannot escape that directory")
	}
	base, err := filepath.Abs(filepath.Join(a.root, "Models"))
	if err != nil {
		return "", err
	}
	full, err := filepath.Abs(filepath.Join(base, clean))
	if err != nil {
		return "", err
	}
	relCheck, err := filepath.Rel(base, full)
	if err != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
		return "", errors.New("model path escapes Models directory")
	}
	// If the target already exists, resolve symlinks too; lexical checks alone are insufficient.
	if _, statErr := os.Lstat(full); statErr == nil {
		resolvedBase, baseErr := filepath.EvalSymlinks(base)
		resolvedFull, fullErr := filepath.EvalSymlinks(full)
		if baseErr != nil || fullErr != nil {
			return "", errors.New("could not safely resolve model path")
		}
		resolvedRel, relErr := filepath.Rel(resolvedBase, resolvedFull)
		if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", errors.New("model path resolves outside Models directory")
		}
	}
	return full, nil
}

func (a *App) monitorStartup(s *ManagedServer) {
	deadline := time.Now().Add(120 * time.Second)
	client := &http.Client{Timeout: 1200 * time.Millisecond}
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			return
		default:
		}
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/health", s.Port), nil)
		if err == nil {
			if s.APIKey != "" {
				req.Header.Set("Authorization", "Bearer "+s.APIKey)
			}
			resp, e := client.Do(req)
			if e == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					a.mu.Lock()
					if s.Status == "starting" {
						s.Status = "running"
					}
					a.mu.Unlock()
					return
				}
			}
		}
		time.Sleep(350 * time.Millisecond)
	}
	a.mu.Lock()
	if s.Status == "starting" {
		s.Status = "unresponsive"
		s.ExitError = "server did not pass its authenticated health check within 120 seconds; inspect the server log"
	}
	a.mu.Unlock()
}

func (a *App) stopServer(id string) error {
	a.mu.Lock()
	s := a.servers[id]
	if s == nil {
		a.mu.Unlock()
		return errors.New("controller does not own this server")
	}
	if s.Status == "stopped" || s.Status == "exited" {
		a.mu.Unlock()
		return nil
	}
	s.Status = "stopping"
	proc := s.cmd.Process
	a.mu.Unlock()
	// Kill only the process object created and retained by this controller; never target a port owner by PID lookup.
	if proc != nil {
		if err := proc.Kill(); err != nil && !strings.Contains(strings.ToLower(err.Error()), "process already finished") {
			select {
			case <-s.done:
			default:
				return err
			}
		}
	}
	select {
	case <-s.done:
	case <-time.After(5 * time.Second):
		return errors.New("timed out waiting for owned server process to exit")
	}
	a.mu.Lock()
	s.Status = "stopped"
	a.mu.Unlock()
	return nil
}

func copyPublicServer(s *ManagedServer) *ManagedServer {
	cp := *s
	cp.cmd = nil
	cp.logFile = nil
	cp.done = nil
	cp.LogPath = ""
	return &cp
}
