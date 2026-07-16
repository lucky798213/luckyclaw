package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCalculatorEvaluatesSafeExpressions(t *testing.T) {
	tool := newCalculatorTool()
	tests := []struct {
		expression string
		want       string
	}{
		{expression: "2 + 3 * 4", want: "14"},
		{expression: "(2 + 3) * 4", want: "20"},
		{expression: "--5 + -(2)", want: "3"},
		{expression: ".5 + 1.5", want: "2"},
		{expression: "1e3 / 4", want: "250"},
		{expression: "-0", want: "0"},
	}
	for _, test := range tests {
		t.Run(test.expression, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"expression": test.expression})
			result, err := tool.Execute(context.Background(), raw)
			if err != nil || result != test.want {
				t.Fatalf("Execute() = %q, %v; want %q", result, err, test.want)
			}
		})
	}
}

func TestCalculatorRejectsUnsafeOrInvalidExpressions(t *testing.T) {
	tool := newCalculatorTool()
	nested := strings.Repeat("(", maxCalculatorNesting+1) + "1" + strings.Repeat(")", maxCalculatorNesting+1)
	tooLong := strings.Repeat("1", maxCalculatorExpressionLength+1)
	for _, expression := range []string{
		"",
		"1 / 0",
		"sqrt(4)",
		"2 ^ 8",
		"1e",
		"1 2",
		nested,
		tooLong,
		"1e309",
	} {
		t.Run(expression, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]string{"expression": expression})
			if _, err := tool.Execute(context.Background(), raw); err == nil {
				t.Fatalf("Execute(%q) error = nil", expression)
			}
		})
	}
}

func TestCalculatorRejectsMalformedJSONAndUnknownFields(t *testing.T) {
	tool := newCalculatorTool()
	for _, raw := range []string{
		`{"expression":"1+1","extra":true}`,
		`{"expression":1}`,
		`{"expression":"1+1"} trailing`,
	} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(raw)); err == nil {
			t.Fatalf("Execute(%s) error = nil", raw)
		}
	}
}
