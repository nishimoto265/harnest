# Provider hooks 設計

この文書は、Claude Code と Codex の hooks を `auto-improve` から扱う場合の
共通化境界を定義する。hook の詳細仕様は provider 側で変わり得るため、
この文書では harness が依存してよい最小 subset だけを正本とする。

参考:

- Claude Code Hooks reference: <https://code.claude.com/docs/en/hooks>
- Codex Hooks: <https://developers.openai.com/codex/hooks>

## 結論

Claude と Codex の hooks は同じ機能ではない。

`auto-improve` では provider の hook 設定を直接アプリ仕様にしない。
代わりに、以下の 6 種類だけを provider-neutral capability として扱い、
Claude / Codex adapter が各 provider の設定形式へ変換する。

| auto-improve capability | Claude event | Codex event | 用途 |
| --- | --- | --- | --- |
| `session_start` | `SessionStart` | `SessionStart` | セッション開始時に追加コンテキストや環境確認を行う |
| `user_prompt_submit` | `UserPromptSubmit` | `UserPromptSubmit` | prompt 投入前に secrets / 禁止操作 / task brief を検査する |
| `pre_tool_use` | `PreToolUse` | `PreToolUse` | tool 実行前に危険コマンドや禁止 path 編集を止める |
| `permission_request` | `PermissionRequest` | `PermissionRequest` | approval 要求に対して allow / deny / no decision を返す |
| `post_tool_use` | `PostToolUse` | `PostToolUse` | tool 実行後に lint / validation / feedback を返す |
| `stop` | `Stop` | `Stop` | turn 終了時に完了条件を確認し、必要なら継続指示を返す |

この 6 種類以外は共通仕様に入れない。

## 共通 capability の意味

### `session_start`

セッション開始または resume 時に走る。
追加コンテキストの注入、repo policy snapshot の説明、runtime 状態の注意喚起に使える。

hard block 用途には使わない。実行開始可否は `auto-improve preflight` と step 側で判定する。

### `user_prompt_submit`

agent に prompt が渡る前に走る。
secret 混入、禁止されている高リスク依頼、task brief の最低限の形を検査する用途に向く。

ここで実装方針を作り替えすぎると pass1 / pass2 の比較条件が変わるため、
`auto-improve` では追加コンテキストまたは明確な block に限定する。

### `pre_tool_use`

tool 実行前に走る。共通 hook の中では最も guardrail に向いている。

主な用途:

- 危険な shell command の拒否
- `deny_write_paths` に該当する編集の拒否
- MCP write tool の拒否
- approval が不要な操作でも repository policy に反する操作を止める

ただし、provider 間で捕捉できる tool 経路は同一ではない。
Codex の `PreToolUse` は `Bash`、`apply_patch`、MCP tool を捕捉できるが、
すべての shell / WebSearch / 非 shell 非 MCP tool を完全には捕捉しない。
そのため、ファイル編集禁止を `pre_tool_use` だけに依存してはいけない。

### `permission_request`

provider が approval を要求する直前に走る。
`pre_tool_use` と違い、approval が発生しない操作には走らない。

用途:

- network / shell escalation の deny
- 特定 command だけ allow
- user approval に回す前の policy 判定

`auto-improve` では、実装 agent の自動実行を安定させるため、
provider の permission model へ直接依存しすぎない。
最終的な安全性は sandbox と post-run diff validation で担保する。

### `post_tool_use`

tool 実行後に走る。
副作用はすでに発生しているため、禁止操作を未然に防ぐ用途には使わない。

用途:

- tool output を読んで追加 feedback を返す
- lint / format / lightweight validation の結果を agent に伝える
- 変更後の generated file や checklist の不足を指摘する

修正不能な違反は `post_tool_use` で止めきろうとせず、
step artifact の validation で run を失敗させる。

### `stop`

turn 終了時に走る。
最終応答の直前に、完了条件や必須検証が満たされているかを見る用途に向く。

用途:

- test 未実行なら追加実行を促す
- checklist 未更新なら継続させる
- 明らかな未完了状態なら final response を止める

`auto-improve` では `stop` hook を採用判定の正本にしない。
採用判定は step30 / step60 / step70 の artifact と judge 結果で行う。

通常利用の PR 前 checklist guard では、provider-specific hook に判定を埋め込まず、
次を呼ぶ。

```bash
auto-improve lessons verify-checklist-result
```

このコマンドは `.auto-improve/work/checklist-result.md` の `[x]` / `[-]` / `[!]` を検証し、
未確認 `[ ]` や理由のない `[!]` を失敗させる。

## 編集禁止の扱い

ファイル編集禁止は 3 段で扱う。

| 層 | 責務 | 例 |
| --- | --- | --- |
| sandbox | そもそも書けない実行環境にする | judge / task generator は read-only で起動する |
| hook | 実行前に obvious な違反を止める | `pre_tool_use` で `docs/harness-eval/checklists/*` の編集を deny |
| diff validation | 実行後に最終的な変更範囲を検証する | 禁止 path が差分にあれば run を失敗させる |

hook は guardrail であり、security boundary ではない。
特に Codex では `PreToolUse` がすべての操作を完全捕捉する前提にしない。

## provider 差分

### Claude Code

Claude Code は hooks の種類が多く、command / HTTP / MCP tool / prompt / agent hook を持つ。
共通 6 種類以外にも、`InstructionsLoaded`、`UserPromptExpansion`、`PostToolUseFailure`、
`PostToolBatch`、`PermissionDenied`、`Notification`、`SubagentStart`、
`SubagentStop`、`TaskCreated`、`TaskCompleted`、`FileChanged`、`WorktreeCreate`、
`PreCompact`、`SessionEnd` などがある。

これらは便利だが、Codex 側に同等の安定機能がないため、
`auto-improve` の共通 hook contract には入れない。

### Codex

Codex hooks は `config.toml` で `codex_hooks = true` を有効にする必要がある。
設定場所は主に次の 4 つである。

- `~/.codex/hooks.json`
- `~/.codex/config.toml`
- `<repo>/.codex/hooks.json`
- `<repo>/.codex/config.toml`

Codex の実用上の共通 event は `SessionStart`、`PreToolUse`、
`PermissionRequest`、`PostToolUse`、`UserPromptSubmit`、`Stop` に限定する。
`PreToolUse` / `PostToolUse` / `PermissionRequest` の matcher は
`Bash`、`apply_patch`、MCP tool を対象にできる。
`apply_patch` は `Edit` / `Write` alias でも match できる。

Codex hooks は複数 matching hook が並列実行されるため、
1 つの hook が別の hook の起動を止める前提にしない。

## auto-improve の設定モデル案

provider 固有設定を直接ユーザーに書かせず、まずは次のような中立設定を持つ。

```yaml
hooks:
  enabled: true
  deny_write_paths:
    - docs/harness-eval/checklists/*
    - .node-version
  deny_commands:
    - "git reset --hard"
    - "git checkout --"
  post_validation:
    run_after_edit:
      - "gofmt"
      - "go test ./..."
```

adapter はこれを provider 別に変換する。

Claude では `.claude/settings.json` の hook 設定に変換する。
Codex では `.codex/hooks.json` または `.codex/config.toml` に変換し、
必要なら `[features] codex_hooks = true` も生成する。

ただし、`auto-improve` が npm 的に配布される前提では、
tool install directory に対象 repo の hook 設定を書かない。

配置方針:

- user scope: `~/.auto-improve/hooks/` に共通 script template / cache を置く
- repo scope: `<repo>/.auto-improve/` に repo-specific hook policy を置く
- provider projection: `.claude/` / `.codex/` は必要時に生成または managed path として publish する

## 共通仕様に入れないもの

以下は provider-specific extension として扱い、共通 harness contract には入れない。

- Claude の `FileChanged` / `WorktreeCreate` / `Subagent*` / `Task*`
- Claude の prompt hook / agent hook / async hook
- Codex 固有の future hook fields
- provider の hooks を採用判定の正本にすること
- hook だけで security boundary を作ること

## 実装時の優先順位

1. read-only role は hooks ではなく provider sandbox / permissions で書き込み禁止にする。
2. implementer role には `pre_tool_use` で obvious な禁止操作を入れる。
3. step 終了後に diff validation を必ず走らせる。
4. `post_tool_use` / `stop` は agent への feedback と完了条件の補助に限定する。
5. provider-specific hooks は、共通仕様を壊さない optional extension として足す。
