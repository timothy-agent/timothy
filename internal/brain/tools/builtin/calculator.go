package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/SumonMSelim/timothy/internal/brain/tools"
)

type calculateArgs struct {
	Expression string `json:"expression"`
}

func Calculator() *tools.Tool {
	return &tools.Tool{
		Name: "calculate",
		Description: `Evaluates an arithmetic expression exactly.

Use for any arithmetic the answer depends on — never compute numbers
in your head when precision matters (prices, conversions, percentages,
large multiplications).

Arguments:
- expression (string, required): plain arithmetic using numbers and
  the operators + - * / % ^ with parentheses. Decimal numbers use a
  dot (3.14). ^ is exponentiation, % is remainder. No variables,
  functions, or units — pre-resolve those yourself ("15% of 80" →
  "80 * 0.15").

Edge cases: division or remainder by zero is an error; results too
large for a float64 are an error.

Example: {"expression": "19*23"} → "437"`,
		InputSchema: json.RawMessage(`{
			"type": "object",
			"properties": {
				"expression": {
					"type": "string",
					"description": "Arithmetic expression, e.g. (2 + 3.5) * 4 ^ 2"
				}
			},
			"required": ["expression"],
			"additionalProperties": false
		}`),
		Execute: func(_ context.Context, raw json.RawMessage) (string, error) {
			var args calculateArgs
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("invalid arguments: %w", err)
			}
			v, err := evalExpr(args.Expression)
			if err != nil {
				return "", err
			}
			return formatNumber(v), nil
		},
	}
}

func formatNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// evalExpr parses and evaluates an arithmetic expression with a small
// recursive-descent parser — no code evaluation, no identifiers.
//
//	expr  := term (('+'|'-') term)*
//	term  := unary (('*'|'/'|'%') unary)*
//	unary := '-' unary | power
//	power := atom ('^' unary)?          right-associative
//	atom  := number | '(' expr ')'
func evalExpr(input string) (float64, error) {
	p := &exprParser{src: input}
	v, err := p.expr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		return 0, fmt.Errorf("unexpected %q at position %d", p.src[p.pos], p.pos)
	}
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0, fmt.Errorf("result out of range")
	}
	return v, nil
}

type exprParser struct {
	src string
	pos int
}

func (p *exprParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *exprParser) peek() (byte, bool) {
	p.skipSpace()
	if p.pos >= len(p.src) {
		return 0, false
	}
	return p.src[p.pos], true
}

func (p *exprParser) expr() (float64, error) {
	v, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		c, ok := p.peek()
		if !ok || (c != '+' && c != '-') {
			return v, nil
		}
		p.pos++
		rhs, err := p.term()
		if err != nil {
			return 0, err
		}
		if c == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

func (p *exprParser) term() (float64, error) {
	v, err := p.unary()
	if err != nil {
		return 0, err
	}
	for {
		c, ok := p.peek()
		if !ok || (c != '*' && c != '/' && c != '%') {
			return v, nil
		}
		p.pos++
		rhs, err := p.unary()
		if err != nil {
			return 0, err
		}
		switch c {
		case '*':
			v *= rhs
		case '/':
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		case '%':
			if rhs == 0 {
				return 0, fmt.Errorf("remainder by zero")
			}
			v = math.Mod(v, rhs)
		}
	}
}

func (p *exprParser) unary() (float64, error) {
	if c, ok := p.peek(); ok && c == '-' {
		p.pos++
		v, err := p.unary()
		return -v, err
	}
	return p.power()
}

func (p *exprParser) power() (float64, error) {
	v, err := p.atom()
	if err != nil {
		return 0, err
	}
	if c, ok := p.peek(); ok && c == '^' {
		p.pos++
		exp, err := p.unary()
		if err != nil {
			return 0, err
		}
		return math.Pow(v, exp), nil
	}
	return v, nil
}

func (p *exprParser) atom() (float64, error) {
	c, ok := p.peek()
	if !ok {
		return 0, fmt.Errorf("unexpected end of expression")
	}
	if c == '(' {
		p.pos++
		v, err := p.expr()
		if err != nil {
			return 0, err
		}
		if c, ok := p.peek(); !ok || c != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return v, nil
	}
	start := p.pos
	for p.pos < len(p.src) && (isDigit(p.src[p.pos]) || p.src[p.pos] == '.') {
		p.pos++
	}
	if p.pos == start {
		return 0, fmt.Errorf("unexpected %q at position %d: only numbers, + - * / %% ^ and parentheses are supported", c, p.pos)
	}
	lit := p.src[start:p.pos]
	if strings.Count(lit, ".") > 1 {
		return 0, fmt.Errorf("malformed number %q", lit)
	}
	v, err := strconv.ParseFloat(lit, 64)
	if err != nil {
		return 0, fmt.Errorf("malformed number %q", lit)
	}
	return v, nil
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }
