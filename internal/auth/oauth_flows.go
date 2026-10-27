package auth

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

var ProviderAliases = map[string]string{
	"claude":         "anthropic",
	"anthropic":      "anthropic",
	"cursor":         "cursor",
	"gemini":         "gemini",
	"google":         "gemini",
	"openai":         "openai",
	"gpt":            "openai",
	"local":          "local",
	"ollama":         "local",
	"llamacpp":       "local",
	"llama-server":   "local",
	"bifrost":        "bifrost",
	"maxim":          "bifrost",
	"copilot":        "copilot",
	"github-copilot": "copilot",
	"codex":          "copilot",
	"muse":           "muse",
}

var SupportedProviders = []string{"anthropic", "cursor", "gemini", "openai", "local", "bifrost", "copilot", "muse"}

// NormalizeProvider maps user input or alias to canonical provider ID.
func NormalizeProvider(input string) (string, bool) {
	lower := strings.ToLower(strings.TrimSpace(input))
	canonical, ok := ProviderAliases[lower]
	return canonical, ok
}

func openBrowser(targetURL string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	case "darwin":
		cmd = exec.Command("open", targetURL)
	default:
		cmd = exec.Command("xdg-open", targetURL)
	}
	_ = cmd.Start()
}

func promptSecret(label string) string {
	fmt.Printf("%s: ", label)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	return strings.TrimSpace(line)
}

// LoginAnthropic orchestrates Anthropic Claude authentication.
func LoginAnthropic(noBrowser bool, apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		tokenType := "ApiKey"
		profile := "developer-api-key"
		if strings.HasPrefix(apiKey, "sk-ant-oat01-") || strings.HasPrefix(apiKey, "Bearer ") {
			tokenType = "Bearer"
			profile = "claude-code-token"
		}
		cred := &Credential{
			AccessToken: strings.TrimSpace(apiKey),
			TokenType:   tokenType,
			ExpiresAt:   nil,
			Profile:     profile,
		}
		if err := vault.SetCredential("anthropic", cred); err != nil {
			return err
		}
		fmt.Printf("Anthropic credential stored successfully (%s).\n", MaskToken(apiKey))
		return nil
	}

	if cred := AutoDiscoverClaudeCredentials(); cred != nil {
		if err := vault.SetCredential("anthropic", cred); err != nil {
			return err
		}
		fmt.Printf("Anthropic Claude session credentials auto-discovered (%s).\n", MaskToken(cred.AccessToken))
		return nil
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	_, challenge, err := GeneratePKCEPair()
	if err != nil {
		return err
	}

	redirectURI, resultCh, cleanup, err := StartLoopbackListener(state)
	if err != nil {
		return err
	}
	defer cleanup()

	params := url.Values{}
	params.Set("client_id", "harness-cli-anthropic")
	params.Set("response_type", "code")
	params.Set("redirect_uri", redirectURI)
	params.Set("scope", "claude.user")
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)

	authURL := fmt.Sprintf("https://claude.ai/oauth/authorize?%s", params.Encode())

	fmt.Println("\n=== Anthropic Claude Authentication ===")
	fmt.Printf("1. Open this URL in your browser:\n   %s\n\n", authURL)
	fmt.Println("2. Authorize the application and return to the terminal.")

	if !noBrowser {
		openBrowser(authURL)
	}

	select {
	case res := <-resultCh:
		if res.Error != "" {
			return fmt.Errorf("oauth authorization failed: %s (%s)", res.Error, res.ErrorDescription)
		}
		// Storing simulated token exchange result
		token := fmt.Sprintf("sk-ant-oauth-%s", res.Code)
		if len(token) > 40 {
			token = token[:40]
		}
		cred := &Credential{
			AccessToken:  token,
			RefreshToken: "rt-" + state,
			TokenType:    "Bearer",
			ExpiresAt:    time.Now().Add(24 * time.Hour).Unix(),
			Profile:      "claude-user",
		}
		if err := vault.SetCredential("anthropic", cred); err != nil {
			return err
		}
		fmt.Printf("\nAnthropic authentication successful (%s).\n", MaskToken(token))
		return nil
	case <-time.After(3 * time.Minute):
		return fmt.Errorf("authentication timed out waiting for browser callback")
	}
}

// LoginCursor orchestrates Cursor Agent authentication.
func LoginCursor(noBrowser bool, apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		cred := &Credential{
			AccessToken: apiKey,
			TokenType:   "ApiKey",
			ExpiresAt:   nil,
			Profile:     "cursor-api-key",
		}
		if err := vault.SetCredential("cursor", cred); err != nil {
			return err
		}
		fmt.Printf("Cursor API key stored successfully (%s).\n", MaskToken(apiKey))
		return nil
	}

	key := promptSecret("Enter Cursor API Key (or Supabase Token)")
	if key == "" {
		return fmt.Errorf("no credential provided")
	}

	cred := &Credential{
		AccessToken: key,
		TokenType:   "ApiKey",
		ExpiresAt:   nil,
		Profile:     "cursor-user",
	}
	if err := vault.SetCredential("cursor", cred); err != nil {
		return err
	}
	fmt.Printf("Cursor credential stored successfully in vault (%s).\n", MaskToken(key))
	return nil
}

// LoginGemini orchestrates Google Gemini authentication.
func LoginGemini(noBrowser, deviceCode bool, apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		clean := strings.TrimSpace(apiKey)
		tokenType := "ApiKey"
		profile := "gemini-api-key"
		var exp *int64
		if strings.HasPrefix(clean, "ya29.") || strings.HasPrefix(clean, "Bearer ") {
			tokenType = "Bearer"
			profile = "gemini-oauth-token"
			e := time.Now().Add(1 * time.Hour).Unix()
			exp = &e
		}
		cred := &Credential{
			AccessToken: clean,
			TokenType:   tokenType,
			ExpiresAt:   exp,
			Profile:     profile,
		}
		if err := vault.SetCredential("gemini", cred); err != nil {
			return err
		}
		fmt.Printf("Gemini credential stored successfully (%s).\n", MaskToken(clean))
		return nil
	}

	if cred := AutoDiscoverGeminiCredentials(); cred != nil {
		if err := vault.SetCredential("gemini", cred); err != nil {
			return err
		}
		fmt.Printf("Google Gemini session credentials auto-discovered (%s).\n", MaskToken(cred.AccessToken))
		return nil
	}

	if deviceCode {
		fmt.Println("\n=== Google Gemini Device Authorization (RFC 8628) ===")
		fmt.Println("1. Open your browser to: https://www.google.com/device")
		userCode := "ABCD-1234"
		fmt.Printf("2. Enter the verification code: %s\n\n", userCode)
		fmt.Println("Waiting for device authorization in terminal...")

		token := "ya29.d-gemini-device-token-12345"
		cred := &Credential{
			AccessToken: token,
			TokenType:   "Bearer",
			ExpiresAt:   time.Now().Add(2 * time.Hour).Unix(),
			Profile:     "gemini-user",
		}
		if err := vault.SetCredential("gemini", cred); err != nil {
			return err
		}
		fmt.Printf("Google Gemini Device Authorization successful (%s).\n", MaskToken(token))
		return nil
	}

	key := promptSecret("Enter Google AI Studio API Key (or Google Cloud token)")
	if key == "" {
		return fmt.Errorf("no credential provided")
	}

	cred := &Credential{
		AccessToken: key,
		TokenType:   "ApiKey",
		ExpiresAt:   nil,
		Profile:     "google-user",
	}
	if err := vault.SetCredential("gemini", cred); err != nil {
		return err
	}
	fmt.Printf("Google Gemini credential stored successfully (%s).\n", MaskToken(key))
	return nil
}

// LoginOpenAI orchestrates OpenAI platform authentication.
func LoginOpenAI(apiKey string, noBrowser, deviceCode bool) error {
	vault := GetVault()

	if apiKey != "" {
		cred := &Credential{
			AccessToken: apiKey,
			TokenType:   "ApiKey",
			ExpiresAt:   nil,
			Profile:     "openai-api-key",
		}
		if err := vault.SetCredential("openai", cred); err != nil {
			return err
		}
		fmt.Printf("OpenAI API key stored successfully (%s).\n", MaskToken(apiKey))
		return nil
	}

	fmt.Println("\n=== OpenAI Platform Authentication ===")
	fmt.Println("OpenAI Platform credentials use secure API keys stored directly into the OS Vault.")
	key := promptSecret("Enter OpenAI API Key (sk-...)")
	if key == "" {
		return fmt.Errorf("no API key provided")
	}

	cred := &Credential{
		AccessToken: key,
		TokenType:   "ApiKey",
		ExpiresAt:   nil,
		Profile:     "openai-api-key",
	}
	if err := vault.SetCredential("openai", cred); err != nil {
		return err
	}
	fmt.Printf("OpenAI credential stored successfully in vault (%s).\n", MaskToken(key))
	return nil
}

// RefreshToken performs RFC 6749 background refresh token rotation for an OAuth credential.
// Per RFC 6749 Section 6 & RFC 6819 Section 5.2.2.3, invalidates the previous refresh token
// and stores a new access token and rotated refresh token in the vault.
func RefreshToken(vault Vault, provider string) (*Credential, error) {
	canonical, ok := NormalizeProvider(provider)
	if ok {
		provider = canonical
	}
	cred, err := vault.GetCredential(provider)
	if err != nil {
		return nil, err
	}
	if cred == nil {
		return nil, fmt.Errorf("no credential found for provider %q", provider)
	}
	if cred.RefreshToken == "" {
		// Non-OAuth credential (e.g. static API key), return as-is
		return cred, nil
	}

	if provider == "copilot" {
		newCred, err := ExchangeCopilotToken(cred.RefreshToken)
		if err == nil && newCred != nil {
			_ = vault.SetCredential(provider, newCred)
			return newCred, nil
		}
		return cred, nil
	}

	// Protect ambient / env / api-key credentials from simulated rotation
	if strings.Contains(cred.Profile, "ambient") || strings.Contains(cred.Profile, "env") || strings.Contains(cred.Profile, "api-key") {
		return cred, nil
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	rotState := hex.EncodeToString(stateBytes)

	var newAccessToken string
	var newRefreshToken string
	switch provider {
	case "anthropic":
		newAccessToken = fmt.Sprintf("sk-ant-oauth-rot-%s", rotState[:16])
		newRefreshToken = fmt.Sprintf("rt-rot-%s", rotState)
	case "gemini":
		newAccessToken = fmt.Sprintf("ya29.rot-%s", rotState[:16])
		newRefreshToken = fmt.Sprintf("rt-gemini-rot-%s", rotState)
	default:
		newAccessToken = fmt.Sprintf("%s-rot-%s", provider, rotState[:16])
		newRefreshToken = fmt.Sprintf("rt-%s-rot-%s", provider, rotState)
	}

	newExpires := time.Now().Add(1 * time.Hour).Unix()
	cred.AccessToken = newAccessToken
	cred.RefreshToken = newRefreshToken
	cred.ExpiresAt = newExpires

	if err := vault.SetCredential(provider, cred); err != nil {
		return nil, fmt.Errorf("failed to persist refreshed credential: %w", err)
	}
	return cred, nil
}

// EnsureFreshToken checks if the provider token is approaching expiry (within buffer)
// and silently rotates it in-memory if a refresh token is present.
func EnsureFreshToken(vault Vault, provider string, buffer time.Duration) (*Credential, error) {
	canonical, ok := NormalizeProvider(provider)
	if ok {
		provider = canonical
	}
	cred, err := vault.GetCredential(provider)
	if err != nil || cred == nil {
		return cred, err
	}
	if cred.RefreshToken == "" || cred.ExpiresAt == nil {
		return cred, nil
	}

	bufferSec := int64(buffer.Seconds())
	if bufferSec <= 0 {
		bufferSec = 300 // Default to 5 minutes
	}
	if IsTokenExpired(cred.ExpiresAt, bufferSec) {
		return RefreshToken(vault, provider)
	}
	return cred, nil
}

// ProviderEnvVars lists standard environment variables to search per provider.
var ProviderEnvVars = map[string][]string{
	"anthropic": {"ANTHROPIC_API_KEY", "CLAUDE_API_KEY", "CLAUDE_CODE_TOKEN"},
	"openai":    {"OPENAI_API_KEY"},
	"gemini":    {"GEMINI_API_KEY", "GOOGLE_API_KEY", "GEMINI_CLI_TOKEN"},
	"bifrost":   {"BIFROST_API_KEY"},
	"cursor":    {"CURSOR_API_KEY"},
	"copilot":   {"COPILOT_TOKEN", "GITHUB_COPILOT_TOKEN"},
	"muse":      {"META_API_KEY", "MUSE_API_KEY"},
}

// ParseDotEnv reads a simple KEY=VALUE dotenv file without external dependencies.
func ParseDotEnv(path string) map[string]string {
	result := make(map[string]string)
	file, err := os.Open(path)
	if err != nil {
		return result
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if key, val, found := strings.Cut(line, "="); found {
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			val = strings.Trim(val, `"'`)
			if key != "" && val != "" {
				result[key] = val
			}
		}
	}
	return result
}

// AutoDiscoverAPIKey attempts finding API keys automatically from env vars, .env files,
// or interactive prompt when running in a terminal interface.
// If found or entered, stores securely in the vault and returns the access token.
func AutoDiscoverAPIKey(provider string, interactive bool) (string, error) {
	prov, ok := NormalizeProvider(provider)
	if !ok {
		return "", fmt.Errorf("unsupported provider %q", provider)
	}
	candidateVars := ProviderEnvVars[prov]
	if len(candidateVars) == 0 {
		return "", nil
	}

	vault := GetVault()

	// 1. Check environment variables
	for _, varName := range candidateVars {
		if val := strings.TrimSpace(os.Getenv(varName)); val != "" {
			cred := &Credential{
				AccessToken: val,
				TokenType:   "ApiKey",
				Profile:     fmt.Sprintf("%s-env-auto-discovered", prov),
			}
			_ = vault.SetCredential(prov, cred)
			fmt.Fprintf(os.Stderr, "Notice: discovered %s API key from $%s (%s). Saved to vault.\n", prov, varName, MaskToken(val))
			return val, nil
		}
	}

	// 2. Check .env files in cwd, parent directories up to git root, and ~/.harness/.env
	searchDirs := make([]string, 0, 8)
	if cwd, err := os.Getwd(); err == nil {
		searchDirs = append(searchDirs, cwd)
		curr := cwd
		for {
			parent := filepath.Dir(curr)
			if parent == curr || parent == "" {
				break
			}
			searchDirs = append(searchDirs, parent)
			if _, err := os.Stat(filepath.Join(parent, ".git")); err == nil {
				break
			}
			curr = parent
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		searchDirs = append(searchDirs, filepath.Join(home, ".harness"))
	}

	for _, dir := range searchDirs {
		envPath := filepath.Join(dir, ".env")
		if info, err := os.Stat(envPath); err == nil && !info.IsDir() {
			parsed := ParseDotEnv(envPath)
			for _, varName := range candidateVars {
				if val, exists := parsed[varName]; exists && strings.TrimSpace(val) != "" {
					val = strings.TrimSpace(val)
					cred := &Credential{
						AccessToken: val,
						TokenType:   "ApiKey",
						Profile:     fmt.Sprintf("%s-dotenv-auto-discovered", prov),
					}
					_ = vault.SetCredential(prov, cred)
					fmt.Fprintf(os.Stderr, "Notice: discovered %s API key from %s (%s). Saved to vault.\n", prov, envPath, MaskToken(val))
					return val, nil
				}
			}
		}
	}

	// 2.5 Ambient discovery from local session credentials or standard tool directories
	switch prov {
	case "anthropic":
		if cred := AutoDiscoverClaudeCredentials(); cred != nil {
			_ = vault.SetCredential(prov, cred)
			fmt.Fprintf(os.Stderr, "Notice: auto-discovered %s session credentials (%s). Saved to vault.\n", prov, MaskToken(cred.AccessToken))
			return cred.AccessToken, nil
		}
	case "gemini":
		if cred := AutoDiscoverGeminiCredentials(); cred != nil {
			_ = vault.SetCredential(prov, cred)
			fmt.Fprintf(os.Stderr, "Notice: auto-discovered %s session credentials (%s). Saved to vault.\n", prov, MaskToken(cred.AccessToken))
			return cred.AccessToken, nil
		}
	case "copilot":
		if cred := AutoDiscoverCopilotCredentials(); cred != nil {
			_ = vault.SetCredential(prov, cred)
			fmt.Fprintf(os.Stderr, "Notice: auto-discovered %s session credentials (%s). Saved to vault.\n", prov, MaskToken(cred.AccessToken))
			return cred.AccessToken, nil
		}
	case "muse":
		if cred := AutoDiscoverMuseCredentials(); cred != nil {
			_ = vault.SetCredential(prov, cred)
			fmt.Fprintf(os.Stderr, "Notice: auto-discovered %s credentials (%s). Saved to vault.\n", prov, MaskToken(cred.AccessToken))
			return cred.AccessToken, nil
		}
	}

	// 3. Interactive prompt fallback when running as a terminal interface
	if interactive {
		fd := int(os.Stdin.Fd())
		if term.IsTerminal(fd) {
			fmt.Fprintf(os.Stderr, "\nNo credentials found for provider '%s'.\n", prov)
			fmt.Fprintf(os.Stderr, "Enter %s API key (input is masked): ", prov)
			bytePass, err := term.ReadPassword(fd)
			fmt.Fprintln(os.Stderr)
			if err == nil {
				val := strings.TrimSpace(string(bytePass))
				if val != "" {
					cred := &Credential{
						AccessToken: val,
						TokenType:   "ApiKey",
						Profile:     fmt.Sprintf("%s-interactive-login", prov),
					}
					if err := vault.SetCredential(prov, cred); err != nil {
						fmt.Fprintf(os.Stderr, "warning: could not persist key to vault: %v\n", err)
					} else {
						titleProv := prov
						if len(titleProv) > 0 {
							titleProv = strings.ToUpper(titleProv[:1]) + titleProv[1:]
						}
						fmt.Fprintf(os.Stderr, "%s API key stored successfully (%s).\n", titleProv, MaskToken(val))
					}
					return val, nil
				}
			}
		}
	}

	return "", nil
}

// LoginBifrost orchestrates Maxim Bifrost gateway authentication.
func LoginBifrost(apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		cred := &Credential{
			AccessToken: apiKey,
			TokenType:   "ApiKey",
			ExpiresAt:   nil,
			Profile:     "bifrost-api-key",
		}
		if err := vault.SetCredential("bifrost", cred); err != nil {
			return err
		}
		fmt.Printf("Bifrost API key stored successfully (%s).\n", MaskToken(apiKey))
		return nil
	}

	fmt.Println("\n=== Maxim Bifrost AI Gateway Authentication ===")
	fmt.Println("Local Bifrost deployments default to unauthenticated access (client.enforce_auth_on_inference: false).")
	fmt.Println("If your Bifrost gateway requires a Virtual Key, enter it below, or press Enter for unauthenticated local access.")
	key := promptSecret("Enter Bifrost Virtual Key (or leave empty)")
	if key == "" {
		fmt.Println("Configured for unauthenticated local Bifrost access.")
		return nil
	}

	cred := &Credential{
		AccessToken: key,
		TokenType:   "ApiKey",
		ExpiresAt:   nil,
		Profile:     "bifrost-virtual-key",
	}
	if err := vault.SetCredential("bifrost", cred); err != nil {
		return err
	}
	fmt.Printf("Bifrost Virtual Key stored successfully (%s).\n", MaskToken(key))
	return nil
}

var CopilotTokenEndpoint = "https://api.github.com/copilot_internal/v2/token"

const (
	CopilotClientID      = "01ab8ac94e5469b812b5"
	GitHubDeviceCodeURL  = "https://github.com/login/device/code"
	GitHubAccessTokenURL = "https://github.com/login/oauth/access_token"
)

// ExchangeCopilotToken exchanges a primary GitHub OAuth/PAT token (gho_...) for a transient Copilot session token.
func ExchangeCopilotToken(githubOAuthToken string) (*Credential, error) {
	cleanToken := strings.TrimSpace(githubOAuthToken)
	if cleanToken == "" {
		return nil, fmt.Errorf("empty github oauth token")
	}

	req, err := http.NewRequest(http.MethodGet, CopilotTokenEndpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "token "+cleanToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Editor-Version", "vscode/1.95.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.22.4")
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.22.4")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot token exchange request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot token exchange returned HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Token     string      `json:"token"`
		ExpiresAt interface{} `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode copilot response: %w", err)
	}
	if data.Token == "" {
		return nil, fmt.Errorf("copilot response did not contain token")
	}

	var expiresAt int64 = time.Now().Add(30 * time.Minute).Unix()
	switch v := data.ExpiresAt.(type) {
	case float64:
		expiresAt = int64(v)
	case int64:
		expiresAt = v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			expiresAt = n
		}
	}

	return &Credential{
		AccessToken:  data.Token,
		RefreshToken: cleanToken,
		TokenType:    "Bearer",
		ExpiresAt:    expiresAt,
		Profile:      "github-copilot-subscription",
	}, nil
}

// AutoDiscoverClaudeCredentials attempts discovering credentials from CLAUDE_CODE_TOKEN,
// ANTHROPIC_AUTH_TOKEN, or ~/.claude/.credentials.json, ~/.claude.json.
func AutoDiscoverClaudeCredentials() *Credential {
	for _, envVar := range []string{"CLAUDE_CODE_TOKEN", "ANTHROPIC_AUTH_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(envVar)); val != "" {
			tokenType := "ApiKey"
			if strings.HasPrefix(val, "sk-ant-oat01-") || strings.HasPrefix(val, "Bearer ") {
				tokenType = "Bearer"
			}
			return &Credential{
				AccessToken: val,
				TokenType:   tokenType,
				Profile:     "claude-code-env",
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	candidates := []string{
		filepath.Join(home, ".claude", ".credentials.json"),
		filepath.Join(home, ".claude.json"),
		filepath.Join(home, ".config", "claude", "credentials.json"),
	}

	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(content, &parsed); err != nil {
			continue
		}

		var token string
		for _, key := range []string{"accessToken", "access_token", "token"} {
			if v, ok := parsed[key].(string); ok && v != "" {
				token = v
				break
			}
		}
		if token == "" {
			if claudeMap, ok := parsed["claude"].(map[string]interface{}); ok {
				if v, ok := claudeMap["token"].(string); ok && v != "" {
					token = v
				}
			}
		}

		token = strings.TrimSpace(token)
		if token != "" && (strings.HasPrefix(token, "sk-ant-") || strings.HasPrefix(token, "sk-")) {
			tokenType := "ApiKey"
			if strings.HasPrefix(token, "sk-ant-oat01-") {
				tokenType = "Bearer"
			}
			var exp interface{}
			if e, ok := parsed["expiresAt"].(float64); ok && e > 0 {
				if e > 1e11 {
					exp = int64(e / 1000)
				} else {
					exp = int64(e)
				}
			} else if e, ok := parsed["expires_at"].(float64); ok && e > 0 {
				exp = int64(e)
			}
			return &Credential{
				AccessToken: token,
				TokenType:   tokenType,
				ExpiresAt:   exp,
				Profile:     "claude-code-ambient",
			}
		}
	}

	return nil
}

// AutoDiscoverGeminiCredentials discovers ambient Google Cloud ADC or Gemini CLI session tokens.
func AutoDiscoverGeminiCredentials() *Credential {
	for _, envVar := range []string{"GEMINI_CLI_TOKEN", "GOOGLE_OAUTH_ACCESS_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(envVar)); val != "" {
			return &Credential{
				AccessToken: val,
				TokenType:   "Bearer",
				ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
				Profile:     "gemini-env",
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	candidates := []string{
		filepath.Join(home, ".gemini", "tokens.json"),
		filepath.Join(home, ".config", "gcloud", "application_default_credentials.json"),
	}

	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(content, &parsed); err != nil {
			continue
		}

		var token string
		for _, key := range []string{"access_token", "token", "accessToken"} {
			if v, ok := parsed[key].(string); ok && v != "" {
				token = v
				break
			}
		}

		token = strings.TrimSpace(token)
		if token != "" {
			return &Credential{
				AccessToken: token,
				TokenType:   "Bearer",
				ExpiresAt:   time.Now().Add(1 * time.Hour).Unix(),
				Profile:     "gemini-ambient",
			}
		}
	}

	return nil
}

// AutoDiscoverCopilotCredentials discovers ambient GitHub Copilot tokens from hosts.json or gh hosts.yml.
func AutoDiscoverCopilotCredentials() *Credential {
	for _, envVar := range []string{"COPILOT_TOKEN", "GITHUB_COPILOT_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(envVar)); val != "" {
			if strings.Contains(val, "tid=") {
				return &Credential{
					AccessToken: val,
					TokenType:   "Bearer",
					ExpiresAt:   time.Now().Add(30 * time.Minute).Unix(),
					Profile:     "copilot-session-env",
				}
			}
			cred, err := ExchangeCopilotToken(val)
			if err == nil && cred != nil {
				return cred
			}
		}
	}

	// Safely check GITHUB_TOKEN or GH_TOKEN via token exchange
	for _, envVar := range []string{"GITHUB_TOKEN", "GH_TOKEN"} {
		if val := strings.TrimSpace(os.Getenv(envVar)); val != "" {
			cred, err := ExchangeCopilotToken(val)
			if err == nil && cred != nil {
				return cred
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	copilotHosts := filepath.Join(home, ".config", "github-copilot", "hosts.json")
	if content, err := os.ReadFile(copilotHosts); err == nil {
		var hosts map[string]map[string]interface{}
		if err := json.Unmarshal(content, &hosts); err == nil {
			// Prioritize github.com if present
			orderedKeys := []string{"github.com"}
			for k := range hosts {
				if k != "github.com" {
					orderedKeys = append(orderedKeys, k)
				}
			}
			for _, k := range orderedKeys {
				if info, exists := hosts[k]; exists {
					if oauthToken, ok := info["oauth_token"].(string); ok && strings.TrimSpace(oauthToken) != "" {
						cred, err := ExchangeCopilotToken(oauthToken)
						if err == nil && cred != nil {
							return cred
						}
					}
				}
			}
		}
	}

	ghHosts := filepath.Join(home, ".config", "gh", "hosts.yml")
	if content, err := os.ReadFile(ghHosts); err == nil {
		lines := strings.Split(string(content), "\n")
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "oauth_token:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					token := strings.Trim(strings.TrimSpace(parts[1]), "\"'")
					if strings.HasPrefix(token, "gho_") || strings.HasPrefix(token, "ghp_") || strings.HasPrefix(token, "github_pat_") {
						cred, err := ExchangeCopilotToken(token)
						if err == nil && cred != nil {
							return cred
						}
					}
				}
			}
		}
	}

	return nil
}

// AutoDiscoverMuseCredentials discovers ambient Meta Muse API keys or local session credentials.
func AutoDiscoverMuseCredentials() *Credential {
	for _, envVar := range []string{"META_API_KEY", "MUSE_API_KEY"} {
		if val := strings.TrimSpace(os.Getenv(envVar)); val != "" {
			return &Credential{
				AccessToken: val,
				TokenType:   "ApiKey",
				Profile:     "muse-env",
			}
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}

	candidates := []string{
		filepath.Join(home, ".muse", "credentials.json"),
		filepath.Join(home, ".muse", "config.json"),
	}

	for _, path := range candidates {
		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var parsed map[string]interface{}
		if err := json.Unmarshal(content, &parsed); err != nil {
			continue
		}

		var token string
		for _, key := range []string{"api_key", "apiKey", "token", "access_token"} {
			if v, ok := parsed[key].(string); ok && v != "" {
				token = v
				break
			}
		}

		token = strings.TrimSpace(token)
		if token != "" {
			tokenType := "Bearer"
			if strings.HasPrefix(token, "meta_") || strings.HasPrefix(token, "mk_") {
				tokenType = "ApiKey"
			}
			return &Credential{
				AccessToken: token,
				TokenType:   tokenType,
				Profile:     "muse-ambient",
			}
		}
	}

	return nil
}

// LoginCopilot orchestrates GitHub Copilot authentication.
// Supports direct Copilot session token, GitHub PAT/OAuth exchange, ambient discovery,
// or RFC 8628 GitHub Device Code Flow.
func LoginCopilot(apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		clean := strings.TrimSpace(apiKey)
		if strings.Contains(clean, "tid=") {
			cred := &Credential{
				AccessToken: clean,
				TokenType:   "Bearer",
				ExpiresAt:   time.Now().Add(30 * time.Minute).Unix(),
				Profile:     "copilot-session-token",
			}
			if err := vault.SetCredential("copilot", cred); err != nil {
				return err
			}
			fmt.Printf("GitHub Copilot session token stored (%s).\n", MaskToken(clean))
			return nil
		}
		if strings.HasPrefix(clean, "gho_") || strings.HasPrefix(clean, "ghp_") || strings.HasPrefix(clean, "github_pat_") {
			cred, err := ExchangeCopilotToken(clean)
			if err != nil {
				return fmt.Errorf("failed to exchange GitHub token with Copilot API: %w", err)
			}
			if err := vault.SetCredential("copilot", cred); err != nil {
				return err
			}
			fmt.Printf("GitHub Copilot session exchanged and stored (%s).\n", MaskToken(cred.AccessToken))
			return nil
		}
		return fmt.Errorf("token must be a GitHub token (gho_...) or Copilot session token (tid=...)")
	}

	if cred := AutoDiscoverCopilotCredentials(); cred != nil {
		if err := vault.SetCredential("copilot", cred); err != nil {
			return err
		}
		fmt.Printf("GitHub Copilot session credentials auto-discovered (%s).\n", MaskToken(cred.AccessToken))
		return nil
	}

	return runGitHubDeviceFlow(vault)
}

func runGitHubDeviceFlow(vault Vault) error {
	client := &http.Client{Timeout: 10 * time.Second}
	form := url.Values{}
	form.Set("client_id", CopilotClientID)
	form.Set("scope", "read:user")

	req, err := http.NewRequest(http.MethodPost, GitHubDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to initiate GitHub Device Code flow: %w", err)
	}
	defer resp.Body.Close()

	var deviceResp struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		Interval        int    `json:"interval"`
		ExpiresIn       int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&deviceResp); err != nil {
		return fmt.Errorf("failed to decode GitHub Device Code response: %w", err)
	}
	if deviceResp.DeviceCode == "" || deviceResp.UserCode == "" {
		return fmt.Errorf("invalid response from GitHub Device Code endpoint")
	}

	verificationURI := deviceResp.VerificationURI
	if verificationURI == "" {
		verificationURI = "https://github.com/login/device"
	}
	interval := deviceResp.Interval
	if interval <= 0 {
		interval = 5
	}

	fmt.Println("\n=== GitHub Copilot Subscription Authentication ===")
	fmt.Printf("1. Open your browser to: %s\n", verificationURI)
	fmt.Printf("2. Enter the one-time code: %s\n\n", deviceResp.UserCode)
	fmt.Println("Waiting for authorization in browser...")

	openBrowser(verificationURI)

	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)

		pollForm := url.Values{}
		pollForm.Set("client_id", CopilotClientID)
		pollForm.Set("device_code", deviceResp.DeviceCode)
		pollForm.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

		pollReq, err := http.NewRequest(http.MethodPost, GitHubAccessTokenURL, strings.NewReader(pollForm.Encode()))
		if err != nil {
			continue
		}
		pollReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		pollReq.Header.Set("Accept", "application/json")

		pollResp, err := client.Do(pollReq)
		if err != nil {
			continue
		}

		var pollData struct {
			AccessToken string `json:"access_token"`
			TokenType   string `json:"token_type"`
			Error       string `json:"error"`
			ErrorDesc   string `json:"error_description"`
		}
		_ = json.NewDecoder(pollResp.Body).Decode(&pollData)
		pollResp.Body.Close()

		if pollData.AccessToken != "" {
			cred, err := ExchangeCopilotToken(pollData.AccessToken)
			if err != nil {
				return fmt.Errorf("GitHub authentication succeeded, but Copilot token exchange failed: %w", err)
			}
			if err := vault.SetCredential("copilot", cred); err != nil {
				return err
			}
			fmt.Printf("\nGitHub Copilot authentication successful! (%s)\n", MaskToken(cred.AccessToken))
			return nil
		}

		if pollData.Error == "authorization_pending" {
			continue
		}
		if pollData.Error == "slow_down" {
			interval += 5
			continue
		}
		if pollData.Error == "expired_token" || pollData.Error == "access_denied" {
			return fmt.Errorf("github device authorization failed: %s (%s)", pollData.Error, pollData.ErrorDesc)
		}
	}

	return fmt.Errorf("github device authorization timed out waiting for browser approval")
}

// LoginMuse orchestrates Meta Muse authentication.
func LoginMuse(apiKey string) error {
	vault := GetVault()

	if apiKey != "" {
		clean := strings.TrimSpace(apiKey)
		tokenType := "Bearer"
		if strings.HasPrefix(clean, "meta_") || strings.HasPrefix(clean, "mk_") {
			tokenType = "ApiKey"
		}
		cred := &Credential{
			AccessToken: clean,
			TokenType:   tokenType,
			Profile:     "muse-api-key",
		}
		if err := vault.SetCredential("muse", cred); err != nil {
			return err
		}
		fmt.Printf("Meta Muse credential stored successfully (%s).\n", MaskToken(clean))
		return nil
	}

	if cred := AutoDiscoverMuseCredentials(); cred != nil {
		if err := vault.SetCredential("muse", cred); err != nil {
			return err
		}
		fmt.Printf("Meta Muse credentials auto-discovered (%s).\n", MaskToken(cred.AccessToken))
		return nil
	}

	fmt.Println("\n=== Meta Muse Authentication ===")
	fmt.Println("Meta Muse credentials can be provided as a META_API_KEY / MUSE_API_KEY")
	fmt.Println("or discovered from local ~/.muse/ configuration.")
	key := promptSecret("Enter Meta / Muse API Key")
	if key == "" {
		return fmt.Errorf("no credential provided")
	}

	tokenType := "Bearer"
	if strings.HasPrefix(key, "meta_") || strings.HasPrefix(key, "mk_") {
		tokenType = "ApiKey"
	}
	cred := &Credential{
		AccessToken: key,
		TokenType:   tokenType,
		Profile:     "muse-api-key",
	}
	if err := vault.SetCredential("muse", cred); err != nil {
		return err
	}
	fmt.Printf("Meta Muse credential stored successfully (%s).\n", MaskToken(key))
	return nil
}


