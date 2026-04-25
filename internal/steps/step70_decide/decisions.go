package step70_decide

import (
	"time"

	"github.com/nishimoto265/auto-improve/internal/contracts"
	internalio "github.com/nishimoto265/auto-improve/internal/io"
)

func newRollbackDecision(runCtx internalio.RunContext, intention contracts.IntentionRecord, reason contracts.RollbackReason, failed contracts.FailedStep, at time.Time) contracts.Decision {
	return contracts.Decision{
		Action: contracts.DecisionActionRollback,
		Value: contracts.DecisionRollback{
			Action:         contracts.DecisionActionRollback,
			SchemaVersion:  "1",
			RunID:          runCtx.RunID,
			IdempotencyKey: intention.IdempotencyKey,
			RollbackReason: reason,
			FailedStep:     failed,
			BestShaBefore:  intention.BestShaBefore,
			TargetSha:      intention.TargetSha,
			DecidedAt:      at,
		},
	}
}
