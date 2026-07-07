# 発表「実施結果」用 素材集

スライドに載せる元データ。一番映えるのは §1 のタイムライン。
（出典: v12 maintain session `sesn_016WqNwNwyVL1dJv1qKBbMDn` ほか。すべて実データ）

---

## ★本番で載せるもの（絞り込み）

実施結果は盛りすぎず、以下の 3 点に絞る:
1. **動画**: agent が動く様子（main→classifier→review→DB→Issue）を早送りで流す（events 再生方式。下記スクリプト）
2. **主要フロー 1 枚（簡略版）**: 詳細タイムライン（§1）でなく主要ステップだけ
   ```
   PR の指摘 → main が抽出 → classifier が分類 → review が審査（revise→approve の自己修正）
   → DB に記録 + Issue 作成
   ```
3. **生成された PR のスクショ 1 枚**（distribute が作った 1 rule 1 PR）

§1〜§5 の詳細はすべて「参考資料」。本番スライドには上記 3 点だけ載せ、口頭で補足する。

---

## 1. agent 実行フローのタイムライン（最重要・そのまま貼り付け可）

main agent が classifier → review → self-check を自律的に呼び、DB 書き込み・Issue 作成まで完遂する実データの時系列。**review が revise を返し、main が改訂して再審査する自己修正ループが 2 回**入っているのが核。

```
[t=0]      USER → main      「harness rule の整備をお願いします。対象 PR: .../pull/1」

── Step 1: データ取得 ──────────────────────────────
[t+0s]     main → github      pull_request_read (PR本文取得)
[t+0s]     main → databricks   SELECT 既存 rule 全件
[t+9s]     main → github       get_diff / get_review_comments / get_reviews 並列取得
[t+58s]    main → databricks   SELECT harness_processed_prs (処理済み確認 → pending)

── Step 2: rule 候補 4 件を抽出 ────────────────────
[t+1m49s]  main             候補: safe-content-block-extraction / anthropic-api-retry-rate-limit
                            / reuse-anthropic-client / max-tokens-parameterized

── Step 3: classifier に delegate ─────────────────
[t+1m49s]  main ✦SPAWN✦ subagent: harness-rule-classifier
[t+1m49s]  main ══DELEGATE══▶ classifier  「候補4件を既存ruleと照合してclassifyして」
[t+2m24s]  classifier ══RETURN══▶ main
              候補1 safe-content-block-extraction → archived_skip (id=14と一致)
              候補2 anthropic-api-retry-rate-limit → archived_skip → ※後にupdate判定で復活
              候補3 reuse-anthropic-client        → duplicate (id=19, 配布済みと一致)
              候補4 max-tokens-parameterized      → new

── Step 4: review に並列 delegate + 自己修正ループ ──
[t+2m23s]  main ✦SPAWN✦ subagent: harness-rule-review ×2 (並列)
[t+2m23s]  main ══DELEGATE══▶ review#1  anthropic-api-retry-rate-limit
[t+2m23s]  main ══DELEGATE══▶ review#2  max-tokens-parameterized
[t+3m26s]  review#1 ══RETURN══▶ main  verdict=revise
              「SDK組み込みリトライ Anthropic(max_retries=N) を第一選択に。手動実装はフォールバック」
[t+3m27s]  main ══DELEGATE══▶ review#1 (改訂版で再審査 iteration 2/5)
[t+3m27s]  review#2 ══RETURN══▶ main  verdict=revise
              「max_tokensのみは粒度が恣意的。model/temperature含む主要パラメータ全般に拡張せよ」
[t+3m40s]  main ══DELEGATE══▶ review#2 (改名: anthropic-api-params-parameterized で再審査)
[t+4m35s]  review#1 ══RETURN══▶ main  verdict=approve  「全6 criteria 満たす」
[t+4m40s]  review#2 ══RETURN══▶ main  verdict=approve  「model/max_tokens/temperature包括で適切」

── Step 5: 副作用（DB書き込み + Issue作成）───────────
[t+4m35s]  main → databricks  UPDATE harness_rules (id=1 update: tier+1, evidence_count+1)
[t+5m20s]  main → databricks  INSERT harness_rules (新規 anthropic-api-params-parameterized)
[t+5m20s]  main → github      issue_write → Issue #20 作成 (update)
[t+5m42s]  main → github      issue_write → Issue #21 作成 (new, id=24)
[t+5m42s]  main → databricks  UPDATE harness_processed_prs (status=processed, patterns=2)

── Step 6: Self-check + 最終 JSON ──────────────────
[t+6m08s]  main             self-check: archived_skip×2 / duplicate×1 / update+approve / new+approve 全整合
[t+6m08s]  main             {"status":"done","results":[...4件...]} を出力 → 完遂
```

**見せ方**: 自己修正ループ（t+3m26s〜t+4m40s）を赤枠で囲む。「人間が一度も介入せず、subagent 同士で品質ゲートを回した」が最大の訴求点。候補2が classifier で一旦 archived_skip → main が PR エビデンスから update 復活 → approve、という経緯も語れる。

---

## 2. GitHub Actions ログ（auto-continue が 5 回 idle を乗り越える）

run `26495821576` / `harness-maintain` / conclusion=success。

```
[kick] PR_URL=.../pull/1
[kick] session=sesn_01GifPMqywhTE73SEj3ZgSkg
[poll] status=idle t+0s
[kick] idle without final JSON; sending continue (1/5)
[kick] idle without final JSON; sending continue (2/5)
[kick] idle without final JSON; sending continue (3/5)
[kick] idle without final JSON; sending continue (4/5)
[kick] idle without final JSON; sending continue (5/5)
[poll] status=running t+0s          ← ここで agent が起動
[poll] status=idle    t+271s        ← 271秒走って完遂
[kick] FINAL JSON detected: { "status": "done", "results": [ ... ] }
```

**見せ方**: 「CI 上で agent が idle に陥っても、kick スクリプトが自律的に背中を押して完遂させる」運用の堅牢性アピール。結果の中身（全件 archived_skip）でなく "落ちずに完走する仕組み" の文脈で使う。

---

## 3. Databricks テーブル

### 3-1. self-check の verdict（`harness_rule_check_results`）— 実データ
self-check agent が「この rule が実際にコードで守られているか」を根拠付きで判定。

| rule_id | rule_name | verdict | reason（要約） |
|---------|-----------|---------|----------------|
| 27 | structured-data-spec-verification | **compliant** | generateRecipeImages で Google 推奨の 1:1/4:3/16:9 の3サイズを生成し Recipe/Article の image に適用 |
| 28 | iso8601-for-structured-data-dates | **compliant** | store/article.js に openedAtISO/revisedAtISO getter を追加し JSON-LD に ISO 値を渡す |
| 29 | verify-node-version-before-deploy | **n_a** | 本PRはフロント変更で Node問題は別リポジトリ(Lambda)側。本PR対象外 |

**見せ方**: 「lint ではなく設計レベルの compliant/n_a 判定 + 具体的根拠（ファイル名・関数名）」が技術的深さ。verdict を色分け（緑=compliant, 灰=n_a）。

---

## 4. 生成 SKILL.md（配布物）

`.claude/skills/claude-api/SKILL.md`（category 親、静的 index）:
```markdown
---
name: harness-claude-api
description: claude-api のコーディング規約集。claude-api のコードを書く・レビューする時に必ず参照。rules/ 配下の全ファイルをチェックすること。
---
# Harness Rules: claude-api
この category の作業をする時は、rules/ 配下の全ファイルを読み、各 rule に違反していないか網羅的に確認すること。
```

`.claude/skills/claude-api/rules/reuse-anthropic-client.md`（tier=3 配布済み）:
```markdown
---
rule: reuse-anthropic-client
when: Anthropic API を呼び出すコードを書く・レビューする時
tier: 3
---
# Anthropic client はモジュールレベルで生成し再利用する
## 守ること
Anthropic client はモジュールレベルで生成し再利用する
## 問題
関数呼び出しのたびに Anthropic() を new すると接続確立のオーバーヘッドとリソース浪費が発生する
## ガイダンス
クライアントはモジュールスコープで一度だけ生成し、関数間で共有する
## 例
NG: def f(): client=Anthropic(); ...   OK: _client=Anthropic(); def f(): _client.messages.create(...)
```

**見せ方**: `when:`/`tier:` 構造が「Claude Code が自動参照する skill フォーマット」。DB の rule が開発者の手元で効く skill に変換される一気通貫が伝わる。reuse-anthropic-client は §1 で classifier が duplicate 判定した候補3 と同じ → タイムラインと紐付けて語れる。

---

## 5. 作成 Issue（代表: #21）

§1 タイムラインで review が 2 iteration で approve した new rule:

```
TITLE: [harness] new: anthropic-api-params-parameterized   [CLOSED]
## Rule: anthropic-api-params-parameterized (id: 24)
### checklist_item
Anthropic API 呼び出しに渡す主要パラメータ（model・max_tokens・temperature 等）を
関数内にハードコードせず、引数で上書きできるようにする
### rationale
PR #1 のレビューで max_tokens のハードコードを修正。同原則が model・temperature 等にも
適用される汎用パターンとして抽出。review subagent により 2 iteration で approve。
### source_pr_url: .../pull/1
```

**見せ方**: rationale に「review subagent により 2 iteration で approve」と明記 → §1 の自己修正ループが成果物にトレースできる。

---

## end-to-end ストーリー（1 枚で語る）

```
PR #1 のレビュー指摘
  → [§1 タイムライン] main が抽出 → classifier 分類 → review が自己修正ループで審査
  → [§5 Issue #21] 成果物として Issue 化（rationale に "2 iteration で approve"）
  → [§4 SKILL.md] tier=3 で skill として配布
  → [§3 self-check] 実コードで守られたか compliant/n_a を根拠付き判定
  → [§2 Actions] 全部 CI で自動・落ちずに完走
```

**発表で映える順**: ①§1 タイムライン（断トツ）→ ②§3 self-check verdict → ③§4 SKILL.md + §5 Issue #21（end-to-end 補強）→ ④§2 Actions ログ（堅牢性）

---

## 実行シナリオ（図用）

> 実データソース: 成功した Managed Agent セッション 2 件（events API より取得・完遂確認済み）。
> 詳細ログ JSON: `Docs/demo-log-pr4386.json` / `Docs/demo-log-selfcorrection.json`。

### メインシナリオ: PR #4386（self-check + maintain フルラン）

session `sesn_01M8cRsXPirZ8vNWugQzr824` / 所要 約7分33秒（08:36:14 → 08:44:09 UTC）/ stop=end_turn

```
【入力 PR】PR #4386「構造化データの改善（BreadcrumbList / Recipe image / Article 日付）」
           外部 API レスポンス検証・JSON-LD 実装
  ↓
【データ取得】main → github / databricks: PR diff・過去会話セッション・active rule(tier=3) を並行取得
  ↓
【self-check】既存 rule(tier=3) 3 件を、この PR の会話ログ + diff から評価（並行 delegate）
  - rule 27 recipe-image: 3 アスペクト比(1:1,4:3,16:9)         → compliant
  - rule 28 iso8601-for-structured-data-dates               → compliant
  - rule 29 verify-node-version-before-deploy               → n_a（このセッションでデプロイ未実施）
  critical=true なし → 結果 3 件を DB に INSERT
  ↓
【maintain】新しい指摘を 2 候補抽出 → classifier に分類依頼
  - 候補1 lazy-hydration-breaks-ssr-structured-data         → new
  - 候補2 recipe-image-array-three-aspect-ratios            → update（rule_id=27 へ）
  ↓
【review 自己修正ループ】両候補を review に並行 delegate（最大 5 iteration）
  - 候補1: revise（iteration 1）→ 改訂して再 delegate → approve（iteration 2）
  - 候補2: revise（iteration 1）→ 改訂して再 delegate → reject
  ↓
【副作用】
  - 候補1 approve → harness_rules に INSERT（tier=1, rule_id=32）+ GitHub Issue #4444 作成
  - 候補2 reject → tier=-1 で記録（再抽出防止）
  - processed_prs に PR #4386 を記録
  ↓
【step6 self-check】全副作用を自己確認（INSERT 件数・classifier 結果の非上書き・Issue 本文に機密なし）→ done
```

#### シーケンス（誰が・誰に・何を）

```
user            → main          : PR #4386 を分析して
main            → github        : pull_request_read（PR #4386 diff）
main            → databricks    : active rule(tier=3) 取得 / 過去会話セッション検索
main            → selfcheck-sub : rule 27/28/29 を会話ログ+diff で評価依頼（delegate）
selfcheck-sub   → main          : 27=compliant, 28=compliant, 29=n_a（critical なし）
main            → databricks    : self-check 結果 3 件 INSERT
main            → classifier-sub: 候補1・候補2 の分類依頼（delegate）
classifier-sub  → main          : 候補1=new, 候補2=update(→27)
main            → review-sub    : 候補1 審査依頼（delegate, iter1）
review-sub      → main          : 候補1 = revise（+ suggested_revision）
main            → review-sub    : 候補1 改訂版 審査依頼（delegate, iter2）
review-sub      → main          : 候補1 = approve
main            → review-sub    : 候補2 審査依頼 → revise → 改訂 → reject
main            → databricks    : 候補1 INSERT(tier=1) / 候補2 reject 記録(tier=-1)
main            → github        : Issue #4444 作成（候補1）
main            → databricks    : processed_prs に PR #4386 記録
main            → main          : step6 self-check（副作用検証）→ done
```

### サブシナリオ: 自己修正ループが綺麗に出るケース（v12 maintain）

session `sesn_016WqNwNwyVL1dJv1qKBbMDn` / 所要 約6分14秒（05:44:52 → 05:51:06 UTC、続けて確認応答 1 往復）/ stop=end_turn

```
【入力 PR】Anthropic SDK 利用コードの PR（pending）
  ↓
【候補抽出】main が 4 候補を抽出 → classifier に分類依頼
  - safe-content-block-extraction       → archived_skip（id=14/20、廃止済み再提案）
  - anthropic-api-retry-rate-limit      → update（rule_id=1）
  - reuse-anthropic-client              → duplicate（rule_id=19, tier=3 で配布済み）
  - max-tokens-parameterized            → new
  ↓
【review 自己修正ループ】update / new の 2 候補のみ review に並行 delegate（最大 5 iteration）
  - anthropic-api-retry-rate-limit:
      iter1 → revise（理由: SDK 組み込み max_retries を第一選択にすべき）
      main が guidance を改訂（SDK 機能優先 + 手動実装はフォールバック）
      iter2 → approve
  - max-tokens-parameterized:
      iter1 → revise（理由: スコープを max_tokens 単体から主要パラメータ全般へ拡張すべき）
      main がスコープ拡張して改訂
      iter2 → approve
  ↓
【副作用】
  - update + approve → harness_rules id=1 更新 + Issue #20 作成
  - new + approve    → harness_rules id=24 INSERT + Issue #21 作成
  - archived_skip / duplicate → Issue なし・processed_prs にのみ反映
  ↓
【step6 self-check】副作用を自己確認 → done
```

**見せ方**: review subagent が「方向性は正しいが指摘の粒度/スコープを直せ」と revise を返し、main が suggested_revision を取り込んで再 delegate → approve に至る往復が両候補で起きている。「review が単なる承認印ではなく、品質ゲートとして機能している」ことを 1 枚で示せる。

### 図に起こす際のメモ

- 主体（ノード）は **user / main-agent / 3 種の subagent（selfcheck・classifier・review）/ MCP サーバ（github・databricks）** の 7 種。
- subagent は毎回新規スレッドで起動され（PR #4386 では合計 6 スレッド）、review は iteration ごとに別スレッド。
- self-check と classifier、複数候補の review は **並行 delegate** されている（矢印を束ねると並列性が伝わる）。
- thinking ブロックは events API で本文非公開のため、図では「main が判断」程度の抽象ノードで表現。

---

## full シナリオ（tier 昇格 → skill 化 → 遵守）

主役 rule: **rule 27 `structured-data-spec-verification`**（category `claude-api-web` / tier=3 / 配布済み）。
checklist_item =「構造化データ(JSON-LD)を実装する際は Google 公式ドキュメントの推奨仕様を確認してから実装する」。

1 回の maintain では tier=3 にならない（同一指摘を **3 回独立に観測** して初めて昇格・配布対象になる）。
そこで「収集 → 繰り返し検知で昇格 → skill 配布 → 効果測定」の閉ループを 1 本のストーリーで見せる。
**昇格の 3 段（1→2→3）だけ【ストーリー】**、配布フォーマットと遵守判定は **【実データ】**。

### tier 遷移帯（1 枚の上部に置く）

```
 観測1            観測2            観測3            配布            遵守
 ┌─────┐   ┌─────┐   ┌─────┐   ┌─────┐   ┌─────┐
 │tier=1│ →│tier=2│ →│tier=3│ →│ SKILL │ →│compliant│
 │候補   │   │昇格   │   │配布対象│   │ 配布物 │   │ 効果測定│
 └─────┘   └─────┘   └─────┘   └─────┘   └─────┘
  ストーリー    ストーリー    ストーリー    実データ      実データ
  （薄い色）    （薄い色）    （薄い色）    （濃い色）    （濃い色）
```

### 縦フロー

```
【PR-A（1回目の観測）】 ※ストーリー（tier=1）
  あるレビューで「JSON-LD を Google の仕様を確認せず推測で実装していた」と指摘
  → main が指摘を抽出
  → classifier=new（既存 rule に該当なし）
  → review=approve
  → INSERT rule 27（tier=1, candidate / evidence_count=1）
        checklist_item: 構造化データ(JSON-LD)を実装する際は
                        Google 公式ドキュメントの推奨仕様を確認してから実装する

      ▼ 同じ指摘がまた起きるか？（1 回では skill にしない）

【PR-B（2回目・別 PR）】 ※ストーリー（tier 1→2）
  別の構造化データ PR で同じ種類の指摘が再発
  → classifier=update（既存 rule 27 と一致 → target_rule_id=27）
  → UPDATE rule 27（tier 1→2, evidence_count 1→2）

      ▼ もう 1 回観測されれば配布対象（ノイズ除去のしきい値）

【PR-C（3回目・さらに別 PR）】 ※ストーリー（tier 2→3）
  さらに別 PR で同じ指摘
  → classifier=update（rule 27 と一致）
  → UPDATE rule 27（tier 2→3, evidence_count 2→3）= promoted（配布対象に到達）

───────────────── ここから実データ ─────────────────

【distribute（配布）】 ※実データのフォーマット（§4）
  tier=3 の rule 27 を skill として配布する PR を作成
  → .claude/skills/claude-api-web/rules/structured-data-spec-verification.md

  ┌──────────────────────────────────────────────┐
  │ ---                                                          │
  │ rule: structured-data-spec-verification                      │
  │ when: 構造化データ(JSON-LD)を実装・修正する時                 │
  │ tier: 3                                                      │
  │ ---                                                          │
  │ # 構造化データは Google 公式仕様を確認してから実装する        │
  │ ## 守ること                                                  │
  │ 構造化データ(JSON-LD)を実装する際は                          │
  │ Google 公式ドキュメントの推奨仕様を確認してから実装する       │
  │ ## 問題                                                      │
  │ 推測でプロパティやフォーマットを決めると                     │
  │ リッチリザルトに反映されない                                 │
  │ ## ガイダンス                                                │
  │ 実装前に schema.org / Google 検索セントラルの該当タイプ       │
  │ （Recipe / Article 等）の必須・推奨プロパティを確認し、       │
  │ 値の形式（画像アスペクト比・日付フォーマット等）を仕様に合わせる │
  │ ## 例                                                        │
  │ NG: image: imageUrl（推測で単一 URL を指定）                  │
  │ OK: image: generateRecipeImages(imageUrl)                    │
  │     → Google 推奨 3 アスペクト比 1:1 / 4:3 / 16:9 を配列で渡す │
  └──────────────────────────────────────────────┘
  ※ category 親 index は §4 同様 .claude/skills/claude-api-web/SKILL.md（rules/ 配下を全チェックさせる静的 index）

      ▼ 配布後、開発者の手元で skill として効くか？

【PR #4386（後続の実装）】 ※実データ（§3 self-check / Docs/demo-log-pr4386.json）
  開発者が構造化データを実装した PR（BreadcrumbList / Recipe image / Article 日付）
  → self-check agent が「会話ログ + diff」から rule 27 の遵守を評価
  → verdict = compliant
        根拠（実物）: generateRecipeImages で Google 推奨の 1:1 / 4:3 / 16:9 の
                      3 アスペクト比を生成し Recipe / Article の image に適用。
                      会話中の「Google はどこを見て正しいと判断するの？」という問いから
                      仕様確認の議論が行われ、正しいアスペクト比を採用した結果が diff に反映。
  → harness_rule_check_results に記録（rule_id=27, verdict=compliant, critical=false）
```

### シーケンス（誰が・誰に・何を）

```
── 観測1（PR-A）※ストーリー ──────────────────────
user        → main          : PR-A を分析して
main        → classifier    : 指摘候補（構造化データ仕様未確認）を分類依頼
classifier  → main          : new（既存 rule に該当なし）
main        → review        : 審査依頼 → approve
main        → DB            : INSERT rule 27（tier=1, evidence_count=1）

── 観測2（PR-B）※ストーリー ──────────────────────
user        → main          : PR-B を分析して
main        → classifier    : 同種の指摘を分類依頼
classifier  → main          : update（→ rule 27 と一致）
main        → DB            : UPDATE rule 27（tier 1→2, evidence_count→2）

── 観測3（PR-C）※ストーリー ──────────────────────
user        → main          : PR-C を分析して
classifier  → main          : update（→ rule 27 と一致）
main        → DB            : UPDATE rule 27（tier 2→3 = promoted, evidence_count→3）

── 配布 ※実データのフォーマット ──────────────────
distribute  → repo          : .claude/skills/claude-api-web/rules/
                              structured-data-spec-verification.md を作る PR
                              （frontmatter rule/when/tier + 守ること/問題/ガイダンス/例）

── 遵守（PR #4386）※実データ ─────────────────────
user        → main          : PR #4386 を分析して
main        → selfcheck-sub : rule 27 を会話ログ + diff で評価依頼（delegate）
selfcheck-sub → main        : rule 27 = compliant
                              （generateRecipeImages で 1:1/4:3/16:9 を生成・適用）
main        → DB            : harness_rule_check_results に compliant を記録 → done
```

### 訴求コメント（スライド脇に置く一言）

> **3 回の独立した観測を経て初めて配布される。**
> 1 回きりの偶発的な指摘は tier=1 のまま埋もれ、skill として配布されない。
> 同じ問題が別 PR で繰り返し観測されたものだけが tier を上げて昇格 = **ノイズを排除して重要な rule だけ skill 化される**。
> そして配布された skill は後続 PR で self-check により「実コードで守られたか」を根拠付きで効果測定される（rule 27 = compliant）。

### 発表での見せ方

- **1 枚のスライドで縦に流す**: 上部に「tier 遷移帯」、その下に PR-A → PR-B → PR-C → 配布 → PR #4386 の縦フロー。
- **色で実物/ストーリーを区別**:
  - 昇格 3 段（PR-A/B/C）は **薄い色（グレー破線枠）** + 各カードに【ストーリー】バッジ。
  - 配布 SKILL.md と PR #4386 self-check は **濃い色（実線枠）** + 【実データ】バッジ。
- **しきい値の矢印**: 観測2→3 の間に「同種指摘が再発 → 昇格」の小さい注記を入れ、tier=3 で初めて配布側へ太い矢印が伸びる演出。
- **トレーサビリティの線**: rule 27 の checklist_item が「収集時の文言」→「skill の守ること」→「self-check の評価対象」と一本の線で繋がることを薄い下線でつなぐ。
- **数字の強調**: tier 帯に「3 回観測でようやく配布」を太字で。発表口頭で「ここがノイズ除去」と一言添える。
- **誇張しない注意**: 配布 PR と PR #4386 = compliant は実物に忠実。昇格 3 段は各カードに【ストーリー】を明記し、「実 maintain では 1 回 1 段、3 回で tier=3」という仕組みの説明に徹する。
