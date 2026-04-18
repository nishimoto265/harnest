# Claude Code rules の paths 挙動検証

## 検証日
2026-04-09 〜 2026-04-10

## 疑問
`.claude/rules/` に `paths` 指定付きルールを置いた場合、マッチするファイルにアクセスした時だけコンテキストにロードされるのか？

## 公式ドキュメントの記述
> Path-specific rules are loaded when Claude reads files matching those patterns
> Path-scoped rules trigger when Claude reads files matching the pattern, not on every tool use.

マッチするファイルを読む時にオンデマンドでロードされるのが仕様。

## 公式フォーマット

```yaml
---
paths:
  - "src/api/**/*.ts"
---
```

- フィールド名: `paths`（`globs` ではない）
- 値: YAML配列 + クォート付き文字列
- brace expansion対応: `"src/**/*.{ts,tsx}"`

## 検証（3回実施）

### 検証1: 不正フォーマット `globs: ["**/*.tsx"]`
- 結果: **全ルールがセッション開始時にロード**
- 原因: 不正フォーマットがパーサに無視され、グローバルルール扱いになった

### 検証2: 無効フィールド `globs: "**/*.css"`
- 結果: **paths指定ルールが一切ロードされない**
- 原因: `globs` はClaude Codeの正式フィールド名ではない

### 検証3: 公式フォーマット `paths:` + YAML配列（最終検証）

4つのルールを作成:
- test-global.md: paths指定なし
- test-css.md: `paths: ["**/*.css"]`
- test-tsx.md: `paths: ["**/*.tsx"]`
- test-api.md: `paths: ["lib/api/**/*.ts"]`

結果:

| ラウンド | 読んだファイル | 追加されたルール |
|---|---|---|
| 1 | なし（初期状態） | TEST_GLOBAL_RULE（常時ロード） |
| 2 | app/globals.css | **TEST_CSS_RULE が追加** |
| 3 | app/layout.tsx | **TEST_TSX_RULE が追加** |
| 4 | lib/api/client.ts | **TEST_API_RULE が追加** |

**公式フォーマットでは、paths指定のオンデマンドロードが正しく動作する。**

## 結論

- **公式フォーマット（`paths:` + YAML配列）を使えば、条件付きルールは正しく動作する**
- `globs` は無効。必ず `paths` を使う
- 値は必ずYAML配列形式（`- "pattern"`）で記述する
- paths指定なしのルールはセッション開始時に常時ロード
- paths指定ありのルールは該当ファイルを読んだ時に動的注入
- **一度注入されたルールはセッション中ずっと残る（再注入はされない）**

## 実用性

**rulesのpaths指定は実用的に使える。** 特に以下のユースケースで有効:
- CSSファイル編集時のみCSS責務ルールをロード
- コンポーネント作成時のみアーキテクチャルールをロード
- API層編集時のみデータ取得ルールをロード

コンテキスト効率の面でも、全ルールを常時ロードするより必要な時だけロードする方が効率的。

## 注意事項
- GitHub Issue #16299（全ルールがグローバルロードされるバグ）は、フォーマットの問題だった可能性がある
- Issue #23478（Write時に未ロード）、#26868（複数パターン処理）は未検証
