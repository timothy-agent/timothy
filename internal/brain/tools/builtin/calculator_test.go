package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCalculator(t *testing.T) {
	t.Parallel()
	tool := Calculator()

	tests := []struct {
		expr    string
		want    string
		wantErr string
	}{
		{expr: "19*23", want: "437"},
		{expr: "2 + 3 * 4", want: "14"},
		{expr: "(2 + 3) * 4", want: "20"},
		{expr: "10 / 4", want: "2.5"},
		{expr: "2 ^ 10", want: "1024"},
		{expr: "2 ^ 3 ^ 2", want: "512"}, // right-associative
		{expr: "-3 + 5", want: "2"},
		{expr: "10 % 3", want: "1"},
		{expr: "-(2 + 3)", want: "-5"},
		{expr: "3.5 * 2", want: "7"},
		{expr: "80 * 0.15", want: "12"},
		{expr: "1 / 0", wantErr: "division by zero"},
		{expr: "5 % 0", wantErr: "remainder by zero"},
		{expr: "2 +", wantErr: "unexpected end"},
		{expr: "(1 + 2", wantErr: "closing parenthesis"},
		{expr: "1..2", wantErr: "malformed number"},
		{expr: "two + 2", wantErr: "unexpected"},
		{expr: "2; DROP TABLE", wantErr: "unexpected"},
		{expr: "10 ^ 400", wantErr: "out of range"},
	}
	for _, tc := range tests {
		t.Run(tc.expr, func(t *testing.T) {
			t.Parallel()
			args, _ := json.Marshal(map[string]string{"expression": tc.expr})
			got, err := tool.Execute(context.Background(), args)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if got != tc.want {
				t.Fatalf("= %q, want %q", got, tc.want)
			}
		})
	}
}

func FuzzEvalExpr(f *testing.F) {
	for _, seed := range []string{"1+2", "(3*4)^2", "-1.5/0.5", "1..", "((", "%", "1e10"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, expr string) {
		// Must never panic; errors are fine.
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", expr, r)
			}
		}()
		v, err := evalExpr(expr)
		if err == nil {
			_ = fmt.Sprintf("%v", v)
		}
	})
}
