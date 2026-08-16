package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Protocol string

const (
	ProtocolChat      Protocol = "chat"
	ProtocolResponses Protocol = "responses"
	ProtocolAnthropic Protocol = "anthropic"
)

func validProtocol(p Protocol) bool {
	return p == ProtocolChat || p == ProtocolResponses || p == ProtocolAnthropic
}

type Tier string

const (
	TierZen Tier = "zen"
	TierGo  Tier = "go"
)

type modelRoute struct {
	ID       string
	Tier     Tier
	Protocol Protocol
}

type modelCatalog struct {
	mu        sync.RWMutex
	zen       map[string]bool
	goModels  map[string]bool
	protocols map[string]Protocol
	updatedAt time.Time
	prefer    Tier
}

type modelCatalogSnapshot struct {
	Zen       int       `json:"zen"`
	Go        int       `json:"go"`
	Total     int       `json:"total"`
	Exposed   int       `json:"exposed"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func newModelCatalog(prefer Tier, overrides map[string]string) *modelCatalog {
	protocols := make(map[string]Protocol, len(overrides))
	for model, protocol := range overrides {
		protocols[model] = Protocol(protocol)
	}
	return &modelCatalog{zen: map[string]bool{}, goModels: map[string]bool{}, protocols: protocols, prefer: prefer}
}

func (c *modelCatalog) Replace(zen, goModels []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if zen != nil {
		c.zen = toSet(zen)
	}
	if goModels != nil {
		c.goModels = toSet(goModels)
	}
	c.updatedAt = time.Now()
}

func (c *modelCatalog) CopyState(source *modelCatalog) {
	if source == nil {
		return
	}
	source.mu.RLock()
	zen := make(map[string]bool, len(source.zen))
	goModels := make(map[string]bool, len(source.goModels))
	for model, available := range source.zen {
		zen[model] = available
	}
	for model, available := range source.goModels {
		goModels[model] = available
	}
	updatedAt := source.updatedAt
	source.mu.RUnlock()
	c.mu.Lock()
	c.zen, c.goModels, c.updatedAt = zen, goModels, updatedAt
	c.mu.Unlock()
}

func (c *modelCatalog) Route(model string, hasZenNodes, hasGoNodes bool) (modelRoute, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	protocol := c.protocols[model]
	if protocol == "" {
		protocol = inferProtocol(model)
	}
	// When a model exists on both tiers, honor the configured priority.
	preferGo := c.prefer == TierGo
	if preferGo && c.goModels[model] && hasGoNodes {
		return modelRoute{ID: model, Tier: TierGo, Protocol: protocol}, nil
	}
	if c.zen[model] && hasZenNodes {
		return modelRoute{ID: model, Tier: TierZen, Protocol: protocol}, nil
	}
	if !preferGo && c.goModels[model] && hasGoNodes {
		return modelRoute{ID: model, Tier: TierGo, Protocol: protocol}, nil
	}
	// Model discovery can temporarily fail. Honor the configured priority.
	if len(c.zen) == 0 && len(c.goModels) == 0 {
		if preferGo && hasGoNodes {
			return modelRoute{ID: model, Tier: TierGo, Protocol: protocol}, nil
		}
		if hasZenNodes {
			return modelRoute{ID: model, Tier: TierZen, Protocol: protocol}, nil
		}
		if hasGoNodes {
			return modelRoute{ID: model, Tier: TierGo, Protocol: protocol}, nil
		}
	}
	return modelRoute{}, fmt.Errorf("model %q is not available in the configured Zen or Go pools", model)
}

func (c *modelCatalog) List() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]bool, len(c.zen)+len(c.goModels))
	for model := range c.zen {
		seen[model] = true
	}
	for model := range c.goModels {
		seen[model] = true
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models
}

func (c *modelCatalog) Snapshot() modelCatalogSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	seen := make(map[string]bool, len(c.zen)+len(c.goModels))
	for model := range c.zen {
		seen[model] = true
	}
	for model := range c.goModels {
		seen[model] = true
	}
	return modelCatalogSnapshot{
		Zen:       len(c.zen),
		Go:        len(c.goModels),
		Total:     len(seen),
		Exposed:   len(seen),
		UpdatedAt: c.updatedAt,
	}
}

func toSet(items []string) map[string]bool {
	out := make(map[string]bool, len(items))
	for _, item := range items {
		out[item] = true
	}
	return out
}

func inferProtocol(model string) Protocol {
	m := strings.ToLower(model)
	for _, prefix := range []string{"claude-", "qwen"} {
		if strings.HasPrefix(m, prefix) {
			return ProtocolAnthropic
		}
	}
	for _, prefix := range []string{"gpt-", "o1", "o3", "o4", "grok-", "muse-"} {
		if strings.HasPrefix(m, prefix) {
			return ProtocolResponses
		}
	}
	return ProtocolChat
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

func fetchModels(ctx context.Context, client *http.Client, baseURL, key string, ids requestIDs) ([]string, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/v1/models", nil)
	if err != nil {
		return nil, 0, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	setOpenCodeIdentityHeaders(req, ids)
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, resp.StatusCode, fmt.Errorf("models endpoint returned HTTP %d", resp.StatusCode)
	}
	var payload modelsResponse
	dec := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := dec.Decode(&payload); err != nil {
		return nil, resp.StatusCode, err
	}
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID != "" {
			models = append(models, item.ID)
		}
	}
	if len(models) == 0 {
		return nil, resp.StatusCode, errors.New("models endpoint returned an empty list")
	}
	return models, resp.StatusCode, nil
}
