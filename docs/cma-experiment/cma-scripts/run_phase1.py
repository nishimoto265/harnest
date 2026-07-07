"""Phase 1 E2E trigger 層: PR メタを GitHub から取得 → Managed Agent kick → candidate JSON を stdout に出す。

副作用は出さない（DB / Issue 書き込みなし）。post_actions.py がそれを担当する。

env var 名は `HARNESS_ANTHROPIC_API_KEY`（Claude Code 本体が `ANTHROPIC_API_KEY` を読み取って claude.ai 枠から API key 経由に切り替わるのを避けるため、別名にしてある）。

使い方:
    export HARNESS_ANTHROPIC_API_KEY=sk-ant-...
    python scripts/run_phase1.py everytv/harness-rule-test 1 > /tmp/candidate.json
"""

from __future__ import annotations

import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

API = "https://api.anthropic.com"
MAIN_AGENT = "agent_018aTQSKpTLeXJz3cJCKaikY"
ENV_ID = "env_01AAvct6ZfPtCxBHs82DrjZy"
POLL_TIMEOUT_SEC = 240
POLL_INTERVAL_SEC = 2


def headers() -> dict[str, str]:
    key = os.environ.get("HARNESS_ANTHROPIC_API_KEY")
    if not key:
        sys.exit(
            "ERROR: HARNESS_ANTHROPIC_API_KEY env var が未設定です。\n"
            "       Claude Code 本体が ANTHROPIC_API_KEY を読み取って claude.ai 枠から離れるのを避けるため、\n"
            "       別名 (HARNESS_ANTHROPIC_API_KEY) で export してください。"
        )
    return {
        "x-api-key": key,
        "anthropic-version": "2023-06-01",
        "anthropic-beta": "managed-agents-2026-04-01",
        "Content-Type": "application/json",
    }


def call(method: str, path: str, body: dict | None = None) -> dict:
    url = f"{API}{path}"
    data = json.dumps(body).encode("utf-8") if body is not None else None
    req = urllib.request.Request(url, headers=headers(), data=data, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as e:
        sys.stderr.write(f"HTTP {e.code} {method} {path}\n{e.read().decode()}\n")
        raise


def gh(args: list[str]) -> str:
    r = subprocess.run(["gh"] + args, capture_output=True, text=True, check=True)
    return r.stdout


def fetch_pr_meta(repo: str, pr_number: int) -> dict:
    pr = json.loads(gh([
        "pr", "view", str(pr_number), "--repo", repo,
        "--json", "title,body,headRefName,baseRefName,mergedAt,additions,deletions,author",
    ]))
    review_comments = json.loads(gh([
        "api", f"repos/{repo}/pulls/{pr_number}/comments",
    ]))
    return {
        "repo": repo,
        "pr_number": pr_number,
        "title": pr["title"],
        "body": pr.get("body") or "",
        "head_ref": pr["headRefName"],
        "base_ref": pr["baseRefName"],
        "merged_at": pr.get("mergedAt"),
        "additions": pr.get("additions"),
        "deletions": pr.get("deletions"),
        "author": pr.get("author", {}).get("login", ""),
        "review_comments": [
            {
                "path": c.get("path"),
                "line": c.get("line"),
                "body": c.get("body", ""),
                "user": (c.get("user") or {}).get("login", ""),
            }
            for c in review_comments
        ],
    }


def build_user_message(pr_meta: dict) -> str:
    comments_xml = "\n".join(
        f'  <comment path="{c["path"]}" line="{c.get("line", "?")}" user="{c["user"]}">{c["body"]}</comment>'
        for c in pr_meta["review_comments"]
    ) or "  <!-- no review comments -->"
    return (
        f"<pr>\n"
        f"  <repo>{pr_meta['repo']}</repo>\n"
        f"  <pr_number>{pr_meta['pr_number']}</pr_number>\n"
        f"  <title>{pr_meta['title']}</title>\n"
        f"  <body>{pr_meta['body']}</body>\n"
        f"  <head_ref>{pr_meta['head_ref']}</head_ref>\n"
        f"  <additions>{pr_meta['additions']}</additions>\n"
        f"  <deletions>{pr_meta['deletions']}</deletions>\n"
        f"</pr>\n\n"
        f"<review_comments>\n{comments_xml}\n</review_comments>\n\n"
        f"上記の PR を分析し、繰り返し指摘されそうなパターンを 1 つだけ抽出してください。\n"
        f"system prompt の指示に従い、review に delegate して最終 JSON を返してください。"
    )


def kick_agent(user_msg: str) -> tuple[str, str]:
    sess = call("POST", "/v1/sessions", {
        "agent": {"type": "agent", "id": MAIN_AGENT},
        "environment_id": ENV_ID,
        "title": "phase1-e2e",
    })
    sid = sess["id"]
    sys.stderr.write(f"[run_phase1] session_id={sid}\n")

    call("POST", f"/v1/sessions/{sid}/events", {
        "events": [{
            "type": "user.message",
            "content": [{"type": "text", "text": user_msg}],
        }],
    })

    deadline = time.time() + POLL_TIMEOUT_SEC
    last_status = None
    while time.time() < deadline:
        s = call("GET", f"/v1/sessions/{sid}")
        status = s.get("status")
        if status != last_status:
            sys.stderr.write(f"[run_phase1] status={status}\n")
            last_status = status
        if status == "idle":
            events = call("GET", f"/v1/sessions/{sid}/events")
            agent_msgs = [e for e in events.get("data", []) if e["type"] == "agent.message"]
            if agent_msgs:
                last = agent_msgs[-1]
                text = "".join(
                    b.get("text", "") for b in last.get("content", []) if b.get("type") == "text"
                )
                stats = s.get("stats", {})
                sys.stderr.write(
                    f"[run_phase1] done agent_messages={len(agent_msgs)} "
                    f"duration={stats.get('duration_seconds', 0):.1f}s\n"
                )
                return sid, text
        if status == "terminated":
            raise RuntimeError(f"session {sid} terminated")
        time.sleep(POLL_INTERVAL_SEC)
    raise RuntimeError(f"session {sid} did not complete within {POLL_TIMEOUT_SEC}s")


def extract_json(text: str) -> dict:
    # 候補 1: ```json ... ``` ブロック
    fenced = re.search(r"```(?:json)?\s*(\{.*?\})\s*```", text, re.DOTALL)
    if fenced:
        return json.loads(fenced.group(1))
    # 候補 2: テキスト中の最後の {...} ブロック
    candidates = re.findall(r"\{[^{}]*(?:\{[^{}]*\}[^{}]*)*\}", text, re.DOTALL)
    for c in reversed(candidates):
        try:
            d = json.loads(c)
            if "status" in d:
                return d
        except json.JSONDecodeError:
            continue
    raise ValueError(f"no valid JSON with 'status' found in:\n{text}")


def main() -> None:
    if len(sys.argv) != 3:
        sys.exit(f"usage: {sys.argv[0]} <owner/repo> <pr_number>")
    repo, pr_number = sys.argv[1], int(sys.argv[2])

    pr_meta = fetch_pr_meta(repo, pr_number)
    sys.stderr.write(
        f"[run_phase1] PR fetched: {repo}#{pr_number} title={pr_meta['title'][:50]!r} "
        f"reviews={len(pr_meta['review_comments'])}\n"
    )
    user_msg = build_user_message(pr_meta)
    sid, text = kick_agent(user_msg)
    candidate = extract_json(text)

    result = {
        "session_id": sid,
        "repo": repo,
        "pr_number": pr_number,
        "pr_url": f"https://github.com/{repo}/pull/{pr_number}",
        "head_ref": pr_meta["head_ref"],
        "agent_output_raw": text,
        "candidate": candidate,
    }
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
