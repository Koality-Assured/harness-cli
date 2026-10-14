package commands

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Koality-Assured/harness-cli/internal/git"
	"github.com/spf13/cobra"
)

var (
	deferralHost      string
	deferralRecord429 int
	deferralClear429  bool
)

var deferralCmd = &cobra.Command{
	Use:          "deferral [prompt]",
	Short:        "Inspect model deferral eligibility and secondary quota state",
	SilenceUsage: true,
	RunE:         runDeferral,
}

func init() {
	deferralCmd.Flags().StringVar(&deferralHost, "host", "antigravity", "Target host platform (antigravity, cursor, etc.)")
	deferralCmd.Flags().IntVar(&deferralRecord429, "record-429", -1, "Record secondary 429 lockout cooldown in seconds")
	deferralCmd.Flags().BoolVar(&deferralClear429, "clear-429", false, "Clear secondary 429 lockout cooldown state")
}

var triggerOrder = []string{
	"state_and_concurrency",
	"rigid_invariant_refactoring",
	"adversarial_security_audit",
	"ast_and_regex_parser",
	"dense_technical_rfc",
}

var deferralTriggers = map[string][]string{
	"state_and_concurrency": {
		`(?i)\brace condition\b`,
		`(?i)\bdistributed lock(?:ing)?\b`,
		`(?i)\bmutex hierarch(?:y|ies)\b`,
		`(?i)\bdeadlock\b`,
		`(?i)\bchannel synchroniz\w+\b`,
		`(?i)\batomic state machine\b`,
		`(?i)\bthread safety\b`,
		`(?i)\bmemory ordering\b`,
		`(?i)\bconcurrent state machine\b`,
		`(?i)\bgoroutine leak\b`,
		`(?i)\block inversion\b`,
	},
	"rigid_invariant_refactoring": {
		`(?i)\binvariant refactor(?:ing)?\b`,
		`(?i)\bpreserv(?:e|ing) invariants\b`,
		`(?i)\bstructural refactor(?:ing)?\b`,
		`(?i)\brigid (?:type|semantic) invariants\b`,
		`(?i)\bsemantic invariants\b`,
		`(?i)\bcross-module coupling\b`,
		`(?i)\binterface invariants\b`,
		`(?i)\barchitectural refactor(?:ing)?\b`,
	},
	"adversarial_security_audit": {
		`(?i)\badversarial (?:code )?audit\b`,
		`(?i)\bsecurity audit\b`,
		`(?i)\btiming attack\b`,
		`(?i)\btiming leak\b`,
		`(?i)\bauth(?:orization)? bypass\b`,
		`(?i)\bcryptographic flaw\b`,
		`(?i)\breentrancy\b`,
		`(?i)\bside-channel\b`,
		`(?i)\bconstant[- ]time\b`,
		`(?i)\bprivilege escalation\b`,
		`(?i)\btoken refresh\b`,
		`(?i)\bsession verification\b`,
		`(?i)\bsession fixation\b`,
		`(?i)\bcsrf (?:protection|attack|token|defense|mismatch)\b`,
		`(?i)\boauth (?:security|audit|vulnerability|flaw)\b`,
		`(?i)\btoken lifecycle\b`,
	},
	"ast_and_regex_parser": {
		`(?i)\btree-sitter grammar\b`,
		`(?i)\bast (?:compiler|parser)\b`,
		`(?i)\bcompiler grammar\b`,
		`(?i)\bregex engine\b`,
		`(?i)\bnested regex\b`,
		`(?i)\btoken-level grammar\b`,
		`(?i)\bparser grammar\b`,
		`(?i)\blexer tokens\b`,
	},
	"dense_technical_rfc": {
		`(?i)\barchitectural? rfc\b`,
		`(?i)\bformal api schema\b`,
		`(?i)\btechnical specification\b`,
		`(?i)\brfc draft\b`,
		`(?i)\barchitectural? proposal\b`,
		`(?i)\bformal schema definition\b`,
		`(?i)\barchitectural decision record\b`,
	},
}

var operationalDisqualifiers = []string{
	`(?i)^(?:run|execute)\s+(?:pytest|unittest|tests?|suite)\b`,
	`(?i)^(?:git\s+)?(?:status|add|commit|push|pull|merge|checkout|fetch)\b`,
	`(?i)^(?:grep|rg|search)\s+(?:codebase|repo|files?|directory|docs?)\b`,
	`(?i)^(?:fix|clean)\s+(?:lint|linter|flake8|markdownlint|formatting|style)\b`,
	`(?i)^(?:generate|write)\s+(?:docstrings?|boilerplate|getters|setters)\b`,
}

type DeferralResult struct {
	ShouldDefer          bool    `json:"should_defer"`
	RecommendedModel     string  `json:"recommended_model"`
	Trigger              *string `json:"trigger"`
	Reason               string  `json:"reason"`
	QuotaProfile         string  `json:"quota_profile"`
	FallbackActive       bool    `json:"fallback_active"`
	RemainingCooldownSec int     `json:"remaining_cooldown_sec"`
	Notice               *string `json:"notice"`
}

func getQuotaStatePath() string {
	root, err := git.GetPrimaryRepoRoot("")
	if err != nil || root == "" {
		root = "."
	}
	return filepath.Join(root, "scratch", "quota_state.json")
}

func recordSecondaryExhaustion(resetSeconds int) error {
	p := getQuotaStatePath()
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	resetAt := now + float64(resetSeconds)
	data := map[string]any{
		"exhausted":     true,
		"provider":      "claude",
		"exhausted_at":  now,
		"reset_at":      resetAt,
		"reset_seconds": resetSeconds,
	}
	b, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0644)
}

func clearSecondaryExhaustion() error {
	p := getQuotaStatePath()
	if _, err := os.Stat(p); err == nil {
		data := map[string]any{"exhausted": false}
		b, _ := json.MarshalIndent(data, "", "  ")
		_ = os.WriteFile(p, b, 0644)
	}
	return nil
}

func checkSecondaryAvailability() (bool, int) {
	p := getQuotaStatePath()
	b, err := os.ReadFile(p)
	if err != nil {
		return true, 0
	}
	var data map[string]any
	if err := json.Unmarshal(b, &data); err != nil {
		return true, 0
	}
	exhausted, _ := data["exhausted"].(bool)
	if !exhausted {
		return true, 0
	}
	resetAt, _ := data["reset_at"].(float64)
	now := float64(time.Now().Unix())
	remaining := int(resetAt - now)
	if remaining <= 0 {
		_ = clearSecondaryExhaustion()
		return true, 0
	}
	return false, remaining
}

func classifyDeferralEligibility(prompt, host string) DeferralResult {
	promptClean := strings.TrimSpace(prompt)
	hostNorm := strings.ToLower(strings.TrimSpace(host))

	if hostNorm != "antigravity" {
		profile := "standard"
		if hostNorm == "cursor" || hostNorm == "enterprise" {
			profile = "unmetered"
		}
		return DeferralResult{
			ShouldDefer:          false,
			RecommendedModel:     "platform-native",
			Trigger:              nil,
			Reason:               fmt.Sprintf("Host '%s' follows platform-native model tiering.", host),
			QuotaProfile:         profile,
			FallbackActive:       false,
			RemainingCooldownSec: 0,
			Notice:               nil,
		}
	}

	for _, pattern := range operationalDisqualifiers {
		re := regexp.MustCompile(pattern)
		if re.MatchString(promptClean) {
			return DeferralResult{
				ShouldDefer:          false,
				RecommendedModel:     "Gemini 3.8 Flash",
				Trigger:              nil,
				Reason:               "Operational plumbing task reserved for Gemini Ultra high-throughput capacity.",
				QuotaProfile:         "standard",
				FallbackActive:       false,
				RemainingCooldownSec: 0,
				Notice:               nil,
			}
		}
	}

	var matchedTrigger *string
	for _, cat := range triggerOrder {
		patterns := deferralTriggers[cat]
		for _, pat := range patterns {
			re := regexp.MustCompile(pat)
			if re.MatchString(promptClean) {
				c := cat
				matchedTrigger = &c
				break
			}
		}
		if matchedTrigger != nil {
			break
		}
	}

	if matchedTrigger == nil {
		return DeferralResult{
			ShouldDefer:          false,
			RecommendedModel:     "Gemini 3.8 Flash",
			Trigger:              nil,
			Reason:               "Task does not meet material secondary deferral criteria; default to Gemini Ultra.",
			QuotaProfile:         "standard",
			FallbackActive:       false,
			RemainingCooldownSec: 0,
			Notice:               nil,
		}
	}

	isAvailable, rem := checkSecondaryAvailability()
	if !isAvailable {
		reason := fmt.Sprintf("Matches trigger '%s', but secondary quota is exhausted (%ds remaining). Zero-stall circuit-breaker fallback to Gemini 3.8 Flash (High).", *matchedTrigger, rem)
		remMin := int(math.Round(float64(rem) / 60.0))
		if remMin < 1 {
			remMin = 1
		}
		noticeStr := fmt.Sprintf("⚠️ Secondary Claude allocation exhausted (resets in %dm). Executing via Gemini 3.8 Flash (High) with enhanced reasoning prompts instead.", remMin)
		return DeferralResult{
			ShouldDefer:          false,
			RecommendedModel:     "Gemini 3.8 Flash (High)",
			Trigger:              matchedTrigger,
			Reason:               reason,
			QuotaProfile:         "metered_secondary",
			FallbackActive:       true,
			RemainingCooldownSec: rem,
			Notice:               &noticeStr,
		}
	}

	reason := fmt.Sprintf("Matches material deferral trigger '%s'. Active in rolling use-it-or-lose-it window.", *matchedTrigger)
	noticeStr := fmt.Sprintf("ℹ️ Task qualifies for Antigravity Ultra secondary model deferral (%s). Active in rolling 5-hour window -> Recommended model: Claude 5.5 Sonnet.", *matchedTrigger)
	return DeferralResult{
		ShouldDefer:          true,
		RecommendedModel:     "Claude 5.5 Sonnet",
		Trigger:              matchedTrigger,
		Reason:               reason,
		QuotaProfile:         "metered_secondary",
		FallbackActive:       false,
		RemainingCooldownSec: 0,
		Notice:               &noticeStr,
	}
}

func runDeferral(cmd *cobra.Command, args []string) error {
	if deferralClear429 {
		if err := clearSecondaryExhaustion(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Secondary model exhaustion state cleared.")
		return nil
	}

	if deferralRecord429 >= 0 {
		if err := recordSecondaryExhaustion(deferralRecord429); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Recorded secondary 429 exhaustion: resets in %ds.\n", deferralRecord429)
		return nil
	}

	prompt := strings.Join(args, " ")
	res := classifyDeferralEligibility(prompt, deferralHost)

	if JSONOutput {
		return writeJSON(cmd.OutOrStdout(), res)
	}

	fmt.Fprintln(cmd.OutOrStdout(), "=== Antigravity Ultra Model Deferral Analysis ===")
	fmt.Fprintf(cmd.OutOrStdout(), "Host:              %s\n", deferralHost)
	promptDisplay := prompt
	if promptDisplay == "" {
		promptDisplay = "(empty)"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Prompt:            %s\n", promptDisplay)
	fmt.Fprintf(cmd.OutOrStdout(), "Should Defer:      %v\n", res.ShouldDefer)
	fmt.Fprintf(cmd.OutOrStdout(), "Recommended Model: %s\n", res.RecommendedModel)
	triggerStr := "None"
	if res.Trigger != nil {
		triggerStr = *res.Trigger
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Trigger:           %s\n", triggerStr)
	fmt.Fprintf(cmd.OutOrStdout(), "Quota Profile:     %s\n", res.QuotaProfile)
	fmt.Fprintf(cmd.OutOrStdout(), "Fallback Active:   %v\n", res.FallbackActive)
	if res.RemainingCooldownSec > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Cooldown Left:     %ds\n", res.RemainingCooldownSec)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Reason:            %s\n", res.Reason)
	if res.Notice != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "\nNotice:\n  %s\n", *res.Notice)
	}
	return nil
}
