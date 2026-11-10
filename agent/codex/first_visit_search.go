package codex

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

// Pin the existing capability file (including broker revision) across quote,
// execution and read. Rotating the session cannot authorize a different task.
func (s *appServerSession) supplementFirstVisitSearch(query string, limit int) (*webSearchResponse, error) {
	script := s.webReaderScript("tomako-external-data.mjs")
	s.runtimeMu.RLock()
	taskID := s.runtime.TaskID
	authorized := (s.runtime.FirstVisitSearch || s.runtime.ResearchSearchSupplement) &&
		s.runtime.ToolProfile == core.ToolProfilePublicResearch && taskID != ""
	requestPrefix := "first-search-"
	if !s.runtime.FirstVisitSearch {
		requestPrefix = "research-search-"
	}
	var authority []byte
	var err error
	if authorized {
		authority, err = os.ReadFile(s.taskRuntimeEnvFile)
		if broker := s.capabilityBroker; err == nil && broker != nil {
			// This bridge-owned process is outside the model sandbox. Address
			// the same Unix broker directly; each request still requires its
			// pinned revision and Portal capability. Never use raw auth here.
			if !bytes.Contains(authority, []byte("export TOMAKO_CAPABILITY_SOCKET="+shellSingleQuote(broker.socket)+"\n")) ||
				!bytes.Contains(authority, []byte("export TOMAKO_CAPABILITY_REVISION="+shellSingleQuote(broker.revision)+"\n")) || broker.revision == "" {
				err = errors.New("missing current broker revision")
			} else {
				authority = bytes.Replace(authority, []byte("export TOMAKO_CAPABILITY_PROXY='1'\n"), nil, 1)
			}
		}
	}
	s.runtimeMu.RUnlock()
	if script == "" || !authorized || err != nil || len(authority) == 0 {
		return nil, attemptError(webSearchProviderAIsa, webSearchNotConfigured, 0, "supplemental search has no current task authority")
	}
	path, err := writeTaskRuntimeEnvInDir("", string(authority), os.TempDir())
	if err != nil {
		return nil, err
	}
	defer removeTaskRuntimeEnv(path)
	ctx, cancel := context.WithTimeout(s.ctx, 50*time.Second)
	defer cancel()
	env := core.MergeEnv(core.MergeEnv(os.Environ(), s.extraEnv), []string{"TOMAKO_TASK_ENV_FILE=" + path})
	call := func(action string, input map[string]any, result any) error {
		payload, err := json.Marshal(input)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, "node", script, action, "-")
		cmd.Dir = filepath.Dir(script)
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(payload)
		cmd.WaitDelay = time.Second
		raw, err := cmd.Output()
		if err != nil {
			return aisaScriptError(action, err)
		}
		if err = json.Unmarshal(raw, result); err != nil {
			return fmt.Errorf("AIsa %s returned an unreadable receipt", action)
		}
		return nil
	}
	args := map[string]any{"body": map[string]any{"query": query, "max_results": limit, "search_depth": "basic"}}
	input := map[string]any{"operationId": "post_tavily_search", "arguments": args}
	var quote struct {
		ContractRevision string
		Price            struct {
			Currency string
			Amount   json.Number `json:"amount_usd"`
		}
	}
	if err = call("describe", input, &quote); err != nil {
		return nil, err
	}
	amount, err := quote.Price.Amount.Float64()
	if err != nil || amount <= 0 || quote.Price.Currency != "USD" || quote.ContractRevision == "" {
		return nil, attemptError(webSearchProviderAIsa, webSearchUnavailable, 0, "AIsa did not return a usable search quote")
	}
	input["contractRevision"] = quote.ContractRevision
	input["maxPriceUsd"] = quote.Price.Amount
	input["requestId"] = fmt.Sprintf(requestPrefix+"%x", sha256.Sum256([]byte(fmt.Sprintf("%s\n%s\n%d", taskID, strings.TrimSpace(query), limit))))
	var receipt struct{ ResultID, Status string }
	if err = call("execute", input, &receipt); err != nil {
		return nil, err
	}
	if receipt.Status != "SUCCEEDED" || receipt.ResultID == "" {
		return nil, fmt.Errorf("AIsa search did not complete (status %s)", receipt.Status)
	}
	var page struct {
		Status string
		Data   *[]struct{ URL, Title, Content string }
	}
	if err = call("read", map[string]any{"resultId": receipt.ResultID, "pointer": "/data/results", "limit": limit}, &page); err != nil {
		return nil, err
	}
	if page.Status != "SUCCEEDED" || page.Data == nil {
		return nil, errors.New("AIsa search has no readable result list")
	}
	response := &webSearchResponse{}
	for _, item := range *page.Data {
		if item.URL != "" {
			response.Items = append(response.Items, webSearchItem{item.URL, item.Title, item.Content})
		}
	}
	return response, nil
}

// aisaStatus is the HTTP status the external-data client prints for a refused
// Portal request, e.g. "第三方数据请求失败 (503): AIsa MCP 工具执行错误".
var aisaStatus = regexp.MustCompile(`\((\d{3})\)`)

// aisaScriptError keeps the client's own reason (Portal's fixed copy) so the
// bridge log, the Agent and the search ledger say why AIsa failed instead of
// only "exit status 1", and classifies it by the reported HTTP status.
func aisaScriptError(action string, err error) *webSearchAttemptError {
	reason := ""
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		reason = limitRunes(strings.Join(strings.Fields(string(exit.Stderr)), " "), 240)
	}
	if reason == "" {
		return attemptError(webSearchProviderAIsa, webSearchUnavailable, 0, "AIsa %s failed through the task capability: %v", action, err)
	}
	category, status := webSearchUnavailable, 0
	if match := aisaStatus.FindStringSubmatch(reason); match != nil {
		status, _ = strconv.Atoi(match[1])
		category = webSearchHTTPCategory(status)
	}
	return attemptError(webSearchProviderAIsa, category, status, "AIsa %s failed through the task capability: %s", action, reason)
}
