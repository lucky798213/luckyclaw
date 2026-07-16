package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/lucky798213/luckyclaw/internal/provider"
)

type currentTimeTool struct {
	now func() time.Time
}

type currentTimeArguments struct {
	Timezone string `json:"timezone,omitempty"`
}

func newCurrentTimeTool(now func() time.Time) Tool {
	if now == nil {
		now = time.Now
	}
	return &currentTimeTool{now: now}
}

func (t *currentTimeTool) Definition() provider.Tool {
	return functionDefinition(
		"current_time",
		"Return the current time. Optionally provide an IANA timezone such as Asia/Shanghai.",
		map[string]any{
			"type": "object",
			"properties": map[string]any{
				"timezone": map[string]any{
					"type":        "string",
					"description": "Optional IANA timezone name, for example Asia/Shanghai.",
				},
			},
			"additionalProperties": false,
		},
	)
}

func (t *currentTimeTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	var arguments currentTimeArguments
	if err := decodeArguments(raw, &arguments); err != nil {
		return "", err
	}
	location := time.Local
	if arguments.Timezone != "" {
		loaded, err := time.LoadLocation(arguments.Timezone)
		if err != nil {
			return "", fmt.Errorf("invalid timezone %q: %w", arguments.Timezone, err)
		}
		location = loaded
	}
	result, err := json.Marshal(map[string]string{
		"time":     t.now().In(location).Format(time.RFC3339),
		"timezone": location.String(),
	})
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	return string(result), nil
}
