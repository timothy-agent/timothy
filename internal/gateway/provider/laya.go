package provider

import "context"

// DefaultLayaModel is the default English typed-decisions checkpoint (D-163).
const DefaultLayaModel = "typed-decisions"

var layaPreset = systemonePreset{
	driver:     "laya",
	listModels: layaModels,
}

// NewLaya builds a laya-serve driver; it performs no I/O.
func NewLaya(cfg SystemOneConfig) *SystemOne {
	return newSystemOne(layaPreset, cfg)
}

// layaModels reads the loaded checkpoints from GET /health.
func layaModels(ctx context.Context, s *SystemOne) ([]AvailableModel, error) {
	var out struct {
		Loaded []string `json:"loaded"`
	}
	if err := s.getJSON(ctx, s.rootURL()+"/health", &out); err != nil {
		return nil, err
	}
	if len(out.Loaded) == 0 {
		return []AvailableModel{{ID: DefaultLayaModel}}, nil
	}
	models := make([]AvailableModel, 0, len(out.Loaded))
	for _, id := range out.Loaded {
		models = append(models, AvailableModel{ID: id})
	}
	return models, nil
}
