package provider

import "context"

// DefaultJevModel is the default TypeSafe Jev decision model (D-163).
const DefaultJevModel = "jev-latest"

// DefaultJevBaseURL is the default TypeSafe API endpoint base.
const DefaultJevBaseURL = "https://api.typesafe.ai"

var jevPreset = systemonePreset{
	driver:         "jev",
	defaultBaseURL: DefaultJevBaseURL,
	defaultModel:   DefaultJevModel,
	listModels:     jevModels,
}

// NewJev builds a TypeSafe Jev driver; it performs no I/O.
func NewJev(cfg SystemOneConfig) *SystemOne {
	return newSystemOne(jevPreset, cfg)
}

// jevModels reads GET /v1/models, which answers {"models": [{"name",
// "description"}]} or the OpenAI {"data": [{"id"}]} envelope.
func jevModels(ctx context.Context, s *SystemOne) ([]AvailableModel, error) {
	var out struct {
		Models []struct {
			Name        string `json:"name"`
			ID          string `json:"id"`
			Description string `json:"description"`
		} `json:"models"`
		Data []AvailableModel `json:"data"`
	}
	if err := s.getJSON(ctx, s.rootURL()+"/v1/models", &out); err != nil {
		return nil, err
	}
	res := make([]AvailableModel, 0, len(out.Models))
	for _, m := range out.Models {
		id := m.Name
		if id == "" {
			id = m.ID
		}
		if id != "" {
			res = append(res, AvailableModel{ID: id, DisplayName: m.Description})
		}
	}
	if len(res) == 0 && len(out.Data) > 0 {
		res = out.Data
	}
	return res, nil
}
