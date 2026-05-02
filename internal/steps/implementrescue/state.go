package implementrescue

import "time"

type State struct {
	ExpectedBaseSHA string    `json:"expected_base_sha" validate:"required,sha1_hex"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	Pid             int       `json:"pid,omitempty" validate:"gte=0"`
	Pgid            int       `json:"pgid,omitempty" validate:"gte=0"`
	LeaderStartTime string    `json:"leader_start_time,omitempty"`
	RetryCount      int       `json:"retry_count" validate:"gte=0"`
	LastHeartbeat   time.Time `json:"last_heartbeat,omitempty"`
}

func rescueNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now()
	}
	return now()
}
