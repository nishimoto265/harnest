// Package contracts is the single source of truth for all inter-step schemas.
//
// 全 step は本 package から型 / schema を import し、`internal/io` helper 経由
// でのみファイルを読み書きする (io-contracts.md §単一 source of truth)。schema
// 変更は必ず `docs/design/io-contracts.md` と同一 commit で行い、`DisallowUnknownFields`
// 付き reader で動くため field 削除 / discriminator 変更は breaking change 扱い。
//
// Numeric type 規約 (io-contracts.md rev23/rev24): 本 package 配下では
// uint64 / float32 / float64 の使用を禁止する。全 schema は int64 または int
// (= int64-safe range) のみ使用する。canonical JSON hash の安定性のため。
package contracts

// RunID identifies a single pipeline run (1 merged PR = 1 run).
//
// Format: "YYYY-MM-DD-PR<num>-<hex7>" (例: "2026-04-20-PR42-abcdef0").
// step10 が `internal/io/run_layout.go#NewRunID(pr)` で生成する。
type RunID string

// AgentID identifies an implementation agent inside a pass (a1/a2/a3 ...).
//
// Format: "a" + positive integer (no leading zero).
type AgentID string
