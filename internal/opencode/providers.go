package opencode

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// providerV1 is the V1 provider.<id> entry
// (packages/core/src/v1/config/provider.ts).
type providerV1 struct {
	API       *string                    `json:"api"`
	Name      *string                    `json:"name"`
	Env       []string                   `json:"env"`
	NPM       *string                    `json:"npm"`
	Whitelist []string                   `json:"whitelist"`
	Blacklist []string                   `json:"blacklist"`
	Options   map[string]json.RawMessage `json:"options"`
	Models    map[string]json.RawMessage `json:"models"`
}

type modelV1 struct {
	ID         *string `json:"id"`
	Name       *string `json:"name"`
	Family     *string `json:"family"`
	Attachment *bool   `json:"attachment"`
	Reasoning  *bool   `json:"reasoning"`
	ToolCall   *bool   `json:"tool_call"`
	Cost       *struct {
		Input      float64  `json:"input"`
		Output     float64  `json:"output"`
		CacheRead  *float64 `json:"cache_read"`
		CacheWrite *float64 `json:"cache_write"`
	} `json:"cost"`
	Limit *struct {
		Context float64  `json:"context"`
		Input   *float64 `json:"input"`
		Output  float64  `json:"output"`
	} `json:"limit"`
	Modalities *struct {
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"modalities"`
	Status   *string `json:"status"`
	Provider *struct {
		NPM *string `json:"npm"`
		API *string `json:"api"`
	} `json:"provider"`
	Headers map[string]string `json:"headers"`
}

// providerV2 is the V2 providers.<id> entry
// (packages/schema/src/config/provider.ts).
type providerV2 struct {
	Name     *string                    `json:"name"`
	Env      []string                   `json:"env"`
	Package  *string                    `json:"package"`
	Settings map[string]json.RawMessage `json:"settings"`
	Headers  map[string]string          `json:"headers"`
	Models   map[string]json.RawMessage `json:"models"`
}

type costV2 struct {
	Tier   json.RawMessage `json:"tier"`
	Input  float64         `json:"input"`
	Output float64         `json:"output"`
	Cache  *struct {
		Read  *float64 `json:"read"`
		Write *float64 `json:"write"`
	} `json:"cache"`
}

type modelV2 struct {
	ModelID      *string                    `json:"modelID"`
	Family       *string                    `json:"family"`
	Name         *string                    `json:"name"`
	Package      *string                    `json:"package"`
	Settings     map[string]json.RawMessage `json:"settings"`
	Headers      map[string]string          `json:"headers"`
	Capabilities *struct {
		Tools  *bool    `json:"tools"`
		Input  []string `json:"input"`
		Output []string `json:"output"`
	} `json:"capabilities"`
	Cost  json.RawMessage `json:"cost"`
	Limit *struct {
		Context *float64 `json:"context"`
		Input   *float64 `json:"input"`
		Output  *float64 `json:"output"`
	} `json:"limit"`
	Disabled *bool `json:"disabled"`
}

// applyProviders merges the V1 provider and V2 providers maps of one
// document. Within a document V2 entries are applied after V1 ones.
// Providers and their models merge field by field; headers merge key by
// key.
func (m *merger) applyProviders(doc document, source string) {
	if raw, ok := doc["provider"]; ok {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
			m.warnf("%s: provider: expected an object", source)
		}
		for _, id := range slices.Sorted(maps.Keys(entries)) {
			var p providerV1
			if err := json.Unmarshal(entries[id], &p); err != nil {
				m.warnf("%s: provider.%s: %v", source, id, err)
				continue
			}
			m.mergeProvider(id, p.toPatch(id, source, m))
		}
	}
	if raw, ok := doc["providers"]; ok {
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil || entries == nil {
			m.warnf("%s: providers: expected an object", source)
		}
		for _, id := range slices.Sorted(maps.Keys(entries)) {
			var p providerV2
			if err := json.Unmarshal(entries[id], &p); err != nil {
				m.warnf("%s: providers.%s: %v", source, id, err)
				continue
			}
			m.mergeProvider(id, p.toPatch(id, source, m))
		}
	}
}

// providerPatch is one document's view of a provider; nil means unset.
type providerPatch struct {
	name, npm, baseURL, apiKey *string
	env, whitelist, blacklist  []string
	headers                    map[string]string
	models                     map[string]modelPatch
}

type modelPatch struct {
	modelID, name, family, npm, baseURL *string
	limitContext, limitInput, limitOut  *int64
	cost                                *Cost
	toolCall, reasoning, attachment     *bool
	input, output                       []string
	disabled                            *bool
	headers                             map[string]string
}

func (p providerV1) toPatch(id, source string, m *merger) providerPatch {
	patch := providerPatch{
		name:      p.Name,
		npm:       p.NPM,
		env:       p.Env,
		whitelist: p.Whitelist,
		blacklist: p.Blacklist,
	}
	if s, ok := stringSetting(p.Options, "apiKey"); ok {
		patch.apiKey = &s
	}
	if s, ok := stringSetting(p.Options, "baseURL"); ok {
		patch.baseURL = &s
	}
	if p.API != nil && *p.API != "" {
		patch.baseURL = p.API
	}
	patch.headers = stringMapSetting(p.Options, "headers")
	if len(p.Models) > 0 {
		patch.models = map[string]modelPatch{}
	}
	for _, modelID := range slices.Sorted(maps.Keys(p.Models)) {
		var mv modelV1
		if err := json.Unmarshal(p.Models[modelID], &mv); err != nil {
			m.warnf("%s: provider.%s.models.%s: %v", source, id, modelID, err)
			continue
		}
		mp := modelPatch{
			modelID:    mv.ID,
			name:       mv.Name,
			family:     mv.Family,
			toolCall:   mv.ToolCall,
			reasoning:  mv.Reasoning,
			attachment: mv.Attachment,
			headers:    mv.Headers,
		}
		if mv.Provider != nil {
			mp.npm = mv.Provider.NPM
			mp.baseURL = mv.Provider.API
		}
		if mv.Cost != nil {
			mp.cost = &Cost{
				Input:      mv.Cost.Input,
				Output:     mv.Cost.Output,
				CacheRead:  deref(mv.Cost.CacheRead),
				CacheWrite: deref(mv.Cost.CacheWrite),
			}
		}
		if mv.Limit != nil {
			mp.limitContext = toInt64(&mv.Limit.Context)
			mp.limitInput = toInt64(mv.Limit.Input)
			mp.limitOut = toInt64(&mv.Limit.Output)
		}
		if mv.Modalities != nil {
			mp.input = mv.Modalities.Input
			mp.output = mv.Modalities.Output
		}
		if mv.Status != nil && *mv.Status == "deprecated" {
			t := true
			mp.disabled = &t
		}
		patch.models[modelID] = mp
	}
	return patch
}

func (p providerV2) toPatch(id, source string, m *merger) providerPatch {
	patch := providerPatch{
		name:    p.Name,
		env:     p.Env,
		headers: p.Headers,
	}
	if p.Package != nil {
		npm := strings.TrimPrefix(*p.Package, "aisdk:")
		patch.npm = &npm
	}
	if s, ok := stringSetting(p.Settings, "apiKey"); ok {
		patch.apiKey = &s
	}
	if s, ok := stringSetting(p.Settings, "baseURL"); ok {
		patch.baseURL = &s
	}
	if len(p.Models) > 0 {
		patch.models = map[string]modelPatch{}
	}
	for _, modelID := range slices.Sorted(maps.Keys(p.Models)) {
		var mv modelV2
		if err := json.Unmarshal(p.Models[modelID], &mv); err != nil {
			m.warnf("%s: providers.%s.models.%s: %v", source, id, modelID, err)
			continue
		}
		mp := modelPatch{
			modelID:  mv.ModelID,
			name:     mv.Name,
			family:   mv.Family,
			disabled: mv.Disabled,
			headers:  mv.Headers,
		}
		if mv.Package != nil {
			npm := strings.TrimPrefix(*mv.Package, "aisdk:")
			mp.npm = &npm
		}
		if s, ok := stringSetting(mv.Settings, "baseURL"); ok {
			mp.baseURL = &s
		}
		if mv.Capabilities != nil {
			mp.toolCall = mv.Capabilities.Tools
			mp.input = mv.Capabilities.Input
			mp.output = mv.Capabilities.Output
		}
		if mv.Limit != nil {
			mp.limitContext = toInt64(mv.Limit.Context)
			mp.limitInput = toInt64(mv.Limit.Input)
			mp.limitOut = toInt64(mv.Limit.Output)
		}
		if len(mv.Cost) > 0 {
			cost, err := decodeCostV2(mv.Cost)
			if err != nil {
				m.warnf("%s: providers.%s.models.%s.cost: %v", source, id, modelID, err)
			} else {
				mp.cost = cost
			}
		}
		patch.models[modelID] = mp
	}
	return patch
}

// decodeCostV2 accepts a Cost or a list of tiers and returns the base
// (untiered) entry.
func decodeCostV2(raw json.RawMessage) (*Cost, error) {
	var list []costV2
	if err := json.Unmarshal(raw, &list); err != nil {
		var one costV2
		if err := json.Unmarshal(raw, &one); err != nil {
			return nil, err
		}
		list = []costV2{one}
	}
	if len(list) == 0 {
		return nil, nil
	}
	base := list[0]
	for _, c := range list {
		if len(c.Tier) == 0 || string(c.Tier) == "null" {
			base = c
			break
		}
	}
	cost := &Cost{Input: base.Input, Output: base.Output}
	if base.Cache != nil {
		cost.CacheRead = deref(base.Cache.Read)
		cost.CacheWrite = deref(base.Cache.Write)
	}
	return cost, nil
}

func (m *merger) mergeProvider(id string, patch providerPatch) {
	p := m.cfg.Providers[id]
	p.ID = id
	setIf(&p.Name, patch.name)
	setIf(&p.NPM, patch.npm)
	setIf(&p.BaseURL, patch.baseURL)
	setIf(&p.APIKey, patch.apiKey)
	if patch.env != nil {
		p.Env = slices.Clone(patch.env)
	}
	if patch.whitelist != nil {
		p.Whitelist = slices.Clone(patch.whitelist)
	}
	if patch.blacklist != nil {
		p.Blacklist = slices.Clone(patch.blacklist)
	}
	p.Headers = mergeHeaders(p.Headers, patch.headers)
	for _, modelID := range slices.Sorted(maps.Keys(patch.models)) {
		mp := patch.models[modelID]
		if p.Models == nil {
			p.Models = map[string]Model{}
		}
		model := p.Models[modelID]
		model.ID = modelID
		setIf(&model.ModelID, mp.modelID)
		setIf(&model.Name, mp.name)
		setIf(&model.Family, mp.family)
		setIf(&model.NPM, mp.npm)
		setIf(&model.BaseURL, mp.baseURL)
		setIf(&model.Limit.Context, mp.limitContext)
		setIf(&model.Limit.Input, mp.limitInput)
		setIf(&model.Limit.Output, mp.limitOut)
		if mp.cost != nil {
			c := *mp.cost
			model.Cost = &c
		}
		setPtrIf(&model.ToolCall, mp.toolCall)
		setPtrIf(&model.Reasoning, mp.reasoning)
		setPtrIf(&model.Attachment, mp.attachment)
		setPtrIf(&model.Disabled, mp.disabled)
		if mp.input != nil {
			model.InputModalities = slices.Clone(mp.input)
		}
		if mp.output != nil {
			model.OutputModalities = slices.Clone(mp.output)
		}
		model.Headers = mergeHeaders(model.Headers, mp.headers)
		p.Models[modelID] = model
	}
	m.cfg.Providers[id] = p
}

func mergeHeaders(dst, src map[string]string) map[string]string {
	if len(src) == 0 {
		return dst
	}
	out := maps.Clone(dst)
	if out == nil {
		out = make(map[string]string, len(src))
	}
	maps.Copy(out, src)
	return out
}

func stringSetting(settings map[string]json.RawMessage, key string) (string, bool) {
	raw, ok := settings[key]
	if !ok {
		return "", false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", false
	}
	return s, true
}

// stringMapSetting returns the string values of an object setting,
// dropping non-string values like OpenCode does for V1 headers.
func stringMapSetting(settings map[string]json.RawMessage, key string) map[string]string {
	raw, ok := settings[key]
	if !ok {
		return nil
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for k, v := range values {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func setIf[T any](dst *T, src *T) {
	if src != nil {
		*dst = *src
	}
}

func setPtrIf[T any](dst **T, src *T) {
	if src != nil {
		v := *src
		*dst = &v
	}
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

func toInt64(f *float64) *int64 {
	if f == nil {
		return nil
	}
	v := int64(*f)
	return &v
}
