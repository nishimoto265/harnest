"""Phase 1 E2E 副作用層: candidate JSON を読んで GitHub Issue を作成し、Databricks 用 SQL を stdout に出す。

DB 書き込みは Databricks SQL MCP（または手動）で実行する。本スクリプトは SQL 文を生成するだけ。

使い方:
    python scripts/post_actions.py < /tmp/candidate.json > /tmp/post.sql
    # /tmp/post.sql の SQL を Databricks で実行
"""

from __future__ import annotations

import json
import re
import subprocess
import sys


def gh(args: list[str]) -> str:
    r = subprocess.run(["gh"] + args, capture_output=True, text=True, check=True)
    return r.stdout


def sql_str(s: str | None) -> str:
    if s is None:
        return "NULL"
    return "'" + s.replace("'", "''") + "'"


def main() -> None:
    payload = json.load(sys.stdin)
    candidate = payload["candidate"]
    session_id = payload["session_id"]
    repo = payload["repo"]
    pr_number = payload["pr_number"]
    pr_url = payload["pr_url"]

    status = candidate.get("status")
    if status not in ("approved", "rejected", "max_iter_reached"):
        sys.exit(f"unknown candidate status: {status!r}")

    if status == "rejected":
        sys.stderr.write(f"[post] candidate rejected, no INSERT / Issue\n")
        # processed_prs だけ更新（skipped_permanent）
        sql = f"""\
UPDATE dev_bronze.test.harness_processed_prs
SET status='processed', patterns_extracted=0, updated_at=current_timestamp(),
    reason='rejected_by_review'
WHERE pr_url={sql_str(pr_url)};
"""
        print(sql)
        return

    title = candidate["title"]
    problem = candidate["problem"]
    iterations = int(candidate.get("iterations", 1))

    if not re.match(r"^[a-z][a-z0-9-]{0,63}$", title):
        sys.exit(f"invalid title (must be kebab-case): {title!r}")

    # GitHub Issue 作成
    issue_body = (
        f"## Candidate Rule\n\n"
        f"- **id**: `{title}`\n"
        f"- **category** (provisional): `claude-api`\n\n"
        f"### Problem\n\n{problem}\n\n"
        f"### Source\n\n"
        f"- Repo: `{repo}`\n"
        f"- PR: #{pr_number}\n"
        f"- Managed Agent session: `{session_id}`\n"
        f"- Review iterations: {iterations}\n\n"
        f"---\n\n"
        f"このルール候補を採用する場合はこの Issue を close、却下する場合はラベル `harness-rule-rejected` を付けて close してください。"
    )
    issue_url_out = gh([
        "issue", "create",
        "--repo", repo,
        "--title", f"[harness] {title}",
        "--body", issue_body,
        "--label", "harness-rule-candidate",
    ])
    issue_url = issue_url_out.strip().split("\n")[-1]
    sys.stderr.write(f"[post] Issue created: {issue_url}\n")

    # Databricks 用 SQL を出力
    final_verdict = "approve" if status == "approved" else "max_iter_reached"
    sql = f"""\
-- 1. rules（新規 candidate を INSERT）
INSERT INTO dev_bronze.test.harness_rules
  (id, status, evidence_count, category, checklist_item, problem,
   guidance, exceptions, examples, merge_notes,
   first_seen, last_seen, updated_at)
VALUES
  ({sql_str(title)}, 'candidate', 1, 'claude-api',
   {sql_str(title.replace('-', ' ').capitalize())},
   {sql_str(problem)},
   NULL, NULL, NULL, NULL,
   current_timestamp(), current_timestamp(), current_timestamp());

-- 2. rule_evidence（今回の PR の根拠を記録）
INSERT INTO dev_bronze.test.harness_rule_evidence
  (rule_id, pr_number, comment_id, commit_sha, note, ts)
VALUES
  ({sql_str(title)}, {pr_number}, NULL, NULL,
   {sql_str(f"iter={iterations}, session={session_id}")},
   current_timestamp());

-- 3. review_outcomes
INSERT INTO dev_bronze.test.harness_review_outcomes
  (rule_id, pr_url, final_verdict, iterations, feedback_history, ts)
VALUES
  ({sql_str(title)}, {sql_str(pr_url)},
   {sql_str(final_verdict)}, {iterations}, NULL, current_timestamp());

-- 4. processed_prs を 'processed' に UPDATE + issue_url 紐付け
UPDATE dev_bronze.test.harness_processed_prs
SET status='processed',
    patterns_extracted=1,
    issue_url={sql_str(issue_url)},
    updated_at=current_timestamp()
WHERE pr_url={sql_str(pr_url)};
"""
    print(sql)


if __name__ == "__main__":
    main()
