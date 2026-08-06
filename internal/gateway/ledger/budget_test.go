package ledger

import (
	"strings"
	"testing"
)

// Validation rejects bad input before any database access, so a
// zero-value store is enough here.
func TestBudgetSetValidation(t *testing.T) {
	t.Parallel()
	s := &BudgetStore{}
	neg := &BudgetLimit{Amount: -5.0, Currency: "USD"}
	zero := &BudgetLimit{Amount: 0.0, Currency: "USD"}

	if err := s.Set(t.Context(), "week", nil); err == nil || !strings.Contains(err.Error(), "unknown budget scope") {
		t.Fatalf("unknown scope: err = %v", err)
	}
	if err := s.Set(t.Context(), "day", neg); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("negative limit: err = %v", err)
	}
	if err := s.Set(t.Context(), "month", zero); err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("zero limit: err = %v", err)
	}
}
