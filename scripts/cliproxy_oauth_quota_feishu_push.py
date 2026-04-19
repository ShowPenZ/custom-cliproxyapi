#!/usr/bin/env python3
import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from datetime import datetime
from zoneinfo import ZoneInfo


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
ADMIN_SCRIPT = os.path.join(REPO_ROOT, "scripts", "cliproxy_admin.py")
UTC_PLUS_8 = ZoneInfo("Asia/Shanghai")
DEFAULT_TITLE = "Codex OAuth 上游额度推送"
DEFAULT_WEBHOOK = os.environ.get("CLIPROXY_FEISHU_WEBHOOK", "").strip()
DEFAULT_PLAN_PREFIX = os.environ.get("CLIPROXY_FEISHU_PLAN_PREFIX", "").strip().lower()
DEFAULT_PROBE_MODEL = os.environ.get("CLIPROXY_OAUTH_PROBE_MODEL", "gpt-5.4-mini").strip() or "gpt-5.4-mini"
DEFAULT_PROBE_TIMEOUT = int(os.environ.get("CLIPROXY_OAUTH_PROBE_TIMEOUT_SECONDS", "30"))


def fail(message: str, code: int = 1) -> None:
    print(f"ERROR: {message}", file=sys.stderr)
    raise SystemExit(code)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Push the live Codex OAuth quota snapshot to a Feishu bot webhook."
    )
    parser.add_argument("--webhook-url", default=DEFAULT_WEBHOOK, help="Feishu bot webhook URL")
    parser.add_argument("--title", default=DEFAULT_TITLE, help="Push title shown in the Feishu message")
    parser.add_argument(
        "--plan-prefix",
        default=DEFAULT_PLAN_PREFIX,
        help="only include rows whose plan_type starts with this prefix (for example: pro)",
    )
    parser.add_argument(
        "--probe-model",
        default=DEFAULT_PROBE_MODEL,
        help=f"model used for the live quota probe (default: {DEFAULT_PROBE_MODEL})",
    )
    parser.add_argument(
        "--probe-timeout",
        type=int,
        default=DEFAULT_PROBE_TIMEOUT,
        help=f"timeout in seconds for each live quota probe (default: {DEFAULT_PROBE_TIMEOUT})",
    )
    parser.add_argument("--print-only", action="store_true", help="print the message body without sending it")
    parser.add_argument("--json-only", action="store_true", help="print transformed JSON only")
    return parser.parse_args()


def fetch_raw_rows(probe_model: str, probe_timeout: int) -> list[dict]:
    cmd = [
        sys.executable,
        ADMIN_SCRIPT,
        "oauth-quota",
        "--probe",
        "--json",
        "--probe-model",
        probe_model,
        "--probe-timeout",
        str(max(1, probe_timeout)),
    ]
    proc = subprocess.run(cmd, cwd=REPO_ROOT, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    if proc.returncode != 0:
        stderr = (proc.stderr or "").strip()
        stdout = (proc.stdout or "").strip()
        fail(stderr or stdout or f"oauth quota probe failed with exit code {proc.returncode}")
    try:
        payload = json.loads(proc.stdout)
    except json.JSONDecodeError as exc:
        fail(f"invalid JSON from oauth quota probe: {exc}")
    if not isinstance(payload, list):
        fail("unexpected oauth quota payload: expected a JSON array")
    return payload


def is_codex_oauth_row(row: dict) -> bool:
    auth_id = str(row.get("auth_id", "")).strip().lower()
    return auth_id.startswith("codex-")


def percent_or_dash(value: object) -> int | str:
    text = str(value).strip()
    if text in {"", "-"}:
        return "-"
    try:
        return int(float(text))
    except ValueError:
        return text


def utc8_iso_or_dash(value: object) -> str:
    text = str(value).strip()
    if text in {"", "-"}:
        return "-"
    for layout in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%dT%H:%M:%S%z", "%Y-%m-%dT%H:%M:%S"):
        try:
            parsed = datetime.strptime(text, layout)
            if parsed.tzinfo is None:
                parsed = parsed.replace(tzinfo=UTC_PLUS_8)
            else:
                parsed = parsed.astimezone(UTC_PLUS_8)
            return parsed.isoformat(timespec="seconds")
        except ValueError:
            continue
    return text


def transform_row(row: dict) -> dict[str, object]:
    return {
        "account": str(row.get("account", "")).strip() or "-",
        "state": str(row.get("state", "")).strip() or "-",
        "会员到期时间": utc8_iso_or_dash(row.get("subscription_active_until", "-")),
        "5小时额度剩余": percent_or_dash(row.get("primary_remaining_percent", "-")),
        "5小时额度重置时间": utc8_iso_or_dash(row.get("primary_reset_at", "-")),
        "周额度剩余": percent_or_dash(row.get("secondary_remaining_percent", "-")),
        "周额度重置时间": utc8_iso_or_dash(row.get("secondary_reset_at", "-")),
    }


def matches_plan_prefix(row: dict, plan_prefix: str) -> bool:
    normalized = plan_prefix.strip().lower()
    if not normalized:
        return True
    plan_type = str(row.get("plan_type", "")).strip().lower()
    return plan_type.startswith(normalized)


def build_payload(raw_rows: list[dict], plan_prefix: str) -> list[dict[str, object]]:
    rows = [
        transform_row(row)
        for row in raw_rows
        if is_codex_oauth_row(row) and matches_plan_prefix(row, plan_prefix)
    ]
    rows.sort(key=lambda item: str(item.get("account", "")).lower())
    return rows


def format_percent(value: object) -> str:
    if isinstance(value, int):
        return f"{value}%"
    text = str(value).strip()
    if text in {"", "-"}:
        return "-"
    return text if text.endswith("%") else f"{text}%"


def escape_lark_md(value: object) -> str:
    text = str(value).replace("\\", "\\\\").replace("`", "\\`").strip()
    return text or "-"


def build_message(title: str, payload: list[dict[str, object]]) -> str:
    pushed_at = datetime.now(UTC_PLUS_8).isoformat(timespec="seconds")
    lines = [
        f"# {title}",
        "",
        f"- 推送时间：`{pushed_at}`",
        "- 调度：`UTC+8 10:00-23:00 每3小时一次`",
        f"- 账户数：`{len(payload)}`",
    ]
    for index, row in enumerate(payload, start=1):
        lines.extend(
            [
                "",
                f"## {index}. {escape_lark_md(row.get('account', '-'))}",
                f"- 状态：`{escape_lark_md(row.get('state', '-'))}`",
                f"- 会员到期：`{escape_lark_md(row.get('会员到期时间', '-'))}`",
                f"- 5小时额度剩余：`{escape_lark_md(format_percent(row.get('5小时额度剩余', '-')))}`",
                f"- 5小时额度重置：`{escape_lark_md(row.get('5小时额度重置时间', '-'))}`",
                f"- 周额度剩余：`{escape_lark_md(format_percent(row.get('周额度剩余', '-')))}`",
                f"- 周额度重置：`{escape_lark_md(row.get('周额度重置时间', '-'))}`",
            ]
        )
    return "\n".join(lines)


def build_feishu_card(title: str, payload: list[dict[str, object]]) -> dict[str, object]:
    pushed_at = datetime.now(UTC_PLUS_8).isoformat(timespec="seconds")
    elements: list[dict[str, object]] = [
        {
            "tag": "div",
            "text": {
                "tag": "lark_md",
                "content": (
                    f"**推送时间**：`{escape_lark_md(pushed_at)}`\n"
                    "**调度**：`UTC+8 10:00-23:00 每3小时一次`\n"
                    f"**账户数**：`{len(payload)}`"
                ),
            },
        },
        {"tag": "hr"},
    ]
    for index, row in enumerate(payload, start=1):
        elements.append(
            {
                "tag": "div",
                "text": {
                    "tag": "lark_md",
                    "content": f"**{index}. {escape_lark_md(row.get('account', '-'))}**",
                },
            }
        )
        elements.append(
            {
                "tag": "div",
                "fields": [
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**状态**\n`{escape_lark_md(row.get('state', '-'))}`",
                        },
                    },
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**会员到期**\n`{escape_lark_md(row.get('会员到期时间', '-'))}`",
                        },
                    },
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**5小时额度剩余**\n`{escape_lark_md(format_percent(row.get('5小时额度剩余', '-')))}`",
                        },
                    },
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**5小时额度重置**\n`{escape_lark_md(row.get('5小时额度重置时间', '-'))}`",
                        },
                    },
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**周额度剩余**\n`{escape_lark_md(format_percent(row.get('周额度剩余', '-')))}`",
                        },
                    },
                    {
                        "is_short": True,
                        "text": {
                            "tag": "lark_md",
                            "content": f"**周额度重置**\n`{escape_lark_md(row.get('周额度重置时间', '-'))}`",
                        },
                    },
                ],
            }
        )
        if index != len(payload):
            elements.append({"tag": "hr"})

    return {
        "msg_type": "interactive",
        "card": {
            "config": {
                "wide_screen_mode": True,
                "enable_forward": True,
            },
            "header": {
                "template": "blue",
                "title": {
                    "tag": "plain_text",
                    "content": title,
                },
            },
            "elements": elements,
        },
    }


def send_feishu(webhook_url: str, body: dict[str, object]) -> dict:
    data = json.dumps(body, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        webhook_url,
        data=data,
        headers={"Content-Type": "application/json; charset=utf-8"},
        method="POST",
    )
    try:
        with urllib.request.urlopen(request, timeout=30) as response:
            raw = response.read().decode("utf-8", errors="replace").strip()
            if not raw:
                return {}
            decoded = json.loads(raw)
            if isinstance(decoded, dict):
                return decoded
            fail(f"unexpected Feishu response body: {raw}")
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace").strip()
        fail(f"Feishu webhook returned HTTP {exc.code}: {body}")
    except urllib.error.URLError as exc:
        fail(f"failed to call Feishu webhook: {exc}")
    return {}


def check_feishu_response(response: dict) -> None:
    status_code = response.get("StatusCode")
    if status_code not in (None, 0):
        fail(f"Feishu webhook rejected the message: {json.dumps(response, ensure_ascii=False)}")


def main() -> int:
    args = parse_args()
    raw_rows = fetch_raw_rows(args.probe_model, args.probe_timeout)
    payload = build_payload(raw_rows, args.plan_prefix)
    if not payload:
        if args.plan_prefix:
            fail(f"no Codex OAuth upstream accounts matched plan prefix: {args.plan_prefix}")
        fail("no Codex OAuth upstream accounts found in oauth-quota output")

    if args.json_only:
        print(json.dumps(payload, ensure_ascii=False, indent=2))
        return 0

    message = build_message(args.title, payload)
    card = build_feishu_card(args.title, payload)
    if args.print_only:
        print(message)
        return 0

    webhook_url = str(args.webhook_url).strip()
    if not webhook_url:
        fail("webhook URL is required; pass --webhook-url or set CLIPROXY_FEISHU_WEBHOOK")

    response = send_feishu(webhook_url, card)
    check_feishu_response(response)
    print(json.dumps(response, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
