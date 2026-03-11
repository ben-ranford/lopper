package notify

import (
	"fmt"
	"strings"
)

type Outcome struct {
	Breach               bool
	WasteIncreasePercent *float64
}

func ParseTrigger(value string) (Trigger, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", string(TriggerAlways):
		return TriggerAlways, nil
	case string(TriggerBreach):
		return TriggerBreach, nil
	case string(TriggerRegression):
		return TriggerRegression, nil
	case string(TriggerImprovement):
		return TriggerImprovement, nil
	default:
		return "", fmt.Errorf("unknown notify trigger: %s", value)
	}
}

func ShouldTrigger(trigger Trigger, outcome Outcome) bool {
	switch trigger {
	case TriggerAlways:
		return true
	case TriggerBreach:
		return outcome.Breach
	case TriggerRegression:
		return outcome.WasteIncreasePercent != nil && *outcome.WasteIncreasePercent > 0
	case TriggerImprovement:
		return outcome.WasteIncreasePercent != nil && *outcome.WasteIncreasePercent < 0
	default:
		return false
	}
}
