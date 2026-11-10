package codex

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
)

// The platform web search has one entry (lookupWeb) and one configuration.
// Architecture, keys, providers and pricing are documented in the workspace's
// docs/architecture/web-search.md; keep that document in step with this file.

// webSearchConfigEnv names the optional file holding the search configuration
// and credentials. The bridge reads it per call, so a rotated token or a new
// provider order needs no restart, and Agent commands never see it (see
// agentShellExcludedEnv). Without the file the default DeepSeek provider still
// works from the bridge's own DEEPSEEK_API_KEY.
const webSearchConfigEnv = "TOMAKO_WEB_SEARCH_CONFIG"

// webSearchScenario selects a provider chain. Scenarios come from trusted
// runtime metadata (tool profile, first-visit flag, the explicit supplement
// argument), never from the query text.
type webSearchScenario string

const (
	webSearchScenarioConversation         webSearchScenario = "conversation"
	webSearchScenarioResearch             webSearchScenario = "research"
	webSearchScenarioFirstVisit           webSearchScenario = "first_visit"
	webSearchScenarioFirstVisitSupplement webSearchScenario = "first_visit_supplement"
)

var webSearchScenarios = []webSearchScenario{
	webSearchScenarioConversation, webSearchScenarioResearch,
	webSearchScenarioFirstVisit, webSearchScenarioFirstVisitSupplement,
}

const (
	webSearchProviderDeepSeek = "deepseek"
	webSearchProviderBrave    = "brave"
	webSearchProviderAIsa     = "aisa"
)

// Defaults: DeepSeek answers every scenario; the explicit first-visit
// supplement goes to AIsa/Tavily, which is billed through the task capability.
var webSearchDefaultChains = map[webSearchScenario][]string{
	webSearchScenarioConversation:         {webSearchProviderDeepSeek},
	webSearchScenarioResearch:             {webSearchProviderDeepSeek},
	webSearchScenarioFirstVisit:           {webSearchProviderDeepSeek},
	webSearchScenarioFirstVisitSupplement: {webSearchProviderAIsa},
}

type webSearchConfig struct {
	DeepSeekKey string
	AccountID   string
	Token       string
	GatewayID   string
	BraveKey    string
	// Chains are tried in order: a later provider answers only when an
	// earlier one fails or finds nothing.
	Chains map[webSearchScenario][]string
	// Prices override the built-in price per successful search, in USD.
	Prices map[string]float64
}

// cloudflareRouted reports whether provider is reached through Cloudflare's Web
// Search API (exa, linkup and the other providers Cloudflare offers).
func cloudflareRouted(provider string) bool {
	return provider != webSearchProviderDeepSeek && provider != webSearchProviderBrave && provider != webSearchProviderAIsa
}

// hasCredentials reports whether the configuration can call provider. AIsa uses
// the current task's capability instead of a key; see webSearchProviderReady.
func (c webSearchConfig) hasCredentials(provider string) bool {
	switch provider {
	case webSearchProviderDeepSeek:
		return c.DeepSeekKey != ""
	case webSearchProviderBrave:
		return c.BraveKey != ""
	case webSearchProviderAIsa:
		return true
	default:
		return c.AccountID != "" && c.Token != ""
	}
}

func (c webSearchConfig) chain(scenario webSearchScenario) []string {
	if chain := c.Chains[scenario]; len(chain) > 0 {
		return chain
	}
	return webSearchDefaultChains[scenario]
}

var legacyWebSearchKeyWarning sync.Once

func readWebSearchConfigFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("web search configuration is unreadable: %w", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		}
		values[strings.TrimSpace(key)] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("web search configuration is unreadable: %w", err)
	}
	return values, nil
}

func parseWebSearchChain(raw string) []string {
	var chain []string
	seen := map[string]bool{}
	for _, provider := range strings.Split(raw, ",") {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" && !seen[provider] {
			seen[provider] = true
			chain = append(chain, provider)
		}
	}
	return chain
}

// loadWebSearchConfig merges the optional configuration file with the bridge
// environment. The file wins for every key it sets.
func loadWebSearchConfig() (webSearchConfig, error) {
	values := map[string]string{}
	if path := strings.TrimSpace(os.Getenv(webSearchConfigEnv)); path != "" {
		var err error
		if values, err = readWebSearchConfigFile(path); err != nil {
			return webSearchConfig{}, err
		}
	}
	if values["DEEPSEEK_API_KEY"] == "" {
		values["DEEPSEEK_API_KEY"] = os.Getenv("DEEPSEEK_API_KEY")
	}
	config := webSearchConfig{
		DeepSeekKey: values["DEEPSEEK_API_KEY"],
		AccountID:   values["CLOUDFLARE_ACCOUNT_ID"],
		Token:       values["CLOUDFLARE_API_TOKEN"],
		GatewayID:   values["CLOUDFLARE_AI_GATEWAY_ID"],
		BraveKey:    values["BRAVE_API_KEY"],
		Chains:      map[webSearchScenario][]string{},
		Prices:      map[string]float64{},
	}
	if config.GatewayID == "" {
		config.GatewayID = "default"
	}
	// WEB_SEARCH_PROVIDERS pinned every conversation to Exa/Brave/Linkup, which
	// left them without search once those accounts ran out of credit. It is no
	// longer read: the default is DeepSeek and SEARCH_PROVIDERS changes it.
	if legacy := strings.TrimSpace(values["WEB_SEARCH_PROVIDERS"]); legacy != "" {
		legacyWebSearchKeyWarning.Do(func() {
			slog.Warn("codex: WEB_SEARCH_PROVIDERS is no longer read; set SEARCH_PROVIDERS to change the provider order",
				"ignored", legacy)
		})
	}
	shared := parseWebSearchChain(values["SEARCH_PROVIDERS"])
	for _, scenario := range webSearchScenarios {
		chain := parseWebSearchChain(values["SEARCH_PROVIDERS_"+strings.ToUpper(string(scenario))])
		if len(chain) == 0 && scenario != webSearchScenarioFirstVisitSupplement {
			chain = shared
		}
		if len(chain) > 0 {
			config.Chains[scenario] = chain
		}
	}
	for key, value := range values {
		provider, ok := strings.CutPrefix(key, "SEARCH_PRICE_USD_")
		if !ok {
			continue
		}
		price, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
		if err != nil || price < 0 {
			return webSearchConfig{}, fmt.Errorf("web search configuration: %s must be a non-negative number", key)
		}
		config.Prices[strings.ToLower(provider)] = price
	}
	return config, nil
}

// errWebSearchUnconfigured means no provider in the scenario's chain can be
// called with the current configuration.
var errWebSearchUnconfigured = errors.New("web search configuration has no credentials for its providers")

// usableChain returns the providers of scenario that can be called now.
func (c webSearchConfig) usableChain(scenario webSearchScenario, firstVisitAuthorized bool) []string {
	var usable []string
	for _, provider := range c.chain(scenario) {
		if provider == webSearchProviderAIsa && !firstVisitAuthorized {
			continue
		}
		if c.hasCredentials(provider) {
			usable = append(usable, provider)
		}
	}
	return usable
}
