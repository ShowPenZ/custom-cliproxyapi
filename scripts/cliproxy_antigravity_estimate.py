#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import sys
import time
from datetime import datetime
from typing import Any


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ADMIN_SCRIPT = os.path.join(REPO_ROOT, "scripts", "cliproxy_admin.py")
DEFAULT_MODEL = "claude-opus-4-6-thinking"
DEFAULT_PROMPT = "Reply with OK only."
DEFAULT_TIMEOUT = 30


def run_admin(args: list[str]) -> Any:
    completed = subprocess.run(
        [sys.executable, ADMIN_SCRIPT, *args],
        cwd=REPO_ROOT,
        check=False,
        capture_output=True,
        text=True,
    )
    if completed.returncode != 0:
        stderr = completed.stderr.strip()
        stdout = completed.stdout.strip()
        detail = stderr or stdout or f"exit code {completed.returncode}"
        raise RuntimeError(detail)
    try:
        return json.loads(completed.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"failed to parse JSON output: {exc}") from exc


def read_quota(prefix: str, model: str, timeout_seconds: int) -> dict[str, Any]:
    rows = run_admin(
        [
            "antigravity-quota",
            "--prefix",
            prefix,
            "--model",
            model,
            "--timeout",
            str(timeout_seconds),
            "--json",
        ]
    )
    if not isinstance(rows, list) or not rows:
        raise RuntimeError("quota query returned no rows")
    row = rows[0]
    matched = row.get("matched_model")
    if not isinstance(matched, dict):
        raise RuntimeError("quota query returned no matched model")
    return {
        "row": row,
        "remaining_percent": matched.get("remaining_percent"),
        "used_percent": matched.get("used_percent"),
        "reset_at": matched.get("reset_at"),
        "max_output_tokens": matched.get("max_output_tokens"),
    }


def send_probe(prefix: str, model: str, prompt: str, api_key: str | None) -> dict[str, Any]:
    args = [
        "antigravity-test",
        prefix,
        "--model",
        model,
        "--prompt",
        prompt,
        "--json",
    ]
    if api_key:
        args.extend(["--api-key", api_key])
    payload = run_admin(args)
    if not isinstance(payload, dict):
        raise RuntimeError("probe returned unexpected payload")
    return payload


def extract_usage(probe: dict[str, Any]) -> tuple[int | None, int | None, int | None]:
    response = probe.get("response")
    if not isinstance(response, dict):
        return None, None, None
    usage = response.get("usage")
    if not isinstance(usage, dict):
        return None, None, None
    prompt_tokens = usage.get("prompt_tokens")
    completion_tokens = usage.get("completion_tokens")
    total_tokens = usage.get("total_tokens")
    return (
        int(prompt_tokens) if isinstance(prompt_tokens, int) else None,
        int(completion_tokens) if isinstance(completion_tokens, int) else None,
        int(total_tokens) if isinstance(total_tokens, int) else None,
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Continuously probe one Antigravity account/model and estimate how many small requests consume 1%% quota."
    )
    parser.add_argument("prefix", help="Antigravity auth prefix, e.g. ag2")
    parser.add_argument("--model", default=DEFAULT_MODEL, help=f"model name without prefix by default (default: {DEFAULT_MODEL})")
    parser.add_argument("--prompt", default=DEFAULT_PROMPT, help="small probe prompt used for each request")
    parser.add_argument("--api-key", help="optional client API key for the local proxy")
    parser.add_argument("--timeout", type=int, default=DEFAULT_TIMEOUT, help="timeout in seconds for each quota fetch")
    parser.add_argument("--sleep", type=float, default=0.0, help="sleep interval in seconds between probe requests")
    parser.add_argument("--max-requests", type=int, default=0, help="stop after N probe requests; 0 means run until quota changes")
    parser.add_argument(
        "--log-file",
        default="",
        help="optional JSONL output path; defaults to logs/antigravity-estimate-<timestamp>.jsonl",
    )
    return parser.parse_args()


def ensure_log_path(path: str) -> str:
    if path:
        resolved = os.path.abspath(path)
    else:
        os.makedirs(os.path.join(REPO_ROOT, "logs"), exist_ok=True)
        stamp = datetime.now().strftime("%Y%m%dT%H%M%S")
        resolved = os.path.join(REPO_ROOT, "logs", f"antigravity-estimate-{stamp}.jsonl")
    os.makedirs(os.path.dirname(resolved), exist_ok=True)
    return resolved


def append_jsonl(path: str, payload: dict[str, Any]) -> None:
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(payload, ensure_ascii=False) + "\n")


def main() -> int:
    args = parse_args()
    log_path = ensure_log_path(args.log_file)

    baseline = read_quota(args.prefix, args.model, args.timeout)
    baseline_remaining = baseline["remaining_percent"]
    if not isinstance(baseline_remaining, int):
        raise RuntimeError("baseline remaining_percent is unavailable")

    print(f"log_file={log_path}")
    print(f"prefix={args.prefix}")
    print(f"model={args.model}")
    print(f"baseline_left={baseline_remaining}")
    print(f"baseline_reset={baseline.get('reset_at') or '-'}")
    print(f"baseline_max_output_tokens={baseline.get('max_output_tokens')}")
    print("monitoring=started")
    sys.stdout.flush()

    append_jsonl(
        log_path,
        {
            "event": "start",
            "timestamp": datetime.now().isoformat(),
            "prefix": args.prefix,
            "model": args.model,
            "baseline_remaining_percent": baseline_remaining,
            "baseline_reset_at": baseline.get("reset_at"),
            "baseline_max_output_tokens": baseline.get("max_output_tokens"),
            "prompt": args.prompt,
        },
    )

    request_count = 0
    total_prompt_tokens = 0
    total_completion_tokens = 0
    total_tokens = 0
    current_remaining = baseline_remaining

    while True:
        if args.max_requests > 0 and request_count >= args.max_requests:
            print("monitoring=stopped reason=max_requests_reached")
            return 0

        probe = send_probe(args.prefix, args.model, args.prompt, args.api_key)
        request_count += 1
        prompt_tokens, completion_tokens, request_total_tokens = extract_usage(probe)
        if isinstance(prompt_tokens, int):
            total_prompt_tokens += prompt_tokens
        if isinstance(completion_tokens, int):
            total_completion_tokens += completion_tokens
        if isinstance(request_total_tokens, int):
            total_tokens += request_total_tokens

        quota = read_quota(args.prefix, args.model, args.timeout)
        next_remaining = quota["remaining_percent"]
        if not isinstance(next_remaining, int):
            raise RuntimeError("remaining_percent became unavailable")

        event = {
            "event": "probe",
            "timestamp": datetime.now().isoformat(),
            "request_count": request_count,
            "http_status": probe.get("http_status"),
            "prompt_tokens": prompt_tokens,
            "completion_tokens": completion_tokens,
            "request_total_tokens": request_total_tokens,
            "accum_prompt_tokens": total_prompt_tokens,
            "accum_completion_tokens": total_completion_tokens,
            "accum_total_tokens": total_tokens,
            "remaining_percent": next_remaining,
            "used_percent": quota.get("used_percent"),
            "reset_at": quota.get("reset_at"),
        }
        append_jsonl(log_path, event)

        print(
            "request=%d left=%s used=%s prompt_tokens=%s completion_tokens=%s total_tokens=%s"
            % (
                request_count,
                next_remaining,
                quota.get("used_percent"),
                "-" if prompt_tokens is None else prompt_tokens,
                "-" if completion_tokens is None else completion_tokens,
                "-" if request_total_tokens is None else request_total_tokens,
            )
        )
        sys.stdout.flush()

        if next_remaining < baseline_remaining:
            percent_drop = baseline_remaining - next_remaining
            requests_per_percent = request_count / percent_drop
            avg_total_tokens = total_tokens / request_count if request_count > 0 and total_tokens > 0 else None
            avg_prompt_tokens = total_prompt_tokens / request_count if request_count > 0 and total_prompt_tokens > 0 else None
            avg_completion_tokens = (
                total_completion_tokens / request_count if request_count > 0 and total_completion_tokens > 0 else None
            )
            remaining_request_estimate = next_remaining * requests_per_percent

            summary = {
                "event": "estimate",
                "timestamp": datetime.now().isoformat(),
                "request_count": request_count,
                "baseline_remaining_percent": baseline_remaining,
                "current_remaining_percent": next_remaining,
                "percent_drop": percent_drop,
                "requests_per_percent": requests_per_percent,
                "estimated_requests_left_at_current_prompt": remaining_request_estimate,
                "average_total_tokens": avg_total_tokens,
                "average_prompt_tokens": avg_prompt_tokens,
                "average_completion_tokens": avg_completion_tokens,
                "reset_at": quota.get("reset_at"),
            }
            append_jsonl(log_path, summary)

            print("monitoring=complete reason=quota_changed")
            print(f"percent_drop={percent_drop}")
            print(f"requests_to_consume_{percent_drop}pct={request_count}")
            print(f"estimated_requests_per_1pct={requests_per_percent:.2f}")
            print(f"estimated_requests_left_at_current_prompt={remaining_request_estimate:.2f}")
            if avg_total_tokens is not None:
                print(f"average_total_tokens_per_request={avg_total_tokens:.2f}")
            if avg_prompt_tokens is not None:
                print(f"average_prompt_tokens_per_request={avg_prompt_tokens:.2f}")
            if avg_completion_tokens is not None:
                print(f"average_completion_tokens_per_request={avg_completion_tokens:.2f}")
            print(f"reset_at={quota.get('reset_at') or '-'}")
            return 0

        current_remaining = next_remaining
        if args.sleep > 0:
            time.sleep(args.sleep)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except KeyboardInterrupt:
        print("monitoring=stopped reason=keyboard_interrupt", file=sys.stderr)
        raise SystemExit(130)
    except Exception as exc:
        print(f"error={exc}", file=sys.stderr)
        raise SystemExit(1)
