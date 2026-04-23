---
name: review-fix-loop
description: 複数レビューエージェントで findings を出し、妥当性を精査し、worktree で責務分割して修正・統合・再レビューを反復するワークフロー。ユーザーが「レビューのループを回したい」「複数エージェントでレビューして直したい」「findings を潰し切るまで回したい」「並列レビュー→並列修正→統合を繰り返したい」と言ったときに使用する。
---

# review-fix-loop

レビューを並列化し、出た指摘を自分で精査した上で、競合しない ownership に分割して修正し、統合後に再レビューまで回すためのスキル。

## 原則

- 正本は常に現在の統合先ブランチ `main` または専用 integration branch とする
- レビュー指摘は仮説として扱い、コード・README・設計 docs を見て採用可否を決める
- severity と採用可否は分けて考える。`High` でも不適切なら却下、`Low` でも妥当なら取り込む
- 修正分割は severity 基準ではなく **ファイル ownership と merge 競合回避** を優先する
- 分割実装を統合した後は、必ずメインが食い違いを確認し、必要なら自分で追加修正する
- 途中で残っている未統合 branch / worktree は勝手に捨てない。discard するか inspect するかを repo ルールとユーザー指示で決める

## 開始時にやること

1. README と canonical docs を読む
2. 現在の正本ブランチ、未統合 branch、未整理 worktree を確認する
3. 今回の開始点を決める
   - 既存の未統合作業を継続する
   - 既存の未統合作業は破棄し、正本ブランチからレビュー再開する
4. レビュー対象範囲を固定する
   - 直近の統合 diff
   - ある feature branch 全体
   - main の現状態そのもの

## 1. Review Round

レビューは通常 6 体前後で回す。観点が偏らないように slice を分ける。

### 推奨 slice

- rescue / recovery / resume
- worktree / checkout / restore-base / env
- scoring / scorecore / judge outputs
- step70 / archive / registry / contract sync
- docs 契約整合性
- integration risk / regressions / cross-step invariants

各 reviewer には以下を求める。

- finding ごとに `severity`
- 具体的な file / line / code path
- 何が壊れるか
- 可能なら docs / contract との不整合根拠

レビュー結果は次の形でまとめる。

| id | severity | area | summary | source |
|---|---|---|---|---|
| F1 | Critical/High/Medium/Low | rescue / scoring / ... | 1 行要約 | reviewer 名 |

## 2. Triage

レビュー結果はそのまま実装に流さない。メインが以下を行う。

1. 重複 finding をまとめる
2. 実コードを読む
3. README と設計 docs を読む
4. 実装目的に照らして妥当か判定する

判定は 3 分類に落とす。

- `accept`: 今 round で修正する
- `defer`: 妥当だが今回の目的・スコープ外
- `reject`: 指摘自体が不適切、または docs と整合しない

最終的に、以下の ledger を持つ。

| id | severity | disposition | rationale | owner |
|---|---|---|---|---|
| F1 | High | accept/defer/reject | 判断理由 | wt-a / wt-b / main |

## 3. Fix Planning

修正担当数は findings 数と ownership の独立性で決める。

- 1 体: 変更が小さい、または同一ファイル群に集中している
- 3 体: 一般的な fix round
- 4 体: 変更量が多く、ownership を自然に分離できる

### 分割基準

- 同じファイルを複数 worker に触らせない
- 同じ package でも責務が独立していれば分けてよい
- docs 更新は、それに密接に紐づく実装 owner に寄せる
- cross-cutting で conflict しやすい変更は、むしろ 1 owner に寄せる

### よくある ownership 例

- `wt-a`: rescue / recovery / lease / state
- `wt-b`: restore-base / worktree / git / env
- `wt-c`: scoring / pairwise / scorecore
- `wt-d`: step70 / archive / registry / docs sync

各 worker には次を明示する。

- 自分の ownership
- 触ってよいファイル群
- 他 worker の変更を revert しないこと
- finding ID と対応関係
- 必須テスト

## 4. Fix Execution

各 worker は worktree 上で修正する。メインは parallel 実行中に待ち続けず、以下を進める。

- 他 worker の status 確認
- 統合順の見立て
- 後で必要になる cross-check 項目の整理

worker 完了時は次を確認する。

- finding を本当に潰しているか
- 余計な差分がないか
- ownership 外の変更が混ざっていないか
- 対象テストが通っているか

## 5. Integration

統合前に各 slice を確認する。

- `git diff --stat`
- 対応 finding と差分の一致
- 対象 package のテスト

統合は repo ルールに従う。

- main へ直接 merge
- integration branch へ集約後に merge
- squash merge

いずれでも、統合後にメインが次を行う。

1. formatter 実行
2. targeted test
3. 必要なら full test
4. 分割実装間の食い違い確認
5. 必要なら main 上で追加修正

## 6. Post-Merge Check

統合後は「merge できた」で終わらない。必ず以下を点検する。

- review finding が実際に消えたか
- worker 間で invariant が崩れていないか
- docs / contract sync が壊れていないか
- split した結果、片方だけ更新されて pair が崩れていないか

ここでズレを見つけたら、メインが自分で直してから次 round に進む。

## 7. Repeat

統合後の新しい正本に対して、また 6 体前後のレビューを回す。

停止条件は以下のいずれか。

- actionable findings が 0
- 残件が全て `defer` または `reject`
- ユーザーがループ停止を指示

## 出力のしかた

各 round の報告は短く揃える。

### Review 後

- 総 finding 数
- severity 別件数
- `accept / defer / reject` 件数
- 次の分割案

### Fix 後

- どの owner がどの finding を担当したか
- 統合後の追加修正の有無
- 実行した verification
- 次 round に進めるか

## 禁止事項

- レビュー指摘を脳死で採用しない
- 分割のために同じファイルを複数 worker に触らせない
- worker 完了後に未確認のまま機械的に merge しない
- 分割実装のズレを「次の review で見つかるだろう」で放置しない
