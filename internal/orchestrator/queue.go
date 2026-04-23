package orchestrator

import (
	"github.com/nishimoto265/auto-improve/internal/contracts"
	"github.com/nishimoto265/auto-improve/internal/state"
)

type ResumeItem struct {
	PR    int
	RunID contracts.RunID
	Step  contracts.FailedStep
}

func ResumeQueue(entries []contracts.StateEntry) []ResumeItem {
	targets := state.ResumeTarget(entries)
	if len(targets) == 0 {
		return nil
	}
	items := make([]ResumeItem, 0, len(targets))
	for _, target := range targets {
		items = append(items, ResumeItem{
			PR:    target.PR,
			RunID: target.RunID,
			Step:  target.Step,
		})
	}
	return items
}
