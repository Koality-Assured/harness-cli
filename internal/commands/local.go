package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/spf13/cobra"
)

var (
	localApprove     bool
	localProfileOnly bool
)

var localCmd = &cobra.Command{
	Use:          "local [status|onboarding]",
	Short:        "Inspect local model runtime status and hardware onboarding",
	SilenceUsage: true,
	RunE:         runLocalStatus,
}

var localStatusCmd = &cobra.Command{
	Use:          "status",
	Short:        "Check local model runtime health and list loaded models",
	SilenceUsage: true,
	RunE:         runLocalStatus,
}

var localOnboardingCmd = &cobra.Command{
	Use:          "onboarding",
	Short:        "Inspect local hardware profile and model activation steps",
	SilenceUsage: true,
	RunE:         runLocalOnboarding,
}

func init() {
	localOnboardingCmd.Flags().BoolVar(&localApprove, "approve", false, "Acknowledge and approve local model runtime activation")
	localOnboardingCmd.Flags().BoolVar(&localProfileOnly, "profile-only", false, "Inspect hardware capabilities without activation instructions")

	localCmd.AddCommand(localStatusCmd)
	localCmd.AddCommand(localOnboardingCmd)
}

func resolveLocalBaseURL() (string, error) {
	baseURL := strings.TrimSpace(os.Getenv("HARNESS_LOCAL_BASE_URL"))
	if baseURL == "" {
		baseURL = "http://127.0.0.1:11434/v1"
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return baseURL, fmt.Errorf("expected a loopback HTTP(S) origin (127.0.0.1, localhost, or ::1), got '%s'", baseURL)
	}
	hostname := parsed.Hostname()
	if hostname != "127.0.0.1" && hostname != "localhost" && hostname != "::1" {
		return baseURL, fmt.Errorf("expected a loopback HTTP(S) origin (127.0.0.1, localhost, or ::1), got '%s'", baseURL)
	}
	return strings.TrimRight(baseURL, "/"), nil
}

func probeLocalModels(modelsURL string, timeout time.Duration) (bool, []string, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(http.MethodGet, modelsURL, nil)
	if err != nil {
		return false, nil, err
	}
	req.Header.Set("User-Agent", "ai-router-harness")
	resp, err := client.Do(req)
	if err != nil {
		return false, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return false, nil, err
	}
	var names []string
	rawList, _ := data["data"].([]any)
	if len(rawList) == 0 {
		rawList, _ = data["models"].([]any)
	}
	for _, item := range rawList {
		if m, ok := item.(map[string]any); ok {
			if id, ok := m["id"].(string); ok && id != "" {
				names = append(names, id)
			} else if name, ok := m["name"].(string); ok && name != "" {
				names = append(names, name)
			}
		}
	}
	return true, names, nil
}

func runLocalStatus(cmd *cobra.Command, _ []string) error {
	baseURL, err := resolveLocalBaseURL()
	if err != nil {
		if JSONOutput {
			_ = writeJSON(cmd.OutOrStdout(), map[string]any{
				"status":   "invalid_config",
				"base_url": baseURL,
				"error":    err.Error(),
			})
			return exitCodeError(1)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "=== Local Model Runtime Status ===")
		fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", baseURL)
		fmt.Fprintf(cmd.OutOrStdout(), "Status:   INVALID CONFIG (%v)\n", err)
		return exitCodeError(1)
	}

	modelsURL := baseURL + "/models"
	online, models, probeErr := probeLocalModels(modelsURL, 3*time.Second)
	if !online {
		if JSONOutput {
			_ = writeJSON(cmd.OutOrStdout(), map[string]any{
				"status":   "offline",
				"base_url": baseURL,
				"error":    probeErr.Error(),
			})
			return exitCodeError(1)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "=== Local Model Runtime Status ===")
		fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", baseURL)
		fmt.Fprintf(cmd.OutOrStdout(), "Status:   OFFLINE / UNREACHABLE (%v)\n", probeErr)
		fmt.Fprintln(cmd.OutOrStdout(), "\nTo start Ollama, run: ollama serve")
		return exitCodeError(1)
	}

	if JSONOutput {
		if models == nil {
			models = []string{}
		}
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"status":   "online",
			"base_url": baseURL,
			"models":   models,
		})
	}

	fmt.Fprintln(cmd.OutOrStdout(), "=== Local Model Runtime Status ===")
	fmt.Fprintf(cmd.OutOrStdout(), "Endpoint: %s\n", baseURL)
	fmt.Fprintln(cmd.OutOrStdout(), "Status:   ONLINE")
	fmt.Fprintf(cmd.OutOrStdout(), "Models (%d):\n", len(models))
	if len(models) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  (none loaded)")
	}
	for _, m := range models {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %s\n", m)
	}
	return nil
}

type HardwareProfile struct {
	OS                string              `json:"os"`
	CPUCores          int                 `json:"cpu_cores"`
	SystemRAMGB       *float64            `json:"system_ram_gb"`
	GPUName           *string             `json:"gpu_name"`
	VRAMGB            *float64            `json:"vram_gb"`
	Tier              string              `json:"tier"`
	TierDescription   string              `json:"tier_description"`
	RecommendedModels []map[string]string `json:"recommended_models"`
	SystemOneModels   []map[string]string `json:"system_one_models"`
}

func probeHardware() HardwareProfile {
	osDesc := fmt.Sprintf("%s (%s)", runtime.GOOS, runtime.GOARCH)
	cpuCores := runtime.NumCPU()
	ramGB := probeSystemRAM()
	gpuName, vramGB := probeNvidiaGPU()

	var tier, tierDesc string
	var recommendedModels []map[string]string

	if vramGB != nil && *vramGB >= 14 {
		tier = "Tier A"
		tierDesc = "High-Performance GPU Workstation (16 GB+ VRAM)"
		recommendedModels = []map[string]string{
			{"name": "qwen2.5-coder:14b", "role": "Primary coding workhorse (100% in VRAM)"},
			{"name": "qwen2.5-coder:7b", "role": "High-velocity subagent deferral"},
			{"name": "deepseek-r1:14b", "role": "Local reasoning specialist"},
			{"name": "nomic-embed-text", "role": "Local embeddings"},
		}
	} else if vramGB != nil && *vramGB >= 10 {
		tier = "Tier B"
		tierDesc = "Standard GPU Workstation (10-12 GB VRAM)"
		recommendedModels = []map[string]string{
			{"name": "qwen2.5-coder:14b", "role": "Primary coding workhorse (bounded context 8k-16k)"},
			{"name": "qwen2.5-coder:7b", "role": "High-velocity worker (100% in VRAM)"},
			{"name": "nomic-embed-text", "role": "Local embeddings"},
		}
	} else if vramGB != nil && *vramGB >= 6 {
		tier = "Tier C"
		tierDesc = "Entry / Mobile GPU Workstation (6-8 GB VRAM)"
		recommendedModels = []map[string]string{
			{"name": "qwen2.5-coder:7b", "role": "Primary coding workhorse (100% in VRAM)"},
			{"name": "qwen2.5-coder:3b", "role": "Lightweight subagent worker"},
			{"name": "nomic-embed-text", "role": "Local embeddings"},
		}
	} else if runtime.GOOS == "darwin" && (runtime.GOARCH == "arm64" || runtime.GOARCH == "arm") {
		tier = "Tier D"
		tierDesc = "Unified Memory / Apple Silicon Workstation"
		recommendedModels = []map[string]string{
			{"name": "qwen2.5-coder:14b", "role": "Primary coding workhorse (Metal accelerated)"},
			{"name": "qwen2.5-coder:7b", "role": "High-velocity subagent worker"},
			{"name": "deepseek-r1:14b", "role": "Local reasoning specialist"},
		}
	} else {
		tier = "Tier E"
		tierDesc = "CPU-Only / Vector Acceleration Workstation"
		recommendedModels = []map[string]string{
			{"name": "qwen2.5-coder:1.5b", "role": "High-speed CPU coding model"},
			{"name": "qwen2.5-coder:7b", "role": "Standard CPU coding model (AVX2/AVX-512)"},
		}
	}

	systemOneModels := []map[string]string{
		{"name": "laya-modernbert-large", "role": "System 1 non-autoregressive decision engine (421M, ~15-35ms)"},
		{"name": "smollm2:360m", "role": "FSM grammar-constrained fast triage SLM (360M, ~30-50ms)"},
	}

	return HardwareProfile{
		OS:                osDesc,
		CPUCores:          cpuCores,
		SystemRAMGB:       ramGB,
		GPUName:           gpuName,
		VRAMGB:            vramGB,
		Tier:              tier,
		TierDescription:   tierDesc,
		RecommendedModels: recommendedModels,
		SystemOneModels:   systemOneModels,
	}
}

func probeNvidiaGPU() (*string, *float64) {
	nvsmi, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, nvsmi, "--query-gpu=name,memory.total", "--format=csv,noheader,nounits")
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, nil
	}
	parts := strings.Split(lines[0], ",")
	if len(parts) < 2 {
		return nil, nil
	}
	name := strings.TrimSpace(parts[0])
	mbStr := strings.TrimSpace(parts[1])
	mb, err := strconv.ParseFloat(mbStr, 64)
	if err != nil {
		return &name, nil
	}
	vramGB := math.Round((mb/1024.0)*10) / 10
	return &name, &vramGB
}

func probeSystemRAM() *float64 {
	if runtime.GOOS == "windows" {
		kernel32 := syscall.NewLazyDLL("kernel32.dll")
		proc := kernel32.NewProc("GlobalMemoryStatusEx")
		if proc != nil && proc.Find() == nil {
			type memorystatusex struct {
				length                uint32
				memoryLoad            uint32
				totalPhys             uint64
				availPhys             uint64
				totalPageFile         uint64
				availPageFile         uint64
				totalVirtual          uint64
				availVirtual          uint64
				availExtendedVirtual  uint64
			}
			var mem memorystatusex
			mem.length = uint32(unsafe.Sizeof(mem))
			ret, _, _ := proc.Call(uintptr(unsafe.Pointer(&mem)))
			if ret != 0 && mem.totalPhys > 0 {
				gb := math.Round((float64(mem.totalPhys)/(1024*1024*1024))*10) / 10
				return &gb
			}
		}
	} else if runtime.GOOS == "linux" {
		data, err := os.ReadFile("/proc/meminfo")
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "MemTotal:") {
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						if kb, err := strconv.ParseFloat(fields[1], 64); err == nil {
							gb := math.Round((kb/(1024*1024))*10) / 10
							return &gb
						}
					}
				}
			}
		}
	}
	return nil
}

func runLocalOnboarding(cmd *cobra.Command, _ []string) error {
	baseURL, err := resolveLocalBaseURL()
	if err != nil {
		baseURL = "http://127.0.0.1:11434/v1"
	}
	hw := probeHardware()
	modelsURL := baseURL + "/models"
	endpointOnline, loadedModels, _ := probeLocalModels(modelsURL, 2*time.Second)
	if loadedModels == nil {
		loadedModels = []string{}
	}

	if JSONOutput {
		return writeJSON(cmd.OutOrStdout(), map[string]any{
			"action":          "onboarding",
			"human_approved":  localApprove,
			"profile_only":    localProfileOnly,
			"hardware":        hw,
			"endpoint":        baseURL,
			"endpoint_online": endpointOnline,
			"loaded_models":   loadedModels,
			"satellite_repo":  "https://github.com/Koality-Assured/local-ai-management",
		})
	}

	fmt.Fprintln(cmd.OutOrStdout(), "=== Local AI Hardware Profile & Capability Probe ===")
	fmt.Fprintf(cmd.OutOrStdout(), "OS:               %s\n", hw.OS)
	fmt.Fprintf(cmd.OutOrStdout(), "CPU Cores:        %d\n", hw.CPUCores)
	if hw.SystemRAMGB != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "System RAM:       %.1f GB\n", *hw.SystemRAMGB)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "System RAM:       unknown")
	}
	if hw.GPUName != nil && hw.VRAMGB != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "GPU:              %s (%.1f GB VRAM)\n", *hw.GPUName, *hw.VRAMGB)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "GPU:              None / CPU-only inference")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Hardware Profile: %s - %s\n", hw.Tier, hw.TierDescription)
	fmt.Fprintln(cmd.OutOrStdout(), "\nRecommended Models:")
	for _, m := range hw.RecommendedModels {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %-18s (%s)\n", m["name"], m["role"])
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\nSystem 1 Decision Models (Sub-50ms Routing/Triage):")
	for _, m := range hw.SystemOneModels {
		fmt.Fprintf(cmd.OutOrStdout(), "  - %-22s (%s)\n", m["name"], m["role"])
	}

	statusStr := "OFFLINE"
	if endpointOnline {
		statusStr = "ONLINE"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\nLocal Endpoint:   %s (%s)\n", baseURL, statusStr)
	if len(loadedModels) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Loaded Models (%d):\n", len(loadedModels))
		for _, lm := range loadedModels {
			fmt.Fprintf(cmd.OutOrStdout(), "  * %s\n", lm)
		}
	}

	if localProfileOnly {
		fmt.Fprintln(cmd.OutOrStdout(), "\nProfile-only mode requested. No changes performed.")
		return nil
	}

	if !localApprove {
		fmt.Fprintln(cmd.OutOrStdout(), "\n=======================================================")
		fmt.Fprintln(cmd.OutOrStdout(), "[HUMAN APPROVAL REQUIRED]")
		fmt.Fprintln(cmd.OutOrStdout(), "Local AI model execution is completely optional and requires")
		fmt.Fprintln(cmd.OutOrStdout(), "explicit human approval in this specific environment before")
		fmt.Fprintln(cmd.OutOrStdout(), "installing runtimes or downloading gigabytes of model weights.")
		fmt.Fprintln(cmd.OutOrStdout(), "=======================================================")
		fmt.Fprintln(cmd.OutOrStdout(), "\nTo approve and view activation steps:")
		fmt.Fprintln(cmd.OutOrStdout(), "  harness local onboarding --approve")
		fmt.Fprintln(cmd.OutOrStdout(), "\nSatellite Toolkit & Full Model Catalog:")
		fmt.Fprintln(cmd.OutOrStdout(), "  https://github.com/Koality-Assured/local-ai-management")
		return nil
	}

	fmt.Fprintln(cmd.OutOrStdout(), "\n=== Activation Steps (Human Approved) ===")
	if endpointOnline {
		fmt.Fprintln(cmd.OutOrStdout(), "Local AI engine is ONLINE and responding at "+baseURL)
		fmt.Fprintln(cmd.OutOrStdout(), "You can immediately chat with local models via:")
		defaultM := hw.RecommendedModels[0]["name"]
		fmt.Fprintf(cmd.OutOrStdout(), "  harness chat --provider local --model %s -q \"Hello from local AI\"\n", defaultM)
	} else {
		fmt.Fprintln(cmd.OutOrStdout(), "Local AI runtime is currently offline. To complete activation:")
		fmt.Fprintln(cmd.OutOrStdout(), "  1. Install Ollama:")
		fmt.Fprintln(cmd.OutOrStdout(), "     Windows: winget install Ollama.Ollama")
		fmt.Fprintln(cmd.OutOrStdout(), "     macOS:   brew install ollama")
		fmt.Fprintln(cmd.OutOrStdout(), "     Linux:   curl -fsSL https://ollama.com/install.sh | sh")
		fmt.Fprintln(cmd.OutOrStdout(), "  2. Start service: ollama serve")
		fmt.Fprintln(cmd.OutOrStdout(), "  3. Pull recommended models:")
		for i, m := range hw.RecommendedModels {
			if i >= 3 {
				break
			}
			fmt.Fprintf(cmd.OutOrStdout(), "     ollama pull %s\n", m["name"])
		}
		fmt.Fprintln(cmd.OutOrStdout(), "  4. Verify connectivity:")
		fmt.Fprintln(cmd.OutOrStdout(), "     harness local status")
	}
	return nil
}
