package missions

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// baseValidMission returns a Mission that passes ValidateCreate as-is —
// each table test case mutates a copy of this to isolate the one rule
// under test.
func baseValidMission() Mission {
	return Mission{Kind: "general", Route: "default"}
}

func TestValidateCreate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(m Mission) Mission
		deps    ValidateDeps
		wantErr bool
	}{
		{"valid general mission", func(m Mission) Mission { return m }, ValidateDeps{}, false},
		{"valid coding mission", func(m Mission) Mission {
			m.Kind = "coding"
			return m
		}, ValidateDeps{}, false},
		{"unknown kind", func(m Mission) Mission {
			m.Kind = "bogus"
			return m
		}, ValidateDeps{}, true},
		{"empty kind", func(m Mission) Mission {
			m.Kind = ""
			return m
		}, ValidateDeps{}, true},
		{"light on coding", func(m Mission) Mission {
			m.Kind, m.Light = "coding", true
			return m
		}, ValidateDeps{}, true},
		{"light on general", func(m Mission) Mission {
			m.Light = true
			return m
		}, ValidateDeps{}, false},
		{"harness on general", func(m Mission) Mission {
			m.Harness = "claude-cli"
			return m
		}, ValidateDeps{}, true},
		{"unknown harness on coding", func(m Mission) Mission {
			m.Kind, m.Harness = "coding", "not-a-real-harness"
			return m
		}, ValidateDeps{}, true},
		{"environment on general", func(m Mission) Mission {
			m.Environment = "go"
			return m
		}, ValidateDeps{}, true},
		{"unknown environment on coding", func(m Mission) Mission {
			m.Kind, m.Environment = "coding", "bogus"
			return m
		}, ValidateDeps{}, true},
		{"valid environment on coding", func(m Mission) Mission {
			m.Kind, m.Environment = "coding", "go"
			return m
		}, ValidateDeps{}, false},
		{"repo_url on general", func(m Mission) Mission {
			m.Kind, m.RepoURL = "general", "https://github.com/o/r"
			return m
		}, ValidateDeps{}, true},
		{"repo_url without connector_id", func(m Mission) Mission {
			m.Kind, m.RepoURL = "coding", "https://github.com/o/r"
			return m
		}, ValidateDeps{}, true},
		{"connector_id without repo_url", func(m Mission) Mission {
			m.Kind, m.ConnectorID = "coding", "conn-1"
			return m
		}, ValidateDeps{}, true},
		{"branch_pattern on general", func(m Mission) Mission {
			m.BranchPattern = "{type}/{slug}"
			return m
		}, ValidateDeps{}, true},
		{"commit_style on general", func(m Mission) Mission {
			m.CommitStyle = CommitStylePlain
			return m
		}, ValidateDeps{}, true},
		{"on_complete on general", func(m Mission) Mission {
			m.OnComplete = "push"
			return m
		}, ValidateDeps{}, true},
		{"invalid branch_pattern on coding", func(m Mission) Mission {
			m.Kind, m.BranchPattern = "coding", "{oops}/{slug}"
			return m
		}, ValidateDeps{}, true},
		{"valid branch_pattern on coding", func(m Mission) Mission {
			m.Kind, m.BranchPattern = "coding", "{type}/{slug}"
			return m
		}, ValidateDeps{}, false},
		{"invalid commit_style on coding", func(m Mission) Mission {
			m.Kind, m.CommitStyle = "coding", "loud"
			return m
		}, ValidateDeps{}, true},
		{"valid commit_style on coding", func(m Mission) Mission {
			m.Kind, m.CommitStyle = "coding", CommitStylePlain
			return m
		}, ValidateDeps{}, false},
		{"on_complete unknown value", func(m Mission) Mission {
			m.Kind, m.OnComplete = "coding", "bogus"
			return m
		}, ValidateDeps{}, true},
		{"on_complete push without repo/connector", func(m Mission) Mission {
			m.Kind, m.OnComplete = "coding", "push"
			return m
		}, ValidateDeps{}, true},
		{"on_complete push with repo/connector", func(m Mission) Mission {
			m.Kind, m.OnComplete = "coding", "push"
			m.RepoURL, m.ConnectorID = "https://github.com/o/r", "conn-1"
			return m
		}, ValidateDeps{}, false},
		{"empty route", func(m Mission) Mission {
			m.Route = ""
			return m
		}, ValidateDeps{}, true},
		{"empty model pins accepted", func(m Mission) Mission {
			return m
		}, ValidateDeps{}, false},
		{"well-formed route_model accepted", func(m Mission) Mission {
			m.RouteModel = "OpenAI/gpt-5-mini"
			return m
		}, ValidateDeps{}, false},
		{"well-formed plan_route_model accepted", func(m Mission) Mission {
			m.PlanRouteModel = "GLM (Z.ai)/glm-5.3"
			return m
		}, ValidateDeps{}, false},
		{"well-formed review_route_model accepted", func(m Mission) Mission {
			m.ReviewRouteModel = "Anthropic/claude-sonnet-5"
			return m
		}, ValidateDeps{}, false},
		{"route_model missing a slash is rejected", func(m Mission) Mission {
			m.RouteModel = "gpt-5-mini"
			return m
		}, ValidateDeps{}, true},
		{"route_model missing the model part is rejected", func(m Mission) Mission {
			m.RouteModel = "OpenAI/"
			return m
		}, ValidateDeps{}, true},
		{"route_model missing the provider part is rejected", func(m Mission) Mission {
			m.RouteModel = "/gpt-5-mini"
			return m
		}, ValidateDeps{}, true},
		{"plan_route_model malformed is rejected", func(m Mission) Mission {
			m.PlanRouteModel = "no-slash-here"
			return m
		}, ValidateDeps{}, true},
		{"review_route_model malformed is rejected", func(m Mission) Mission {
			m.ReviewRouteModel = "no-slash-here"
			return m
		}, ValidateDeps{}, true},
		{"route_model not checked against a live chain", func(m Mission) Mission {
			m.RouteModel = "SomeProvider/some-model-not-in-any-chain"
			return m
		}, ValidateDeps{}, false},
		{"destination_ids all enabled", func(m Mission) Mission {
			m.DestinationIDs = []string{"d1", "d2"}
			return m
		}, ValidateDeps{DestinationEnabled: func(ctx context.Context, id string) (bool, error) {
			return true, nil
		}}, false},
		{"destination_ids one disabled", func(m Mission) Mission {
			m.DestinationIDs = []string{"d1", "d2"}
			return m
		}, ValidateDeps{DestinationEnabled: func(ctx context.Context, id string) (bool, error) {
			return id == "d1", nil
		}}, true},
		{"destination_ids lookup error", func(m Mission) Mission {
			m.DestinationIDs = []string{"d1"}
			return m
		}, ValidateDeps{DestinationEnabled: func(ctx context.Context, id string) (bool, error) {
			return false, fmt.Errorf("db unavailable")
		}}, true},
		{"destination_ids unchecked when dep nil", func(m Mission) Mission {
			m.DestinationIDs = []string{"d1"}
			return m
		}, ValidateDeps{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.mutate(baseValidMission())
			err := ValidateCreate(context.Background(), m, tc.deps)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateCreate(%+v) error = %v, wantErr %v", m, err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidMission) {
				t.Fatalf("ValidateCreate error %v does not wrap ErrInvalidMission", err)
			}
		})
	}
}
