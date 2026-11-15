package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type SettingsProfile struct {
	ModelPath      string  `json:"modelPath"`
	MMProjPath     string  `json:"mmprojPath,omitempty"`
	Port           int     `json:"port"`
	ContextSize    int     `json:"contextSize"`
	GPUBackend     string  `json:"gpuBackend"`
	GPULayers      int     `json:"gpuLayers"`
	CPUThreads     int     `json:"cpuThreads"`
	ThreadsBatch   int     `json:"threadsBatch"`
	BatchSize      int     `json:"batchSize"`
	UBatchSize     int     `json:"ubatchSize"`
	Parallel       int     `json:"parallel"`
	FlashAttention string  `json:"flashAttention"`
	NoMmap         bool    `json:"noMmap"`
	Mlock          bool    `json:"mlock"`
	CacheTypeK     string  `json:"cacheTypeK"`
	CacheTypeV     string  `json:"cacheTypeV"`
	ImageMinTokens int     `json:"imageMinTokens"`
	ImageMaxTokens int     `json:"imageMaxTokens"`
	CustomArgs     string  `json:"customArgs"`
	Temperature    float64 `json:"temperature"`
	TopP           float64 `json:"topP"`
	TopK           int     `json:"topK"`
	MinP           float64 `json:"minP"`
	RepeatPenalty  float64 `json:"repeatPenalty"`
	MaxTokens      int     `json:"maxTokens"`
}

type SettingsRequest struct {
	Profile SettingsProfile `json:"profile"`
}

func defaultProfile(modelPath string) SettingsProfile {
	return SettingsProfile{ModelPath: modelPath, Port: defaultServerPort, ContextSize: 8192, GPUBackend: "auto", GPULayers: 999, CPUThreads: 0, ThreadsBatch: 0, BatchSize: 0, UBatchSize: 0, Parallel: 1, FlashAttention: "auto", Temperature: 0.7, TopP: 0.8, TopK: 20, MinP: 0, RepeatPenalty: 1.0, MaxTokens: 1024}
}

func (a *App) loadSettings() error {
	data, err := os.ReadFile(a.settingsPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var profiles map[string]SettingsProfile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return fmt.Errorf("settings file is invalid (%s): %w", a.settingsPath, err)
	}
	for path, p := range profiles {
		path = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(path, "\\", "/"))))
		if _, err := a.safeModelPath(path); err != nil {
			continue
		}
		p.ModelPath = path
		a.profiles[path] = p
	}
	return nil
}

func (a *App) saveSettingsLocked() error {
	data, err := json.MarshalIndent(a.profiles, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.settingsPath), 0755); err != nil {
		return err
	}
	tmp := a.settingsPath + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0644); err != nil {
		return err
	}
	if err := os.Rename(tmp, a.settingsPath); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (a *App) settingsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		path := filepath.ToSlash(filepath.Clean(strings.ReplaceAll(strings.TrimSpace(r.URL.Query().Get("modelPath")), "\\", "/")))
		if path == "." {
			path = ""
		}
		a.settingsMu.Lock()
		p, ok := a.profiles[path]
		a.settingsMu.Unlock()
		if !ok {
			p = defaultProfile(path)
		}
		writeJSON(w, 200, map[string]any{"profile": p, "saved": ok, "offline": true, "note": "Offline mode is enforced by the controller and cannot be disabled."})
	case http.MethodPost:
		var req SettingsRequest
		if err := decodeJSON(w, r, &req); err != nil {
			writeError(w, 400, "invalid JSON request: "+err.Error())
			return
		}
		p := req.Profile
		p.ModelPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(p.ModelPath), "\\", "/"))))
		p.MMProjPath = filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.ReplaceAll(strings.TrimSpace(p.MMProjPath), "\\", "/"))))
		if p.MMProjPath == "." {
			p.MMProjPath = ""
		}
		if _, err := a.safeModelPath(p.ModelPath); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		if err := validateProfile(p); err != nil {
			writeError(w, 400, err.Error())
			return
		}
		a.settingsMu.Lock()
		a.profiles[p.ModelPath] = p
		err := a.saveSettingsLocked()
		a.settingsMu.Unlock()
		if err != nil {
			writeError(w, 500, "could not save settings: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"saved": true, "profile": p, "offline": true})
	default:
		writeError(w, 405, "method not allowed")
	}
}

func validateProfile(p SettingsProfile) error {
	if p.ModelPath == "" {
		return errors.New("modelPath is required")
	}
	if p.Port < 1 || p.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if p.ContextSize < 512 || p.ContextSize > 1048576 {
		return errors.New("contextSize must be between 512 and 1048576")
	}
	p.GPUBackend = strings.ToLower(strings.TrimSpace(p.GPUBackend))
	if p.GPUBackend != "auto" && p.GPUBackend != "nvidia" && p.GPUBackend != "amd" && p.GPUBackend != "apple" && p.GPUBackend != "disable" {
		return errors.New("gpuBackend must be auto, nvidia, amd, apple, or disable")
	}
	if p.GPULayers < 0 || p.GPULayers > 999 {
		return errors.New("gpuLayers must be between 0 and 999")
	}
	if p.CPUThreads < 0 || p.CPUThreads > 512 || p.ThreadsBatch < 0 || p.ThreadsBatch > 512 {
		return errors.New("CPU thread counts must be between 0 and 512 (0 lets the runtime choose)")
	}
	if p.BatchSize < 0 || p.BatchSize > 8192 || p.UBatchSize < 0 || p.UBatchSize > 8192 {
		return errors.New("batch sizes must be between 0 and 8192 (0 uses runtime defaults)")
	}
	if p.Parallel < 1 || p.Parallel > 128 {
		return errors.New("parallel requests must be between 1 and 128")
	}
	if p.FlashAttention != "auto" && p.FlashAttention != "on" && p.FlashAttention != "off" {
		return errors.New("flashAttention must be auto, on, or off")
	}
	for name, value := range map[string]string{"cacheTypeK": p.CacheTypeK, "cacheTypeV": p.CacheTypeV} {
		if value != "" && value != "f16" && value != "q8_0" && value != "q5_0" && value != "q5_1" && value != "q4_0" && value != "q4_1" {
			return fmt.Errorf("%s must be empty, f16, q8_0, q5_0, q5_1, q4_0, or q4_1", name)
		}
	}
	if p.ImageMinTokens < 0 || p.ImageMinTokens > 4096 || p.ImageMaxTokens < 0 || p.ImageMaxTokens > 16384 {
		return errors.New("image token limits must be 0 (runtime default), min 1–4096, max 1–16384")
	}
	if p.ImageMinTokens > 0 && p.ImageMaxTokens > 0 && p.ImageMinTokens > p.ImageMaxTokens {
		return errors.New("imageMinTokens cannot exceed imageMaxTokens")
	}
	if _, err := parseCustomArgs(p.CustomArgs); err != nil {
		return err
	}
	if p.Temperature < 0 || p.Temperature > 2 {
		return errors.New("temperature must be between 0 and 2")
	}
	if p.TopP < 0 || p.TopP > 1 {
		return errors.New("topP must be between 0 and 1")
	}
	if p.TopK < 0 || p.TopK > 200 {
		return errors.New("topK must be between 0 and 200")
	}
	if p.MinP < 0 || p.MinP > 1 {
		return errors.New("minP must be between 0 and 1")
	}
	if p.RepeatPenalty < 0.5 || p.RepeatPenalty > 2 {
		return errors.New("repeatPenalty must be between 0.5 and 2")
	}
	if p.MaxTokens < 1 || p.MaxTokens > 32768 {
		return errors.New("maxTokens must be between 1 and 32768")
	}
	return nil
}

func (a *App) profileForStart(req StartRequest) SettingsProfile {
	p := req.SettingsProfile
	a.settingsMu.Lock()
	saved, ok := a.profiles[p.ModelPath]
	a.settingsMu.Unlock()
	provided := p.Port != 0 || p.ContextSize != 0 || p.GPUBackend != "" || p.GPULayers != 0 || p.MMProjPath != "" || p.CPUThreads != 0 || p.ThreadsBatch != 0 || p.BatchSize != 0 || p.UBatchSize != 0 || p.Parallel != 0 || p.FlashAttention != "" || p.NoMmap || p.Mlock || p.CacheTypeK != "" || p.CacheTypeV != "" || p.ImageMinTokens != 0 || p.ImageMaxTokens != 0 || p.CustomArgs != "" || p.Temperature != 0 || p.TopP != 0 || p.TopK != 0 || p.MinP != 0 || p.RepeatPenalty != 0 || p.MaxTokens != 0
	if ok && !provided {
		return saved
	}
	defaults := defaultProfile(p.ModelPath)
	if p.Port == 0 {
		p.Port = defaults.Port
	}
	if p.ContextSize == 0 {
		p.ContextSize = defaults.ContextSize
	}
	if p.GPUBackend == "" {
		p.GPUBackend = defaults.GPUBackend
	}
	if p.GPULayers == 0 && p.GPUBackend != "disable" {
		p.GPULayers = defaults.GPULayers
	}
	if p.Parallel == 0 {
		p.Parallel = defaults.Parallel
	}
	if p.FlashAttention == "" {
		p.FlashAttention = defaults.FlashAttention
	}
	if p.RepeatPenalty == 0 {
		p.RepeatPenalty = defaults.RepeatPenalty
	}
	if p.MaxTokens == 0 {
		p.MaxTokens = defaults.MaxTokens
	}
	// Sampling values of zero (temperature, top-p, top-k, min-p) are valid and preserved.
	return p
}

func parseCustomArgs(raw string) ([]string, error) {
	args := []string{}
	seen := map[string]bool{}
	boolFlags := map[string]bool{"--jinja": true, "--no-context-shift": true, "--no-warmup": true, "--verbose": true, "--metrics": true}
	valueFlags := map[string]map[string]bool{"--reasoning-format": {"auto": true, "none": true, "deepseek": true, "deepseek-legacy": true, "qwen3": true, "granite": true, "exaone": true, "phi4": true}}
	lines := strings.Split(raw, "\n")
	if len(lines) > 16 {
		return nil, errors.New("custom arguments are limited to 16 lines")
	}
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		flag := parts[0]
		if seen[flag] {
			return nil, fmt.Errorf("custom argument %s is duplicated", flag)
		}
		seen[flag] = true
		if boolFlags[flag] {
			if len(parts) != 1 {
				return nil, fmt.Errorf("%s does not take a value", flag)
			}
			args = append(args, flag)
			continue
		}
		if allowed, ok := valueFlags[flag]; ok {
			if len(parts) != 2 || !allowed[parts[1]] {
				return nil, fmt.Errorf("%s requires one supported value", flag)
			}
			args = append(args, flag, parts[1])
			continue
		}
		return nil, fmt.Errorf("custom argument %s is not on the safe allowlist; model, network, port, API-key, GPU, and file-path flags cannot be overridden", flag)
	}
	return args, nil
}

type AppConfig struct {
	ServerMode string `json:"serverMode"`
}

func (a *App) loadAppConfig() error {
	data, err := os.ReadFile(a.appConfigPath)
	if os.IsNotExist(err) {
		a.serverMode = "single"
		return nil
	}
	if err != nil {
		return err
	}
	var c AppConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("app config is invalid: %w", err)
	}
	if c.ServerMode != "single" && c.ServerMode != "concurrent" {
		return fmt.Errorf("app config serverMode must be single or concurrent")
	}
	a.serverMode = c.ServerMode
	return nil
}
func (a *App) configHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.appConfigMu.Lock()
		mode := a.serverMode
		a.appConfigMu.Unlock()
		writeJSON(w, 200, AppConfig{ServerMode: mode})
	case http.MethodPost:
		var c AppConfig
		if err := decodeJSON(w, r, &c); err != nil {
			writeError(w, 400, "invalid JSON request: "+err.Error())
			return
		}
		if c.ServerMode != "single" && c.ServerMode != "concurrent" {
			writeError(w, 400, "serverMode must be single or concurrent")
			return
		}
		data, err := json.MarshalIndent(c, "", "  ")
		if err != nil {
			writeError(w, 500, err.Error())
			return
		}
		a.appConfigMu.Lock()
		a.serverMode = c.ServerMode
		err = os.MkdirAll(filepath.Dir(a.appConfigPath), 0755)
		if err == nil {
			tmp := a.appConfigPath + ".tmp"
			err = os.WriteFile(tmp, append(data, '\n'), 0644)
			if err == nil {
				err = os.Rename(tmp, a.appConfigPath)
				if err != nil {
					_ = os.Remove(tmp)
				}
			}
		}
		a.appConfigMu.Unlock()
		if err != nil {
			writeError(w, 500, "could not save app config: "+err.Error())
			return
		}
		writeJSON(w, 200, map[string]any{"saved": true, "config": c})
	default:
		writeError(w, 405, "method not allowed")
	}
}
