#!/usr/bin/env python3
"""Kick the harness maintain agent for a merged PR, with auto-continue.

Creates a Managed Agent session bound to the harness vault, sends the PR URL
(which triggers maintain mode), then polls to completion. If the agent goes
idle without having emitted its final JSON ({"status": ...}), it is nudged with
a continue message up to MAX_CONTINUE times to work around the continuity issue
where the agent stalls after its first tool batch.

Auth: requires HARNESS_ANTHROPIC_API_KEY in the environment. All GitHub /
Databricks side effects are performed by the agent via the vault credentials.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

API = "https://api.anthropic.com"
MAIN_AGENT = "agent_018aTQSKpTLeXJz3cJCKaikY"
VAULT = "vlt_011CbQpj1P38ktUep8biJHVR"
ENV = "env_01AAvct6ZfPtCxBHs82DrjZy"

MAX_CONTINUE = 5
POLL_INTERVAL = 8
POLL_TIMEOUT = 2400  # 40 min per idle-wait cycle

KEY = os.environ.get("HARNESS_ANTHROPIC_API_KEY")
if not KEY:
    print("ERROR: HARNESS_ANTHROPIC_API_KEY is not set", file=sys.stderr)
    sys.exit(2)

HDRS = {
    "anthropic-beta": "managed-agents-2026-04-01",
    "anthropic-version": "2023-06-01",
    "x-api-key": KEY,
    "content-type": "application/json",
}

# Terminal session statuses.
DONE_STATUSES = {"idle", "completed", "ended"}
FAIL_STATUSES = {"failed", "error", "terminated"}


def req(method, path, body=None, timeout=120):
    data = json.dumps(body).encode() if body is not None else None
    r = urllib.request.Request(API + path, data=data, headers=HDRS, method=method)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            return json.loads(resp.read().decode())
    except urllib.error.HTTPError as e:
        # Never echo the API key; only surface the response body.
        print(f"HTTP ERROR {e.code} on {method} {path}: {e.read().decode()[:2000]}",
              file=sys.stderr)
        sys.exit(1)


def create_session():
    body = {
        "agent": {"type": "agent", "id": MAIN_AGENT},
        "vault_ids": [VAULT],
        "environment_id": ENV,
    }
    return req("POST", "/v1/sessions", body)["id"]


def send_message(sid, text):
    body = {"events": [{"type": "user.message",
                        "content": [{"type": "text", "text": text}]}]}
    return req("POST", f"/v1/sessions/{sid}/events", body)


def get_session(sid):
    return req("GET", f"/v1/sessions/{sid}")


def list_events(sid):
    out = []
    path = f"/v1/sessions/{sid}/events?limit=100"
    while True:
        d = req("GET", path)
        out.extend(d.get("data", []))
        nxt = d.get("next_page")
        if not nxt:
            break
        path = nxt if nxt.startswith("/") else f"/v1/sessions/{sid}/events?{nxt}"
    return out


def last_agent_text(sid):
    """Return the concatenated text of the most recent agent.message, or ''."""
    evs = list_events(sid)
    last = None
    for e in evs:
        if e.get("type") == "agent.message":
            last = e
    if not last:
        return ""
    parts = []
    for b in last.get("content", []):
        if b.get("type") == "text":
            parts.append(b.get("text", ""))
    return "".join(parts)


def extract_final_json(text):
    """Find a balanced {...} object that parses as JSON with a 'status' key.

    Scans every '{' as a candidate start and uses brace matching that respects
    string literals so control chars / braces inside strings do not confuse it.
    Returns the parsed dict or None.
    """
    n = len(text)
    found = None
    for i in range(n):
        if text[i] != "{":
            continue
        depth = 0
        in_str = False
        esc = False
        for j in range(i, n):
            c = text[j]
            if in_str:
                if esc:
                    esc = False
                elif c == "\\":
                    esc = True
                elif c == '"':
                    in_str = False
                continue
            if c == '"':
                in_str = True
            elif c == "{":
                depth += 1
            elif c == "}":
                depth -= 1
                if depth == 0:
                    candidate = text[i:j + 1]
                    try:
                        obj = json.loads(candidate)
                    except (ValueError, json.JSONDecodeError):
                        obj = None
                    if isinstance(obj, dict) and "status" in obj:
                        found = obj  # keep last (latest) match
                    break
    return found


def poll_until_settled(sid):
    """Poll until the session reaches a terminal status. Returns (status, session)."""
    start = time.time()
    last = None
    while time.time() - start < POLL_TIMEOUT:
        s = get_session(sid)
        st = s.get("status")
        if st != last:
            print(f"[poll] status={st} t+{int(time.time() - start)}s", flush=True)
            last = st
        if st in DONE_STATUSES or st in FAIL_STATUSES:
            return st, s
        time.sleep(POLL_INTERVAL)
    print("[poll] TIMEOUT waiting for settle", flush=True)
    return get_session(sid).get("status"), get_session(sid)


def main():
    pr_url = None
    if len(sys.argv) > 1:
        pr_url = sys.argv[1].strip()
    if not pr_url:
        pr_url = os.environ.get("PR_URL", "").strip()
    if not pr_url:
        print("ERROR: no PR URL provided (arg or PR_URL env)", file=sys.stderr)
        sys.exit(2)

    print(f"[kick] PR_URL={pr_url}", flush=True)
    sid = create_session()
    print(f"[kick] session={sid}", flush=True)

    send_message(sid, f"次の PR を分析してください: {pr_url}")

    for attempt in range(MAX_CONTINUE + 1):
        status, _ = poll_until_settled(sid)
        print(f"[kick] settled status={status} (cycle {attempt})", flush=True)

        if status in FAIL_STATUSES:
            print(f"[kick] session entered failure status: {status}", file=sys.stderr)
            txt = last_agent_text(sid)
            if txt:
                print("[kick] last agent text:\n" + txt[:3000], file=sys.stderr)
            sys.exit(1)

        text = last_agent_text(sid)
        final = extract_final_json(text)
        if final is not None:
            print("[kick] FINAL JSON detected:", flush=True)
            print(json.dumps(final, ensure_ascii=False, indent=2), flush=True)
            print("[kick] maintain completed.", flush=True)
            sys.exit(0)

        if attempt < MAX_CONTINUE:
            print(f"[kick] idle without final JSON; sending continue "
                  f"({attempt + 1}/{MAX_CONTINUE})", flush=True)
            send_message(sid, "続けて")
        else:
            print("[kick] exhausted continue attempts without final JSON",
                  file=sys.stderr)
            if text:
                print("[kick] last agent text:\n" + text[:3000], file=sys.stderr)
            sys.exit(1)


if __name__ == "__main__":
    main()
