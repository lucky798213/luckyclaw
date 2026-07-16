package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

const (
	maxCalculatorExpressionLength = 1024
	maxCalculatorNesting          = 64
)

type calculatorTool struct{}

type calculatorArguments struct {
	Expression string `json:"expression"`
}

type expressionParser struct {
	input []rune
	index int
}

func newCalculatorTool() Tool {
	return &calculatorTool{}
}

func (t *calculatorTool) Definition() provider.Tool {
	return functionDefinition(
		"calculator",
		"Safely evaluate an arithmetic expression using numbers, parentheses, unary signs, and +, -, *, /.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"expression": map[string]any{
					"type":        "string",
					"description": "Arithmetic expression to evaluate.",
				},
			},
			"required":             []string{"expression"},
			"additionalProperties": false,
		},
	)
}

func (t *calculatorTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var arguments calculatorArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	expression := strings.TrimSpace(arguments.Expression)
	if expression == "" {
		return "", fmt.Errorf("expression is required")
	}
	if len([]rune(expression)) > maxCalculatorExpressionLength {
		return "", fmt.Errorf("expression exceeds %d characters", maxCalculatorExpressionLength)
	}
	parser := &expressionParser{input: []rune(expression)}
	value, err := parser.parseExpression(0)
	if err != nil {
		return "", err
	}
	parser.skipWhitespace()
	if parser.index != len(parser.input) {
		return "", fmt.Errorf("unexpected character %q at position %d", parser.input[parser.index], parser.index)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("result is not finite")
	}
	if value == 0 {
		value = 0
	}
	return strconv.FormatFloat(value, 'g', -1, 64), nil
}

func (p *expressionParser) parseExpression(depth int) (float64, error) {
	left, err := p.parseTerm(depth)
	if err != nil {
		return 0, err
	}
	for {
		p.skipWhitespace()
		operator, ok := p.take('+', '-')
		if !ok {
			return left, nil
		}
		right, err := p.parseTerm(depth)
		if err != nil {
			return 0, err
		}
		if operator == '+' {
			left += right
		} else {
			left -= right
		}
		if math.IsInf(left, 0) || math.IsNaN(left) {
			return 0, fmt.Errorf("result is not finite")
		}
	}
}

func (p *expressionParser) parseTerm(depth int) (float64, error) {
	left, err := p.parseUnary(depth)
	if err != nil {
		return 0, err
	}
	for {
		p.skipWhitespace()
		operator, ok := p.take('*', '/')
		if !ok {
			return left, nil
		}
		right, err := p.parseUnary(depth)
		if err != nil {
			return 0, err
		}
		if operator == '*' {
			left *= right
		} else {
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		}
		if math.IsInf(left, 0) || math.IsNaN(left) {
			return 0, fmt.Errorf("result is not finite")
		}
	}
}

func (p *expressionParser) parseUnary(depth int) (float64, error) {
	sign := 1.0
	count := 0
	for {
		p.skipWhitespace()
		operator, ok := p.take('+', '-')
		if !ok {
			break
		}
		count++
		if count > maxCalculatorNesting {
			return 0, fmt.Errorf("too many unary operators")
		}
		if operator == '-' {
			sign = -sign
		}
	}
	value, err := p.parsePrimary(depth)
	if err != nil {
		return 0, err
	}
	return sign * value, nil
}

func (p *expressionParser) parsePrimary(depth int) (float64, error) {
	p.skipWhitespace()
	if p.index >= len(p.input) {
		return 0, fmt.Errorf("expected a number or parenthesis at position %d", p.index)
	}
	if p.input[p.index] == '(' {
		if depth >= maxCalculatorNesting {
			return 0, fmt.Errorf("parenthesis nesting exceeds %d", maxCalculatorNesting)
		}
		p.index++
		value, err := p.parseExpression(depth + 1)
		if err != nil {
			return 0, err
		}
		p.skipWhitespace()
		if p.index >= len(p.input) || p.input[p.index] != ')' {
			return 0, fmt.Errorf("missing closing parenthesis at position %d", p.index)
		}
		p.index++
		return value, nil
	}
	return p.parseNumber()
}

func (p *expressionParser) parseNumber() (float64, error) {
	start := p.index
	digits := 0
	for p.index < len(p.input) && unicode.IsDigit(p.input[p.index]) {
		p.index++
		digits++
	}
	if p.index < len(p.input) && p.input[p.index] == '.' {
		p.index++
		for p.index < len(p.input) && unicode.IsDigit(p.input[p.index]) {
			p.index++
			digits++
		}
	}
	if digits == 0 {
		return 0, fmt.Errorf("expected a number at position %d", start)
	}
	if p.index < len(p.input) && (p.input[p.index] == 'e' || p.input[p.index] == 'E') {
		p.index++
		if p.index < len(p.input) && (p.input[p.index] == '+' || p.input[p.index] == '-') {
			p.index++
		}
		exponentStart := p.index
		for p.index < len(p.input) && unicode.IsDigit(p.input[p.index]) {
			p.index++
		}
		if exponentStart == p.index {
			return 0, fmt.Errorf("invalid exponent at position %d", exponentStart)
		}
	}
	value, err := strconv.ParseFloat(string(p.input[start:p.index]), 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return 0, fmt.Errorf("invalid number %q", string(p.input[start:p.index]))
	}
	return value, nil
}

func (p *expressionParser) skipWhitespace() {
	for p.index < len(p.input) && unicode.IsSpace(p.input[p.index]) {
		p.index++
	}
}

func (p *expressionParser) take(allowed ...rune) (rune, bool) {
	if p.index >= len(p.input) {
		return 0, false
	}
	for _, candidate := range allowed {
		if p.input[p.index] == candidate {
			p.index++
			return candidate, true
		}
	}
	return 0, false
}
