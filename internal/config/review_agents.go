package config

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/kunchenguid/no-mistakes/internal/agentcfg"
	"github.com/kunchenguid/no-mistakes/internal/types"
)

const maxReviewAgentJSON = 1024

var reviewModelCredential = regexp.MustCompile(`(?i)^(?:sk-(?:proj-|svcacct-)?[A-Za-z0-9_-]{20,}|(?:sk|rk)_live_[A-Za-z0-9]{16,}|xai-[A-Za-z0-9]{20,}|hf_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|gh[opusr]_[A-Za-z0-9]{20,}|gl(?:pat|oas|dt|rtr?|cbt|ptt|ft|imt|agent|wt|soat|ffct)-[A-Za-z0-9_-]{10,}|xox[abprs]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{35}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})$`)

// ReviewAgent pins one review-loop role to an explicit harness. Empty model or
// effort inherits agent_config for that harness; native argument overrides win.
// A per-run reviewer override uses the same shape and is stored without
// credentials or prompt content.
type ReviewAgent struct {
	Agent  types.AgentName `yaml:"agent" json:"agent"`
	Model  string          `yaml:"model" json:"model,omitempty"`
	Effort agentcfg.Effort `yaml:"effort" json:"effort,omitempty"`
}

// NormalizeReviewAgent validates one explicit review-role selection and returns
// its stable, whitespace-normalized form. Callers use this for both CLI input
// and the durable per-run snapshot so a selection cannot change spelling while
// it is compared or recovered.
func NormalizeReviewAgent(entry ReviewAgent) (ReviewAgent, error) {
	entry.Model = strings.TrimSpace(entry.Model)
	if !agentcfg.Known(entry.Agent) {
		return ReviewAgent{}, fmt.Errorf("reviewer.agent must name an explicit harness, got %q", entry.Agent)
	}
	if entry.Model != "" {
		if err := validateReviewModelID(entry.Model); err != nil {
			return ReviewAgent{}, err
		}
		// Per-run selections cross the push-option and durable-run boundaries.
		// Keep every harness on an identifier-only surface so a URL or
		// credential-shaped model can never be echoed into those logs. Pi has
		// the stronger provider/model contract; other harnesses use catalog IDs
		// that may contain slash-separated provider prefixes.
		if entry.Agent == types.AgentPi {
			profile := agentcfg.PiProfile{Model: entry.Model, Effort: entry.Effort}
			if err := profile.ValidateRequest(); err != nil {
				return ReviewAgent{}, fmt.Errorf("reviewer model: %w", err)
			}
		}
	}
	if err := agentcfg.Validate(entry.Agent, agentcfg.Profile{Model: entry.Model, Effort: entry.Effort}); err != nil {
		return ReviewAgent{}, fmt.Errorf("invalid reviewer selection: %w", err)
	}
	return entry, nil
}

func validateReviewModelID(model string) error {
	if len(model) > 256 {
		return fmt.Errorf("reviewer model must be an identifier")
	}
	for _, part := range strings.Split(model, "/") {
		if part == "" || part == "." || part == ".." || strings.HasPrefix(part, "-") || reviewModelCredential.MatchString(part) {
			return fmt.Errorf("reviewer model must be an identifier")
		}
		for _, c := range part {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._-", c)) {
				return fmt.Errorf("reviewer model must be an identifier")
			}
		}
	}
	return nil
}

func validateReviewAgents(roles map[string]ReviewAgent) error {
	for role, entry := range roles {
		if role != "reviewer" && role != "fixer" {
			return fmt.Errorf("invalid review_agents role %q (valid: reviewer, fixer)", role)
		}
		if !agentcfg.Known(entry.Agent) {
			return fmt.Errorf("review_agents.%s.agent must name an explicit harness, got %q", role, entry.Agent)
		}
		model := strings.TrimSpace(entry.Model)
		if model != "" {
			if err := validateReviewModelID(model); err != nil {
				return fmt.Errorf("invalid review_agents.%s: %w", role, err)
			}
		}
		if err := agentcfg.Validate(entry.Agent, agentcfg.Profile{Model: model, Effort: entry.Effort}); err != nil {
			return fmt.Errorf("invalid review_agents.%s: %w", role, err)
		}
	}
	return nil
}

// MarshalReviewAgent encodes an explicit per-run reviewer selection without
// credentials or prompt material. An empty result means no per-run override.
func MarshalReviewAgent(entry *ReviewAgent) (string, error) {
	if entry == nil {
		return "", nil
	}
	normalized, err := NormalizeReviewAgent(*entry)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("marshal reviewer selection: %w", err)
	}
	if len(data) > maxReviewAgentJSON {
		return "", fmt.Errorf("reviewer selection is too large")
	}
	return string(data), nil
}

// ParseReviewAgentJSON decodes the durable per-run reviewer snapshot. It is
// strict so a corrupt local row cannot silently select a different harness.
func ParseReviewAgentJSON(raw string) (*ReviewAgent, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	if len(raw) > maxReviewAgentJSON {
		return nil, fmt.Errorf("parse reviewer selection: value is too large")
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var entry ReviewAgent
	if err := dec.Decode(&entry); err != nil {
		return nil, fmt.Errorf("parse reviewer selection: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("parse reviewer selection: multiple JSON values")
		}
		return nil, fmt.Errorf("parse reviewer selection: %w", err)
	}
	normalized, err := NormalizeReviewAgent(entry)
	if err != nil {
		return nil, fmt.Errorf("parse reviewer selection: %w", err)
	}
	return &normalized, nil
}

// ReviewAgentsEqual compares canonical explicit selections, treating nil as
// the absence of an override. It is used at launch-receipt boundaries.
func ReviewAgentsEqual(a, b *ReviewAgent) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	na, errA := NormalizeReviewAgent(*a)
	nb, errB := NormalizeReviewAgent(*b)
	return errA == nil && errB == nil && na == nb
}

// ForReviewAgent returns an isolated configuration for a role without mutating
// shared per-harness profiles (both roles may use the same harness).
func (c *Config) ForReviewAgent(entry ReviewAgent) *Config {
	role := *c
	role.Agent = entry.Agent
	role.Agents = []types.AgentName{entry.Agent}
	profile := c.AgentProfileFor(entry.Agent)
	if entry.Model != "" {
		profile.Model = strings.TrimSpace(entry.Model)
	}
	if entry.Effort != "" {
		profile.Effort = entry.Effort
	}
	role.AgentConfig = map[string]agentcfg.Profile{string(entry.Agent): profile}
	role.ReviewAgents = nil
	return &role
}
