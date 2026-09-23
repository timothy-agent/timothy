package automations

import (
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrBadCron marks a cron expression that does not parse.
var ErrBadCron = errors.New("invalid cron expression")

// ValidateCron reports whether expr parses as a standard 5-field cron
// expression.
func ValidateCron(expr string) error {
	if _, err := cron.ParseStandard(expr); err != nil {
		return fmt.Errorf("%w: %s", ErrBadCron, err)
	}
	return nil
}

// NextRun returns the first boundary of expr after anchor, evaluated in
// anchor's location. Zero time on an invalid expression.
func NextRun(expr string, anchor time.Time) time.Time {
	schedule, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}
	}
	return schedule.Next(anchor)
}
