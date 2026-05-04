#!/usr/bin/env python3
import argparse
import http.client
import json
import os
import re
import secrets
import subprocess
import sys
import urllib.parse
import uuid
from datetime import UTC, datetime, timedelta, timezone
from typing import Any


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_CONTAINER = os.environ.get("CLIPROXY_CONTAINER", "cli-proxy-api")
DEFAULT_MGMT_KEY = os.environ.get(
    "CLIPROXY_MANAGEMENT_KEY",
    "mgmt-8615d984ec794432eb836228350e42ee33b4f80d26de5a3e",
)
DEFAULT_PUBLIC_API = os.environ.get("CLIPROXY_PUBLIC_API", "https://tradetd.cloud-ip.cc/v1")
LOCAL_MGMT_HOST = os.environ.get("CLIPROXY_LOCAL_MGMT_HOST", "127.0.0.1")
LOCAL_MGMT_PORT = int(os.environ.get("CLIPROXY_LOCAL_MGMT_PORT", "8317"))
DEFAULT_MODEL_EXAMPLE = os.environ.get("CLIPROXY_MODEL_EXAMPLE", "gpt-5.4-mini")
DEFAULT_LOG_DIR = os.environ.get(
    "CLIPROXY_LOG_DIR",
    os.path.join(REPO_ROOT, "logs"),
)
DEFAULT_AUTHS_DIR = os.environ.get("CLIPROXY_AUTHS_DIR", os.path.join(REPO_ROOT, "auths"))
DEFAULT_OAUTH_PROBE_TIMEOUT_SECONDS = int(os.environ.get("CLIPROXY_OAUTH_PROBE_TIMEOUT_SECONDS", "30"))
DEFAULT_OAUTH_PROBE_MODEL = os.environ.get("CLIPROXY_OAUTH_PROBE_MODEL", DEFAULT_MODEL_EXAMPLE)
DEFAULT_OAUTH_PROBE_ENDPOINT = os.environ.get(
    "CLIPROXY_OAUTH_PROBE_ENDPOINT",
    "https://chatgpt.com/backend-api/codex/responses/compact",
)
DEFAULT_OAUTH_PROBE_TEXT = os.environ.get("CLIPROXY_OAUTH_PROBE_TEXT", "Reply with OK only.")
ANTIGRAVITY_CLIENT_ID = os.environ.get(
    "CLIPROXY_ANTIGRAVITY_CLIENT_ID",
    "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com",
)
ANTIGRAVITY_CLIENT_SECRET = os.environ.get(
    "CLIPROXY_ANTIGRAVITY_CLIENT_SECRET",
    "",
)
ANTIGRAVITY_TOKEN_ENDPOINT = os.environ.get(
    "CLIPROXY_ANTIGRAVITY_TOKEN_ENDPOINT",
    "https://oauth2.googleapis.com/token",
)
ANTIGRAVITY_LOAD_CODE_ASSIST_ENDPOINT = os.environ.get(
    "CLIPROXY_ANTIGRAVITY_LOAD_CODE_ASSIST_ENDPOINT",
    "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
)
ANTIGRAVITY_FETCH_MODELS_ENDPOINTS = [
    url.strip()
    for url in os.environ.get(
        "CLIPROXY_ANTIGRAVITY_FETCH_MODELS_ENDPOINTS",
        ",".join(
            [
                "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:fetchAvailableModels",
                "https://daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
                "https://cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
            ]
        ),
    ).split(",")
    if url.strip()
]
ANTIGRAVITY_PUBLIC_MODEL_PREFIXES = ("gemini", "claude", "gpt", "image", "imagen")
ANTIGRAVITY_SUMMARY_MODEL_PRIORITY = [
    "gemini-3-flash",
    "gemini-3-pro-high",
    "claude-sonnet-4-5",
    "claude-sonnet-4-6",
    "gemini-3-pro-image",
    "gemini-2.5-flash",
    "gemini-2.5-pro",
]

LOG_SECTION_RE = re.compile(r"^=== (.+?) ===$")
AUTH_TRACE_FIELD_RE = re.compile(r"([a-z_]+)=([^\s]+)")
HTTP_STATUS_RE = re.compile(r"HTTP/\d+(?:\.\d+)?\s+(\d+)")
OAUTH_QUOTA_HEADER_FIELDS = {
    "x-codex-plan-type": "plan_type",
    "x-codex-primary-reset-after-seconds": "primary_reset_after_seconds",
    "x-codex-primary-reset-at": "primary_reset_at_epoch",
    "x-codex-primary-used-percent": "primary_used_percent",
    "x-codex-secondary-reset-after-seconds": "secondary_reset_after_seconds",
    "x-codex-secondary-reset-at": "secondary_reset_at_epoch",
    "x-codex-secondary-used-percent": "secondary_used_percent",
}
UTC_PLUS_8 = timezone(timedelta(hours=8), name="UTC+8")


def fail(message: str, code: int = 1) -> None:
    print(f"ERROR: {message}", file=sys.stderr)
    raise SystemExit(code)


def mask_key(value: str, front: int = 12, back: int = 6) -> str:
    value = str(value)
    if len(value) <= front + back + 3:
        return value
    return f"{value[:front]}...{value[-back:]}"


def sanitize_username(value: str) -> str:
    lowered = value.strip().lower()
    lowered = re.sub(r"[^a-z0-9-]+", "-", lowered)
    lowered = re.sub(r"-{2,}", "-", lowered).strip("-")
    if not lowered:
        fail("username is empty after sanitization")
    return lowered


def key_prefix_for_user(username: str) -> str:
    return f"sk-team-{sanitize_username(username)}-"


def owner_from_key(key: str) -> str:
    prefix = "sk-team-"
    if not key.startswith(prefix):
        return "manual"
    rest = key[len(prefix):]
    if "-" not in rest:
        return "manual"
    user, suffix = rest.rsplit("-", 1)
    if re.fullmatch(r"[0-9a-f]{16,}", suffix):
        return user or "manual"
    return "manual"


def normalise_iso(value: str) -> str:
    if not value:
        return "-"
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return value
    return dt.strftime("%Y-%m-%d %H:%M:%S")


def normalise_iso_in_timezone(value: str, tz: timezone) -> str:
    if not value:
        return "-"
    try:
        dt = datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError:
        return value
    if dt.tzinfo is not None:
        dt = dt.astimezone(tz)
    return dt.strftime("%Y-%m-%d %H:%M:%S")


def normalise_epoch_seconds(value: Any) -> str:
    return normalise_epoch_seconds_in_timezone(value, UTC)


def normalise_epoch_seconds_in_timezone(value: Any, tz: timezone) -> str:
    text = str(value).strip()
    if not text:
        return "-"
    try:
        seconds = int(float(text))
    except ValueError:
        return text
    return datetime.fromtimestamp(seconds, tz=tz).strftime("%Y-%m-%d %H:%M:%S")


def int_from_text(value: Any) -> int | None:
    text = str(value).strip()
    if not text:
        return None
    try:
        return int(float(text))
    except ValueError:
        return None


def remaining_percent(value: Any) -> str:
    used = int_from_text(value)
    if used is None:
        return "-"
    return str(max(0, 100 - used))


def extract_auth_trace_fields(line: str) -> dict[str, str]:
    fields: dict[str, str] = {}
    for match in AUTH_TRACE_FIELD_RE.finditer(line):
        fields[match.group(1)] = match.group(2)
    return fields


def account_from_auth_fields(fields: dict[str, str]) -> str:
    label = fields.get("label", "").strip()
    if label:
        return label

    auth_id = fields.get("auth_id", "").strip()
    if not auth_id:
        return "-"

    account = auth_id.removesuffix(".json")
    if account.startswith("codex-"):
        account = account[len("codex-"):]
    account = re.sub(r"-(free|plus|pro|team|enterprise)$", "", account)
    return account or auth_id


def blank_oauth_quota_row(account: str) -> dict[str, Any]:
    return {
        "account": account,
        "state": "-",
        "plan_type": "-",
        "account_group": "-",
        "subscription_active_until": "-",
        "last_seen": "-",
        "auth_id": "-",
        "source_log": "-",
        "primary_used_percent": "-",
        "primary_remaining_percent": "-",
        "primary_reset_at": "-",
        "primary_reset_at_epoch": "-",
        "primary_reset_after_seconds": "-",
        "secondary_used_percent": "-",
        "secondary_remaining_percent": "-",
        "secondary_reset_at": "-",
        "secondary_reset_at_epoch": "-",
        "secondary_reset_after_seconds": "-",
        "auth_index": "-",
        "auth_file_path": "-",
        "probe_model": "-",
        "probe_status_code": "-",
        "probe_error": "-",
    }


def parse_oauth_quota_log(path: str) -> dict[str, Any] | None:
    current_section = ""
    request_timestamp = ""
    auth_timestamp = ""
    response_timestamp = ""
    auth_fields: dict[str, str] = {}
    headers: dict[str, str] = {}
    in_headers = False

    with open(path, "r", encoding="utf-8", errors="replace") as handle:
        for raw_line in handle:
            line = raw_line.rstrip("\n")
            stripped = line.strip()

            section_match = LOG_SECTION_RE.match(stripped)
            if section_match:
                current_section = section_match.group(1)
                in_headers = False
                continue

            if current_section == "REQUEST INFO" and stripped.startswith("Timestamp: "):
                request_timestamp = stripped.split(":", 1)[1].strip()
                continue

            if current_section == "AUTH ROUTING RESULT":
                if stripped.startswith("Timestamp: "):
                    auth_timestamp = stripped.split(":", 1)[1].strip()
                    continue
                if "auth_type=oauth" in stripped and "succeeded" in stripped:
                    auth_fields = extract_auth_trace_fields(stripped)
                continue

            if not current_section.startswith("API RESPONSE"):
                continue

            if stripped.startswith("Timestamp: "):
                response_timestamp = stripped.split(":", 1)[1].strip()
                continue

            if stripped == "Headers:":
                in_headers = True
                continue

            if stripped == "Body:":
                break

            if not in_headers or ":" not in line:
                continue

            key, value = line.split(":", 1)
            normalized_key = key.strip().lower()
            field_name = OAUTH_QUOTA_HEADER_FIELDS.get(normalized_key)
            if field_name:
                headers[field_name] = value.strip()

    account = account_from_auth_fields(auth_fields)
    if account == "-" or not headers:
        return None

    snapshot: dict[str, Any] = {
        "account": account,
        "auth_id": auth_fields.get("auth_id", "-").strip() or "-",
        "source_log": os.path.basename(path),
        "last_seen": normalise_iso(response_timestamp or auth_timestamp or request_timestamp),
        "plan_type": headers.get("plan_type", "-").strip() or "-",
        "primary_used_percent": headers.get("primary_used_percent", "-").strip() or "-",
        "primary_reset_at_epoch": headers.get("primary_reset_at_epoch", "-").strip() or "-",
        "primary_reset_after_seconds": headers.get("primary_reset_after_seconds", "-").strip() or "-",
        "secondary_used_percent": headers.get("secondary_used_percent", "-").strip() or "-",
        "secondary_reset_at_epoch": headers.get("secondary_reset_at_epoch", "-").strip() or "-",
        "secondary_reset_after_seconds": headers.get("secondary_reset_after_seconds", "-").strip() or "-",
    }
    snapshot["primary_remaining_percent"] = remaining_percent(snapshot["primary_used_percent"])
    snapshot["primary_reset_at"] = normalise_epoch_seconds_in_timezone(snapshot["primary_reset_at_epoch"], UTC_PLUS_8)
    snapshot["secondary_remaining_percent"] = remaining_percent(snapshot["secondary_used_percent"])
    snapshot["secondary_reset_at"] = normalise_epoch_seconds_in_timezone(snapshot["secondary_reset_at_epoch"], UTC_PLUS_8)
    return snapshot


def collect_oauth_quota_rows(log_dir: str) -> list[dict[str, Any]]:
    runtime_auths = get_runtime_auths("codex")
    rows: dict[str, dict[str, Any]] = {}
    pending: set[str] = set()

    for entry in runtime_auths:
        if str(entry.get("account_type", "")).strip() != "oauth":
            continue
        account = display_account(entry)
        if not account:
            continue
        row = rows.setdefault(account, blank_oauth_quota_row(account))
        row["state"] = str(entry.get("state", "")).strip() or "-"
        runtime_plan = str(entry.get("plan_type", "")).strip()
        if runtime_plan:
            row["plan_type"] = runtime_plan
        runtime_group = str(entry.get("account_group", "")).strip()
        if runtime_group:
            row["account_group"] = runtime_group
        runtime_subscription_until = str(entry.get("subscription_active_until", "")).strip()
        if runtime_subscription_until:
            row["subscription_active_until"] = normalise_iso_in_timezone(runtime_subscription_until, UTC_PLUS_8)
        pending.add(account)

    stop_when_runtime_accounts_found = bool(pending)
    if os.path.isdir(log_dir):
        for name in sorted(os.listdir(log_dir), reverse=True):
            if not name.startswith("v1-responses-") or not name.endswith(".log"):
                continue
            snapshot = parse_oauth_quota_log(os.path.join(log_dir, name))
            if snapshot is None:
                continue
            account = str(snapshot.get("account", "")).strip()
            if not account:
                continue
            row = rows.setdefault(account, blank_oauth_quota_row(account))
            if row.get("last_seen", "-") != "-":
                continue
            for key, value in snapshot.items():
                if key == "account":
                    continue
                if str(value).strip() in {"", "-"} and str(row.get(key, "")).strip() not in {"", "-"}:
                    continue
                row[key] = value
            pending.discard(account)
            if stop_when_runtime_accounts_found and not pending:
                break

    return sorted(rows.values(), key=lambda item: str(item.get("account", "")).lower())


def collect_oauth_auth_files() -> list[dict[str, Any]]:
    auth_files = get_auth_files("codex")
    out: list[dict[str, Any]] = []
    for entry in auth_files:
        if str(entry.get("account_type", "")).strip() != "oauth":
            continue
        account = display_account(entry)
        if not account:
            continue
        out.append(entry)
    return out


def merge_oauth_auth_file_rows(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    merged: dict[str, dict[str, Any]] = {
        str(row.get("account", "")).strip(): dict(row)
        for row in rows
        if str(row.get("account", "")).strip()
    }

    for entry in collect_oauth_auth_files():
        account = display_account(entry)
        if not account:
            continue
        row = merged.setdefault(account, blank_oauth_quota_row(account))
        auth_id = str(entry.get("id", "")).strip()
        auth_index = str(entry.get("auth_index", "")).strip()
        auth_path = str(entry.get("path", "")).strip()
        if auth_id and str(row.get("auth_id", "")).strip() in {"", "-"}:
            row["auth_id"] = auth_id
        if auth_index:
            row["auth_index"] = auth_index
        if auth_path:
            row["auth_file_path"] = auth_path
        if str(row.get("plan_type", "")).strip() in {"", "-"}:
            plan_type = str(entry.get("id_token", {}).get("plan_type", "")).strip()
            if plan_type:
                row["plan_type"] = plan_type
        if str(row.get("account_group", "")).strip() in {"", "-"}:
            account_group = str(entry.get("account_group", "")).strip()
            if account_group:
                row["account_group"] = account_group
        if str(row.get("subscription_active_until", "")).strip() in {"", "-"}:
            subscription_until = str(entry.get("id_token", {}).get("chatgpt_subscription_active_until", "")).strip()
            if subscription_until:
                row["subscription_active_until"] = normalise_iso_in_timezone(subscription_until, UTC_PLUS_8)

    return sorted(merged.values(), key=lambda item: str(item.get("account", "")).lower())


def build_oauth_probe_body(model: str) -> str:
    payload = {
        "model": model,
        "instructions": "",
        "input": [
            {
                "role": "user",
                "content": [
                    {
                        "type": "input_text",
                        "text": DEFAULT_OAUTH_PROBE_TEXT,
                    }
                ],
            }
        ],
    }
    return json.dumps(payload, ensure_ascii=False, separators=(",", ":"))


def parse_oauth_probe_output(output: str, model: str) -> dict[str, Any]:
    headers: dict[str, str] = {}
    status_code = "-"
    error_line = "-"

    for line in output.splitlines():
        stripped = line.rstrip()
        if not stripped:
            continue

        status_match = HTTP_STATUS_RE.search(stripped)
        if status_match:
            status_code = status_match.group(1)
            continue

        if stripped.lower().startswith("wget:"):
            error_line = stripped
            continue

        if ":" not in stripped:
            continue
        key, value = stripped.split(":", 1)
        normalized_key = key.strip().lower()
        field_name = OAUTH_QUOTA_HEADER_FIELDS.get(normalized_key)
        if field_name:
            headers[field_name] = value.strip()

    snapshot: dict[str, Any] = {
        "probe_model": model,
        "probe_status_code": status_code,
        "probe_error": error_line,
    }
    if headers:
        snapshot["source_log"] = "live-probe"
        snapshot["last_seen"] = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
        snapshot["plan_type"] = headers.get("plan_type", "-").strip() or "-"
        snapshot["primary_used_percent"] = headers.get("primary_used_percent", "-").strip() or "-"
        snapshot["primary_remaining_percent"] = remaining_percent(snapshot["primary_used_percent"])
        snapshot["primary_reset_at_epoch"] = headers.get("primary_reset_at_epoch", "-").strip() or "-"
        snapshot["primary_reset_at"] = normalise_epoch_seconds_in_timezone(snapshot["primary_reset_at_epoch"], UTC_PLUS_8)
        snapshot["primary_reset_after_seconds"] = headers.get("primary_reset_after_seconds", "-").strip() or "-"
        snapshot["secondary_used_percent"] = headers.get("secondary_used_percent", "-").strip() or "-"
        snapshot["secondary_remaining_percent"] = remaining_percent(snapshot["secondary_used_percent"])
        snapshot["secondary_reset_at_epoch"] = headers.get("secondary_reset_at_epoch", "-").strip() or "-"
        snapshot["secondary_reset_at"] = normalise_epoch_seconds_in_timezone(snapshot["secondary_reset_at_epoch"], UTC_PLUS_8)
        snapshot["secondary_reset_after_seconds"] = headers.get("secondary_reset_after_seconds", "-").strip() or "-"
        snapshot["probe_error"] = "-"
    return snapshot


def probe_oauth_quota_row(row: dict[str, Any], model: str, timeout_seconds: int) -> dict[str, Any]:
    auth_path = str(row.get("auth_file_path", "")).strip()
    if not auth_path or auth_path == "-":
        return {"probe_model": model, "probe_status_code": "-", "probe_error": "auth file path unavailable"}

    body = build_oauth_probe_body(model)
    session_id = f"probe-{uuid.uuid4().hex}"
    script = r'''
f="$PROBE_PATH"
tok=$(tr -d '\n' < "$f" | sed -n 's/.*"access_token"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
acct=$(tr -d '\n' < "$f" | sed -n 's/.*"account_id"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
if [ -z "$tok" ]; then
  echo "wget: access_token not found"
  exit 3
fi
if [ -z "$acct" ]; then
  echo "wget: account_id not found"
  exit 4
fi
wget -qS -O - \
  --header "Authorization: Bearer $tok" \
  --header "Content-Type: application/json" \
  --header "Accept: application/json" \
  --header "Connection: Keep-Alive" \
  --header "Originator: codex_cli_rs" \
  --header "User-Agent: codex_cli_rs/0.116.0 (Mac OS 26.0.1; arm64) Apple_Terminal/464" \
  --header "Chatgpt-Account-Id: $acct" \
  --header "Session_id: $PROBE_SESSION" \
  --header "X-Client-Request-Id: $PROBE_SESSION" \
  --post-data "$PROBE_BODY" \
  "$PROBE_ENDPOINT" 2>&1
'''
    cmd = [
        "docker",
        "exec",
        "-e",
        f"PROBE_PATH={auth_path}",
        "-e",
        f"PROBE_BODY={body}",
        "-e",
        f"PROBE_SESSION={session_id}",
        "-e",
        f"PROBE_ENDPOINT={DEFAULT_OAUTH_PROBE_ENDPOINT}",
        DEFAULT_CONTAINER,
        "/bin/sh",
        "-ec",
        script,
    ]

    try:
        proc = subprocess.run(
            cmd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            timeout=max(1, timeout_seconds),
            check=False,
        )
    except subprocess.TimeoutExpired:
        return {"probe_model": model, "probe_status_code": "-", "probe_error": f"probe timed out after {timeout_seconds}s"}

    output = proc.stdout or ""
    if proc.stderr:
        output = output + ("\n" if output else "") + proc.stderr

    snapshot = parse_oauth_probe_output(output, model)
    has_quota = any(str(snapshot.get(key, "")).strip() not in {"", "-"} for key in ("primary_used_percent", "secondary_used_percent"))
    if not has_quota and proc.returncode != 0 and str(snapshot.get("probe_error", "")).strip() in {"", "-"}:
        snapshot["probe_error"] = f"probe exited with code {proc.returncode}"
    return snapshot


def apply_oauth_probe(rows: list[dict[str, Any]], model: str, timeout_seconds: int) -> list[dict[str, Any]]:
    for row in rows:
        snapshot = probe_oauth_quota_row(row, model, timeout_seconds)
        for key, value in snapshot.items():
            if str(value).strip() in {"", "-"} and str(row.get(key, "")).strip() not in {"", "-"}:
                continue
            row[key] = value
    return rows


def decode_chunked(data: bytes) -> bytes:
    out = bytearray()
    i = 0
    while True:
        j = data.find(b"\r\n", i)
        if j < 0:
            return bytes(out)
        size_line = data[i:j].split(b";", 1)[0].strip()
        if not size_line:
            i = j + 2
            continue
        size = int(size_line, 16)
        i = j + 2
        if size == 0:
            return bytes(out)
        out.extend(data[i:i + size])
        i += size + 2


def raw_http(method: str, path: str, payload: Any = None) -> tuple[int, dict[str, str], bytes]:
    body = b""
    if payload is not None:
        body = json.dumps(payload, separators=(",", ":")).encode()

    headers = [
        f"{method} {path} HTTP/1.1",
        f"Host: {LOCAL_MGMT_HOST}",
        f"Authorization: Bearer {DEFAULT_MGMT_KEY}",
        "Accept: application/json",
        "Connection: close",
    ]
    if body:
        headers.extend(
            [
                "Content-Type: application/json",
                f"Content-Length: {len(body)}",
            ]
        )

    request = ("\r\n".join(headers) + "\r\n\r\n").encode() + body
    cmd = [
        "docker",
        "exec",
        "-i",
        DEFAULT_CONTAINER,
        "/usr/bin/nc",
        LOCAL_MGMT_HOST,
        str(LOCAL_MGMT_PORT),
    ]
    proc = subprocess.run(cmd, input=request, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False)
    if proc.returncode != 0:
        fail(f"docker exec nc failed: {proc.stderr.decode().strip() or proc.stdout.decode().strip()}")

    response = proc.stdout
    sep = b"\r\n\r\n"
    if sep not in response:
        fail(f"invalid HTTP response from management API: {response.decode(errors='replace')}")

    header_blob, body_blob = response.split(sep, 1)
    header_lines = header_blob.decode(errors="replace").split("\r\n")
    if not header_lines:
        fail("empty HTTP response from management API")

    status_match = re.match(r"HTTP/\d+\.\d+\s+(\d+)", header_lines[0])
    if not status_match:
        fail(f"invalid HTTP status line: {header_lines[0]}")
    status_code = int(status_match.group(1))

    headers_out: dict[str, str] = {}
    for line in header_lines[1:]:
        if ":" not in line:
            continue
        key, value = line.split(":", 1)
        headers_out[key.strip().lower()] = value.strip()

    if headers_out.get("transfer-encoding", "").lower() == "chunked":
        body_blob = decode_chunked(body_blob)

    return status_code, headers_out, body_blob


def api_json(method: str, path: str, payload: Any = None) -> Any:
    status, headers, body = raw_http(method, path, payload)
    text = body.decode(errors="replace").strip()
    if not 200 <= status < 300:
        fail(f"{method} {path} failed with HTTP {status}: {text}")
    if not text:
        return {}
    ctype = headers.get("content-type", "")
    if "json" in ctype or text.startswith("{") or text.startswith("["):
        return json.loads(text)
    return text


def scalar_or_dash(value: Any) -> str:
    if value is None:
        return "-"
    text = str(value).strip()
    return text or "-"


def fetch_management_oauth_quota_rows(probe: bool, model: str, timeout_seconds: int) -> list[dict[str, Any]] | None:
    query = []
    if probe:
        query.append("probe=1")
        query.append("refresh=1")
    if model.strip():
        query.append(f"model={urllib.parse.quote(model.strip(), safe='')}")
    if timeout_seconds > 0:
        query.append(f"timeout_seconds={max(1, int(timeout_seconds))}")

    path = "/v0/management/oauth-quota"
    if query:
        path += "?" + "&".join(query)

    url = f"http://127.0.0.1:{LOCAL_MGMT_PORT}{path}"
    cmd = [
        "docker",
        "exec",
        "-e",
        f"CLIPROXY_LOCAL_MGMT_URL={url}",
        "-e",
        f"CLIPROXY_LOCAL_MGMT_KEY={DEFAULT_MGMT_KEY}",
        DEFAULT_CONTAINER,
        "/bin/sh",
        "-lc",
        'wget -qO- --header "Authorization: Bearer $CLIPROXY_LOCAL_MGMT_KEY" --header "Accept: application/json" "$CLIPROXY_LOCAL_MGMT_URL"',
    ]
    proc = subprocess.run(cmd, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, check=False)
    text = (proc.stdout or "").strip()
    stderr = (proc.stderr or "").strip()
    if proc.returncode != 0:
        if "404" in stderr:
            return None
        fail(stderr or f"GET {path} failed with exit code {proc.returncode}")
    if not text:
        return []
    if not text.startswith("{"):
        fail(f"unexpected oauth-quota response: {text}")

    payload = json.loads(text)
    accounts = payload.get("accounts", [])
    if not isinstance(accounts, list):
        fail("unexpected oauth-quota response: accounts is not a list")
    return [normalise_management_oauth_quota_row(entry) for entry in accounts if isinstance(entry, dict)]


def normalise_management_oauth_quota_row(entry: dict[str, Any]) -> dict[str, Any]:
    account = scalar_or_dash(entry.get("account"))
    row = blank_oauth_quota_row("-" if account == "-" else account)
    row["account"] = account
    row["state"] = scalar_or_dash(entry.get("state"))
    row["plan_type"] = scalar_or_dash(entry.get("plan_type"))
    row["account_group"] = scalar_or_dash(entry.get("account_group"))

    subscription_until = str(entry.get("subscription_active_until", "")).strip()
    if subscription_until:
        row["subscription_active_until"] = normalise_iso_in_timezone(subscription_until, UTC_PLUS_8)

    row["auth_id"] = scalar_or_dash(entry.get("auth_id"))
    row["source_log"] = scalar_or_dash(entry.get("source"))
    row["primary_used_percent"] = scalar_or_dash(entry.get("primary_used_percent"))
    row["primary_remaining_percent"] = scalar_or_dash(entry.get("primary_remaining_percent"))

    primary_reset_at = str(entry.get("primary_reset_at", "")).strip()
    if primary_reset_at:
        row["primary_reset_at"] = normalise_iso_in_timezone(primary_reset_at, UTC_PLUS_8)
    row["primary_reset_after_seconds"] = scalar_or_dash(entry.get("primary_reset_after_seconds"))

    row["secondary_used_percent"] = scalar_or_dash(entry.get("secondary_used_percent"))
    row["secondary_remaining_percent"] = scalar_or_dash(entry.get("secondary_remaining_percent"))

    secondary_reset_at = str(entry.get("secondary_reset_at", "")).strip()
    if secondary_reset_at:
        row["secondary_reset_at"] = normalise_iso_in_timezone(secondary_reset_at, UTC_PLUS_8)
    row["secondary_reset_after_seconds"] = scalar_or_dash(entry.get("secondary_reset_after_seconds"))

    last_seen = str(entry.get("last_seen", "")).strip()
    if last_seen:
        row["last_seen"] = normalise_iso_in_timezone(last_seen, UTC_PLUS_8)

    row["probe_model"] = scalar_or_dash(entry.get("probe_model"))
    row["probe_status_code"] = scalar_or_dash(entry.get("probe_status_code"))
    row["probe_error"] = scalar_or_dash(entry.get("probe_error"))
    return row


def parse_datetime_guess(value: Any) -> datetime | None:
    text = str(value).strip()
    if not text or text == "-":
        return None
    for layout in ("%Y-%m-%d %H:%M:%S", "%Y-%m-%dT%H:%M:%S%z", "%Y-%m-%dT%H:%M:%S", "%Y-%m-%dT%H:%M:%S.%f%z", "%Y-%m-%dT%H:%M:%S.%f"):
        try:
            dt = datetime.strptime(text, layout)
            if dt.tzinfo is None:
                dt = dt.replace(tzinfo=UTC_PLUS_8)
            return dt
        except ValueError:
            continue
    try:
        dt = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    if dt.tzinfo is None:
        dt = dt.replace(tzinfo=UTC_PLUS_8)
    return dt


def hide_stale_subscription_until(rows: list[dict[str, Any]]) -> list[dict[str, Any]]:
    now = datetime.now(UTC_PLUS_8)
    for row in rows:
        raw_value = str(row.get("subscription_active_until", "")).strip()
        subscription_until = parse_datetime_guess(row.get("subscription_active_until"))
        if subscription_until is None or subscription_until > now:
            continue
        if str(row.get("state", "")).strip().lower() != "online":
            continue
        source = str(row.get("source_log", "")).strip().lower()
        has_live_quota = any(
            str(row.get(key, "")).strip() not in {"", "-"}
            for key in ("primary_used_percent", "secondary_used_percent")
        )
        if source != "live-probe" or not has_live_quota:
            continue
        if raw_value not in {"", "-"}:
            row["subscription_active_until"] = f"已过期（{raw_value}）"
        else:
            row["subscription_active_until"] = "已过期"
    return rows


def raw_local_api(method: str, path: str, bearer_token: str, payload: Any = None) -> tuple[int, dict[str, str], bytes]:
    body = b""
    if payload is not None:
        body = json.dumps(payload, separators=(",", ":")).encode()

    conn = http.client.HTTPConnection(LOCAL_MGMT_HOST, LOCAL_MGMT_PORT, timeout=30)
    headers = {
        "Authorization": f"Bearer {bearer_token}",
        "Accept": "application/json",
        "Connection": "close",
    }
    if body:
        headers["Content-Type"] = "application/json"

    try:
        conn.request(method, path, body=body if body else None, headers=headers)
        resp = conn.getresponse()
        status = resp.status
        resp_headers = {key.lower(): value for key, value in resp.getheaders()}
        resp_body = resp.read()
        return status, resp_headers, resp_body
    finally:
        conn.close()


def local_api_json(method: str, path: str, bearer_token: str, payload: Any = None) -> tuple[int, Any]:
    status, headers, body = raw_local_api(method, path, bearer_token, payload)
    text = body.decode(errors="replace").strip()
    if not text:
        return status, {}
    ctype = headers.get("content-type", "")
    if "json" in ctype or text.startswith("{") or text.startswith("["):
        return status, json.loads(text)
    return status, text


def get_api_keys() -> list[str]:
    payload = api_json("GET", "/v0/management/api-keys")
    return list(payload.get("api-keys", []))


def put_api_keys(keys: list[str]) -> Any:
    return api_json("PUT", "/v0/management/api-keys", keys)


def get_usage_enabled() -> bool:
    payload = api_json("GET", "/v0/management/usage-statistics-enabled")
    return bool(payload.get("usage-statistics-enabled", False))


def get_usage() -> dict[str, Any]:
    payload = api_json("GET", "/v0/management/usage")
    return dict(payload.get("usage", {}))


def get_routing_strategy() -> str:
    payload = api_json("GET", "/v0/management/routing/strategy")
    return str(payload.get("strategy", "")).strip() or "round-robin"


def put_routing_strategy(strategy: str) -> Any:
    normalized = strategy.strip().lower()
    if normalized in {"rr", "roundrobin", "round-robin", ""}:
        normalized = "round-robin"
    elif normalized in {"ff", "fillfirst", "fill-first"}:
        normalized = "fill-first"
    else:
        fail(f"unsupported routing strategy: {strategy}")
    return api_json("PUT", "/v0/management/routing/strategy", {"value": normalized})


def get_codex_keys() -> list[dict[str, Any]]:
    payload = api_json("GET", "/v0/management/codex-api-key")
    return list(payload.get("codex-api-key", []))


def put_codex_keys(entries: list[dict[str, Any]]) -> Any:
    return api_json("PUT", "/v0/management/codex-api-key", entries)


def get_runtime_auths(provider: str | None = None) -> list[dict[str, Any]]:
    path = "/v0/management/runtime-auths"
    if provider:
        path += f"?provider={provider.strip()}"
    payload = api_json("GET", path)
    return list(payload.get("auths", []))


def patch_auth_status(name: str, disabled: bool) -> Any:
    return api_json("PATCH", "/v0/management/auth-files/status", {"name": name, "disabled": disabled})


def get_auth_files(provider: str | None = None) -> list[dict[str, Any]]:
    path = "/v0/management/auth-files"
    if provider:
        path += f"?provider={provider.strip()}"
    payload = api_json("GET", path)
    return list(payload.get("files", []))


def generate_key(username: str) -> str:
    return f"{key_prefix_for_user(username)}{secrets.token_hex(16)}"


def resolve_matches(keys: list[str], identifier: str) -> list[str]:
    identifier = identifier.strip()
    if not identifier:
        fail("identifier is empty")
    if identifier in keys:
        return [identifier]
    if identifier.startswith("sk-"):
        return [key for key in keys if key.startswith(identifier)]
    prefix = key_prefix_for_user(identifier)
    return [key for key in keys if key.startswith(prefix)]


def command_add(args: argparse.Namespace) -> int:
    username = sanitize_username(args.username)
    keys = get_api_keys()
    prefix = key_prefix_for_user(username)
    existing_user_keys = [key for key in keys if key.startswith(prefix)]

    if existing_user_keys and not args.replace:
        print(f"user '{username}' already has {len(existing_user_keys)} key(s):", file=sys.stderr)
        for key in existing_user_keys:
            print(f"  {mask_key(key)}", file=sys.stderr)
        print("use --replace to rotate the key", file=sys.stderr)
        return 1

    new_key = args.key.strip() if args.key else generate_key(username)
    if args.replace:
        keys = [key for key in keys if not key.startswith(prefix)]
    if new_key not in keys:
        keys.append(new_key)
        put_api_keys(keys)

    print(f"User: {username}")
    print(f"API Key: {new_key if args.show_full else mask_key(new_key)}")
    print(f"Base URL: {DEFAULT_PUBLIC_API}")
    print(f"Model example: {DEFAULT_MODEL_EXAMPLE}")
    return 0


def command_revoke(args: argparse.Namespace) -> int:
    keys = get_api_keys()
    matches = resolve_matches(keys, args.identifier)
    if not matches:
        print(f"no keys matched: {args.identifier}", file=sys.stderr)
        return 1

    remaining = [key for key in keys if key not in set(matches)]
    put_api_keys(remaining)

    print(f"Revoked {len(matches)} key(s):")
    for key in matches:
        print(f"  {mask_key(key)}")
    return 0


def command_list(args: argparse.Namespace) -> int:
    keys = get_api_keys()
    if not keys:
        print("No client API keys configured.")
        return 0

    print(f"{'OWNER':<20} {'KEY'}")
    for key in keys:
        shown = key if args.full else mask_key(key)
        print(f"{owner_from_key(key):<20} {shown}")
    return 0


def iter_filtered_usage(usage: dict[str, Any], identifier: str | None) -> dict[str, Any]:
    apis = dict(usage.get("apis", {}))
    if not identifier:
        return apis
    matches = resolve_matches(list(apis.keys()), identifier)
    return {key: apis[key] for key in matches if key in apis}


def last_seen_for_api(api_snapshot: dict[str, Any]) -> str:
    latest = ""
    for model_data in api_snapshot.get("models", {}).values():
        for detail in model_data.get("details", []):
            ts = str(detail.get("timestamp", ""))
            if ts > latest:
                latest = ts
    return normalise_iso(latest)


def command_usage(args: argparse.Namespace) -> int:
    enabled = get_usage_enabled()
    if not enabled:
        print("Usage statistics are disabled in config.", file=sys.stderr)
        return 1

    usage = get_usage()
    filtered = iter_filtered_usage(usage, args.identifier)

    if args.json:
        print(json.dumps(filtered, ensure_ascii=False, indent=2))
        return 0

    print("Usage note: this is proxy-observed in-memory usage by client API key.")
    print("It is not the upstream provider's remaining balance/quota.")
    print("Stats may reset after proxy restart unless you export/import them manually.")

    if not filtered:
        print("No usage data for the requested key(s) yet.")
        return 0

    print()
    print(f"{'OWNER':<20} {'REQ':>5} {'TOKENS':>10} {'MODELS':>6} {'LAST SEEN':<19} {'KEY'}")
    items = sorted(filtered.items(), key=lambda item: int(item[1].get("total_tokens", 0)), reverse=True)
    for key, snapshot in items:
        total_requests = int(snapshot.get("total_requests", 0))
        total_tokens = int(snapshot.get("total_tokens", 0))
        model_count = len(snapshot.get("models", {}))
        shown_key = key if args.full else mask_key(key)
        print(
            f"{owner_from_key(key):<20} "
            f"{total_requests:>5} "
            f"{total_tokens:>10} "
            f"{model_count:>6} "
            f"{last_seen_for_api(snapshot):<19} "
            f"{shown_key}"
        )
        if args.details:
            models = snapshot.get("models", {})
            for model_name, model_data in sorted(models.items()):
                req = int(model_data.get("total_requests", 0))
                tok = int(model_data.get("total_tokens", 0))
                print(f"  - {model_name}: requests={req}, tokens={tok}")
    return 0


def copy_codex_models(entries: list[dict[str, Any]], source_index: int | None = None) -> list[dict[str, str]]:
    if not entries:
        fail("no existing codex upstream found; provide models explicitly in config first")
    index = source_index if source_index is not None else 0
    if index < 0 or index >= len(entries):
        fail(f"copy-models-from index out of range: {index}")
    models = entries[index].get("models", [])
    if not models:
        fail(f"codex upstream at index {index} has no model mappings to copy")
    copied: list[dict[str, str]] = []
    for item in models:
        name = str(item.get("name", "")).strip()
        alias = str(item.get("alias", "")).strip()
        if not name or not alias:
            continue
        copied.append({"name": name, "alias": alias})
    if not copied:
        fail(f"codex upstream at index {index} has no valid model mappings to copy")
    return copied


def resolve_codex_entry_matches(entries: list[dict[str, Any]], identifier: str) -> list[tuple[int, dict[str, Any]]]:
    identifier = identifier.strip()
    if not identifier:
        fail("identifier is empty")

    matches: list[tuple[int, dict[str, Any]]] = []
    for idx, entry in enumerate(entries):
        api_key = str(entry.get("api-key", "")).strip()
        prefix = str(entry.get("prefix", "")).strip()
        if identifier.isdigit() and idx == int(identifier):
            matches.append((idx, entry))
            continue
        if api_key == identifier or (identifier.startswith("sk-") and api_key.startswith(identifier)):
            matches.append((idx, entry))
            continue
        if prefix and prefix == identifier:
            matches.append((idx, entry))
    return matches


def command_routing(args: argparse.Namespace) -> int:
    if not args.strategy:
        print(get_routing_strategy())
        return 0
    put_routing_strategy(args.strategy)
    print(get_routing_strategy())
    return 0


def command_list_codex(args: argparse.Namespace) -> int:
    entries = get_codex_keys()
    if not entries:
        print("No codex upstream API-key entries configured.")
        return 0

    strategy = get_routing_strategy()
    print(f"Routing: {strategy}")
    print(f"{'INDEX':<5} {'PREFIX':<12} {'MODELS':>6} {'BASE URL':<30} {'API KEY'}")
    for idx, entry in enumerate(entries):
        prefix = str(entry.get("prefix", "")).strip() or "-"
        base_url = str(entry.get("base-url", "")).strip() or "-"
        api_key = str(entry.get("api-key", "")).strip()
        shown_key = api_key if args.full else mask_key(api_key)
        model_count = len(entry.get("models", []) or [])
        print(f"{idx:<5} {prefix:<12} {model_count:>6} {base_url:<30} {shown_key}")
    return 0


def command_add_codex(args: argparse.Namespace) -> int:
    entries = get_codex_keys()
    api_key = args.api_key.strip()
    if not api_key:
        fail("api key is required")

    matches = resolve_codex_entry_matches(entries, api_key)
    if matches and not args.replace:
        print(f"codex upstream already exists for {mask_key(api_key)}", file=sys.stderr)
        print("use --replace to overwrite the existing entry", file=sys.stderr)
        return 1

    base_url = args.base_url.strip() if args.base_url else ""
    if not base_url and entries:
        base_url = str(entries[0].get("base-url", "")).strip()
    if not base_url:
        fail("base-url is required when there is no existing codex upstream to copy from")

    models = copy_codex_models(entries, args.copy_models_from)
    new_entry: dict[str, Any] = {
        "api-key": api_key,
        "base-url": base_url,
        "proxy-url": args.proxy_url.strip() if args.proxy_url else "",
        "models": models,
    }
    if args.prefix:
        new_entry["prefix"] = args.prefix.strip()
    if args.websockets:
        new_entry["websockets"] = True

    remaining = list(entries)
    if args.replace and matches:
        remove_indexes = {idx for idx, _ in matches}
        remaining = [entry for idx, entry in enumerate(entries) if idx not in remove_indexes]
    remaining.append(new_entry)
    put_codex_keys(remaining)

    print("Added codex upstream:")
    print(f"  Prefix: {new_entry.get('prefix', '-') or '-'}")
    print(f"  Base URL: {base_url}")
    print(f"  Models: {len(models)} copied")
    print(f"  API Key: {api_key if args.show_full else mask_key(api_key)}")
    return 0


def command_remove_codex(args: argparse.Namespace) -> int:
    entries = get_codex_keys()
    matches = resolve_codex_entry_matches(entries, args.identifier)
    if not matches:
        print(f"no codex upstream matched: {args.identifier}", file=sys.stderr)
        return 1

    remove_indexes = {idx for idx, _ in matches}
    remaining = [entry for idx, entry in enumerate(entries) if idx not in remove_indexes]
    put_codex_keys(remaining)

    print(f"Removed {len(matches)} codex upstream entrie(s):")
    for idx, entry in matches:
        prefix = str(entry.get("prefix", "")).strip() or "-"
        api_key = str(entry.get("api-key", "")).strip()
        print(f"  index={idx} prefix={prefix} api_key={mask_key(api_key)}")
    return 0


def display_account(entry: dict[str, Any]) -> str:
    account = str(entry.get("account", "")).strip()
    if account:
        return account
    label = str(entry.get("label", "")).strip()
    if label:
        return label
    return str(entry.get("id", "")).strip()


def match_auth_identifier(entry: dict[str, Any], identifier: str) -> bool:
    identifier = identifier.strip().lower()
    if not identifier:
        return False

    candidates = {
        str(entry.get("id", "")).strip().lower(),
        str(entry.get("prefix", "")).strip().lower(),
        str(entry.get("email", "")).strip().lower(),
        str(entry.get("account", "")).strip().lower(),
        str(entry.get("label", "")).strip().lower(),
    }
    return identifier in candidates


def resolve_antigravity_auth(identifier: str) -> dict[str, Any]:
    auths = get_runtime_auths("antigravity")
    identifier = identifier.strip()
    if not identifier:
        fail("identifier is empty")

    matches = [entry for entry in auths if match_auth_identifier(entry, identifier)]
    if not matches:
        fail(f"no antigravity auth matched: {identifier}")
    if len(matches) > 1:
        joined = ", ".join(str(entry.get("id", "")).strip() for entry in matches)
        fail(f"multiple antigravity auths matched {identifier}: {joined}")
    return matches[0]


def extract_error_type(message: Any) -> str:
    text = str(message or "").strip()
    if not text:
        return "-"
    try:
        payload = json.loads(text)
    except json.JSONDecodeError:
        payload = None

    if isinstance(payload, dict):
        error = payload.get("error")
        if isinstance(error, dict):
            for detail in error.get("details", []):
                if isinstance(detail, dict):
                    reason = str(detail.get("reason", "")).strip()
                    if reason:
                        return reason
            for key in ("status", "code", "message"):
                value = str(error.get(key, "")).strip()
                if value:
                    return value

    for pattern in (
        r'"reason"\s*:\s*"([^"]+)"',
        r'"status"\s*:\s*"([^"]+)"',
        r'"code"\s*:\s*"?([^",}\s]+)"?',
    ):
        match = re.search(pattern, text, flags=re.IGNORECASE)
        if match:
            return match.group(1).strip()
    if "verify your account" in text.lower():
        return "VALIDATION_REQUIRED"
    return text.splitlines()[0][:80]


def compact_message(message: Any, limit: int = 80) -> str:
    text = " ".join(str(message or "").split())
    if not text:
        return "-"
    if len(text) <= limit:
        return text
    return text[: limit - 3] + "..."


def parse_iso_datetime(value: Any) -> datetime | None:
    text = str(value or "").strip()
    if not text:
        return None
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    if parsed.tzinfo is None:
        return parsed.replace(tzinfo=UTC)
    return parsed


def json_from_bytes(raw: bytes) -> Any:
    if not raw:
        return None
    try:
        return json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        return None


def response_text(raw: bytes, parsed: Any) -> str:
    if isinstance(parsed, (dict, list)):
        return json.dumps(parsed, ensure_ascii=False)
    if not raw:
        return ""
    return raw.decode("utf-8", errors="replace").strip()


def request_url(
    method: str,
    url: str,
    *,
    headers: dict[str, str] | None = None,
    payload: Any | None = None,
    form: dict[str, Any] | None = None,
    timeout: int = 30,
) -> tuple[int, dict[str, str], bytes, Any]:
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in {"http", "https"}:
        raise RuntimeError(f"unsupported URL scheme: {parsed.scheme or '-'}")

    path = parsed.path or "/"
    if parsed.query:
        path += f"?{parsed.query}"

    body: bytes | None = None
    request_headers = dict(headers or {})
    if payload is not None:
        body = json.dumps(payload).encode("utf-8")
        request_headers.setdefault("Content-Type", "application/json")
    elif form is not None:
        body = urllib.parse.urlencode(form).encode("utf-8")
        request_headers.setdefault("Content-Type", "application/x-www-form-urlencoded")

    conn_class = http.client.HTTPSConnection if parsed.scheme == "https" else http.client.HTTPConnection
    conn = conn_class(parsed.hostname, parsed.port, timeout=timeout)
    try:
        conn.request(method.upper(), path, body=body, headers=request_headers)
        resp = conn.getresponse()
        raw = resp.read()
        return resp.status, {k.lower(): v for k, v in resp.getheaders()}, raw, json_from_bytes(raw)
    finally:
        conn.close()


def auth_file_path(auth_id: str) -> str:
    name = os.path.basename(str(auth_id or "").strip())
    if not name:
        raise RuntimeError("auth id is empty")
    return os.path.join(DEFAULT_AUTHS_DIR, name)


def load_json_file(path: str) -> dict[str, Any]:
    with open(path, "r", encoding="utf-8") as handle:
        payload = json.load(handle)
    if not isinstance(payload, dict):
        raise RuntimeError(f"invalid JSON object in {path}")
    return payload


def save_json_file(path: str, payload: dict[str, Any]) -> None:
    directory = os.path.dirname(path) or "."
    os.makedirs(directory, exist_ok=True)
    temp_path = f"{path}.{uuid.uuid4().hex}.tmp"
    raw = json.dumps(payload, ensure_ascii=False, indent=2) + "\n"
    with open(temp_path, "w", encoding="utf-8") as handle:
        handle.write(raw)
    os.replace(temp_path, path)


def antigravity_remaining_percent(value: Any) -> int | None:
    if value in {"", None}:
        return None
    try:
        fraction = float(value)
    except (TypeError, ValueError):
        return None
    percent = int(fraction * 100.0)
    return max(0, min(100, percent))


def antigravity_auth_needs_refresh(auth_payload: dict[str, Any], skew_seconds: int = 60) -> bool:
    expired_at = parse_iso_datetime(auth_payload.get("expired"))
    if expired_at is not None:
        now = datetime.now(expired_at.tzinfo or UTC)
        return expired_at <= now + timedelta(seconds=skew_seconds)

    issued_ms = int_from_text(auth_payload.get("timestamp"))
    expires_in = int_from_text(auth_payload.get("expires_in"))
    if issued_ms is None or expires_in is None:
        return False
    expiry = datetime.fromtimestamp((issued_ms / 1000.0) + expires_in, tz=UTC)
    return expiry <= datetime.now(UTC) + timedelta(seconds=skew_seconds)


def fetch_antigravity_project_id(access_token: str, timeout_seconds: int) -> str:
    payload = {
        "metadata": {
            "ideType": "ANTIGRAVITY",
            "platform": "PLATFORM_UNSPECIFIED",
            "pluginType": "GEMINI",
        }
    }
    headers = {
        "Authorization": f"Bearer {access_token}",
        "User-Agent": "google-api-nodejs-client/9.15.1",
        "X-Goog-Api-Client": "google-cloud-sdk vscode_cloudshelleditor/0.1",
        "Client-Metadata": '{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}',
    }
    status, _, raw, parsed = request_url(
        "POST",
        ANTIGRAVITY_LOAD_CODE_ASSIST_ENDPOINT,
        headers=headers,
        payload=payload,
        timeout=timeout_seconds,
    )
    if not 200 <= status < 300:
        detail = compact_message(response_text(raw, parsed), 120)
        raise RuntimeError(f"loadCodeAssist failed: HTTP {status} {detail}")
    if not isinstance(parsed, dict):
        raise RuntimeError("loadCodeAssist returned non-JSON response")

    project_id = str(parsed.get("cloudaicompanionProject", "")).strip()
    if not project_id:
        project_obj = parsed.get("cloudaicompanionProject")
        if isinstance(project_obj, dict):
            project_id = str(project_obj.get("id", "")).strip()
    if project_id:
        return project_id
    raise RuntimeError("loadCodeAssist did not return a project id")


def refresh_antigravity_auth(auth_id: str, auth_payload: dict[str, Any], timeout_seconds: int) -> dict[str, Any]:
    refresh_token = str(auth_payload.get("refresh_token", "")).strip()
    if not refresh_token:
        raise RuntimeError(f"{auth_id}: missing refresh_token")
    if not ANTIGRAVITY_CLIENT_SECRET:
        raise RuntimeError(f"{auth_id}: missing CLIPROXY_ANTIGRAVITY_CLIENT_SECRET")

    form = {
        "client_id": ANTIGRAVITY_CLIENT_ID,
        "client_secret": ANTIGRAVITY_CLIENT_SECRET,
        "grant_type": "refresh_token",
        "refresh_token": refresh_token,
    }
    status, _, raw, parsed = request_url(
        "POST",
        ANTIGRAVITY_TOKEN_ENDPOINT,
        headers={"User-Agent": "Go-http-client/2.0"},
        form=form,
        timeout=timeout_seconds,
    )
    if not 200 <= status < 300:
        detail = compact_message(response_text(raw, parsed), 120)
        raise RuntimeError(f"{auth_id}: token refresh failed: HTTP {status} {detail}")
    if not isinstance(parsed, dict):
        raise RuntimeError(f"{auth_id}: token refresh returned non-JSON response")

    access_token = str(parsed.get("access_token", "")).strip()
    if not access_token:
        raise RuntimeError(f"{auth_id}: token refresh returned empty access_token")

    now = datetime.now(UTC)
    auth_payload["access_token"] = access_token
    if str(parsed.get("refresh_token", "")).strip():
        auth_payload["refresh_token"] = str(parsed.get("refresh_token", "")).strip()
    expires_in = int_from_text(parsed.get("expires_in")) or int_from_text(auth_payload.get("expires_in")) or 3600
    auth_payload["expires_in"] = expires_in
    auth_payload["timestamp"] = int(now.timestamp() * 1000)
    auth_payload["expired"] = (now + timedelta(seconds=expires_in)).isoformat()
    auth_payload["type"] = "antigravity"

    if not str(auth_payload.get("project_id", "")).strip():
        auth_payload["project_id"] = fetch_antigravity_project_id(access_token, timeout_seconds)

    save_json_file(auth_file_path(auth_id), auth_payload)
    return auth_payload


def fetch_antigravity_quota_payload(
    auth_id: str,
    auth_payload: dict[str, Any],
    timeout_seconds: int,
) -> tuple[dict[str, Any], dict[str, Any]]:
    working = dict(auth_payload)
    refreshed = False
    project_reloaded = False

    while True:
        if antigravity_auth_needs_refresh(working):
            working = refresh_antigravity_auth(auth_id, working, timeout_seconds)
            refreshed = True

        access_token = str(working.get("access_token", "")).strip()
        if not access_token:
            raise RuntimeError(f"{auth_id}: missing access_token")

        project_id = str(working.get("project_id", "")).strip()
        payload = {"project": project_id} if project_id else {}
        last_error = "all endpoints failed"
        last_status = 0

        for endpoint in ANTIGRAVITY_FETCH_MODELS_ENDPOINTS:
            status, _, raw, parsed = request_url(
                "POST",
                endpoint,
                headers={
                    "Authorization": f"Bearer {access_token}",
                    "User-Agent": "antigravity/1.19.6 darwin/arm64",
                },
                payload=payload,
                timeout=timeout_seconds,
            )

            if 200 <= status < 300:
                if not isinstance(parsed, dict):
                    raise RuntimeError(f"{auth_id}: quota API returned non-JSON response")
                if not str(working.get("project_id", "")).strip():
                    working["project_id"] = project_id
                    save_json_file(auth_file_path(auth_id), working)
                return parsed, working

            detail = compact_message(response_text(raw, parsed), 160)
            last_status = status
            last_error = f"HTTP {status} {detail}".strip()

            if status == 401 and not refreshed:
                working = refresh_antigravity_auth(auth_id, working, timeout_seconds)
                refreshed = True
                break

            if status == 400 and not project_reloaded:
                working["project_id"] = fetch_antigravity_project_id(access_token, timeout_seconds)
                save_json_file(auth_file_path(auth_id), working)
                project_reloaded = True
                break

            if status in {429, 500, 502, 503, 504}:
                continue

            raise RuntimeError(f"{auth_id}: {last_error}")
        else:
            raise RuntimeError(f"{auth_id}: {last_error if last_status else 'quota API request failed'}")


def antigravity_public_model(name: str) -> bool:
    lowered = str(name or "").strip().lower()
    return lowered.startswith(ANTIGRAVITY_PUBLIC_MODEL_PREFIXES)


def parse_antigravity_quota_models(payload: dict[str, Any]) -> list[dict[str, Any]]:
    raw_models = payload.get("models")
    if not isinstance(raw_models, dict):
        return []

    models: list[dict[str, Any]] = []
    for name, info in raw_models.items():
        if not antigravity_public_model(name) or not isinstance(info, dict):
            continue
        quota_info = info.get("quotaInfo")
        if quota_info is None:
            quota_info = {}
        if not isinstance(quota_info, dict):
            quota_info = {}
        reset_time = str(quota_info.get("resetTime", "")).strip()
        reset_dt = parse_iso_datetime(reset_time)

        remaining = antigravity_remaining_percent(quota_info.get("remainingFraction"))
        models.append(
            {
                "name": str(name).strip(),
                "display_name": str(info.get("displayName", "")).strip() or str(name).strip(),
                "remaining_percent": remaining,
                "used_percent": None if remaining is None else max(0, 100 - remaining),
                "reset_time": reset_time,
                "reset_at": "-"
                if reset_dt is None
                else reset_dt.astimezone(UTC_PLUS_8).strftime("%Y-%m-%d %H:%M:%S"),
                "max_output_tokens": int_from_text(info.get("maxOutputTokens")),
            }
        )

    models.sort(key=lambda item: item["name"])
    return models


def pick_antigravity_summary_models(models: list[dict[str, Any]], limit: int = 4) -> list[dict[str, Any]]:
    selected: list[dict[str, Any]] = []
    by_name = {str(model.get("name", "")).strip(): model for model in models}

    for name in ANTIGRAVITY_SUMMARY_MODEL_PRIORITY:
        model = by_name.get(name)
        if model is not None:
            selected.append(model)
        if len(selected) >= limit:
            return selected

    for model in models:
        if model in selected:
            continue
        selected.append(model)
        if len(selected) >= limit:
            break
    return selected


def format_antigravity_model_summary(models: list[dict[str, Any]], limit: int = 4) -> str:
    selected = pick_antigravity_summary_models(models, limit=limit)
    if not selected:
        return "-"

    parts: list[str] = []
    for model in selected:
        name = str(model.get("name", "")).strip() or "-"
        remaining = model.get("remaining_percent")
        shown = "-" if remaining is None else f"{remaining}%"
        parts.append(f"{name}={shown}")
    return " ".join(parts)


def find_antigravity_model(models: list[dict[str, Any]], requested: str) -> dict[str, Any] | None:
    wanted = str(requested or "").strip().lower()
    if not wanted:
        return None
    for model in models:
        name = str(model.get("name", "")).strip().lower()
        if name == wanted:
            return model
    for model in models:
        name = str(model.get("name", "")).strip().lower()
        if wanted in name:
            return model
    return None


def build_antigravity_quota_row(entry: dict[str, Any], model_filter: str | None, timeout_seconds: int) -> dict[str, Any]:
    row: dict[str, Any] = {
        "id": str(entry.get("id", "")).strip() or "-",
        "state": str(entry.get("state", "")).strip() or "-",
        "prefix": str(entry.get("prefix", "")).strip() or "-",
        "email": str(entry.get("email", "")).strip() or display_account(entry) or "-",
        "status": str(entry.get("status", "")).strip() or "-",
        "status_message": str(entry.get("status_message", "")).strip(),
        "recent_error_type": extract_error_type(entry.get("status_message", "")),
        "disabled": bool(entry.get("disabled", False)),
        "runtime_model_count": int(entry.get("model_count", 0) or 0),
        "expires_at": str(entry.get("expires_at", "")).strip() or "-",
        "next_retry_after": str(entry.get("next_retry_after", "")).strip() or "-",
        "auth_file": "-",
        "project_id": "-",
        "tracked_model_count": 0,
        "min_remaining_percent": None,
        "max_remaining_percent": None,
        "next_reset_at": "-",
        "summary": "-",
        "matched_model": None,
        "fetch_error": "",
        "models": [],
    }

    auth_id = str(entry.get("id", "")).strip()
    if not auth_id:
        row["fetch_error"] = "missing auth id"
        return row

    path = auth_file_path(auth_id)
    row["auth_file"] = path
    if not os.path.exists(path):
        row["fetch_error"] = "auth file not found"
        return row

    auth_payload = load_json_file(path)
    try:
        live_payload, refreshed_auth = fetch_antigravity_quota_payload(auth_id, auth_payload, timeout_seconds)
        row["project_id"] = str(refreshed_auth.get("project_id", "")).strip() or "-"
        models = parse_antigravity_quota_models(live_payload)
        row["models"] = models
        row["tracked_model_count"] = len(models)
        known_remaining = [int(model["remaining_percent"]) for model in models if model.get("remaining_percent") is not None]
        if known_remaining:
            row["min_remaining_percent"] = min(known_remaining)
            row["max_remaining_percent"] = max(known_remaining)

        reset_candidates = [parse_iso_datetime(model.get("reset_time")) for model in models if str(model.get("reset_time", "")).strip()]
        reset_candidates = [dt for dt in reset_candidates if dt is not None]
        if reset_candidates:
            earliest = min(reset_candidates)
            row["next_reset_at"] = earliest.astimezone(UTC_PLUS_8).strftime("%Y-%m-%d %H:%M:%S")

        row["summary"] = format_antigravity_model_summary(models)
        if model_filter:
            row["matched_model"] = find_antigravity_model(models, model_filter)
    except Exception as exc:
        row["fetch_error"] = str(exc)

    return row


def resolve_client_api_key(preferred: str | None) -> str:
    preferred = str(preferred or "").strip()
    if preferred:
        return preferred
    keys = get_api_keys()
    if not keys:
        fail("no client API key configured")
    return keys[0]


def command_upstream_status(args: argparse.Namespace) -> int:
    provider = args.provider.strip().lower() if args.provider else "codex"
    auths = get_runtime_auths(provider)

    if args.json:
        print(json.dumps(auths, ensure_ascii=False, indent=2))
        return 0

    if not auths:
        print(f"No runtime upstream auths found for provider: {provider}")
        return 0

    print(f"{'STATE':<12} {'TYPE':<8} {'ACCOUNT':<32} {'PLAN':<8} {'GROUP':<12} {'RECOVER AT':<19} {'MODELS':>6} {'SOURCE':<8} {'PREFIX'}")
    for entry in auths:
        state = str(entry.get("state", "")).strip() or "-"
        account_type = str(entry.get("account_type", "")).strip() or "-"
        account = display_account(entry)
        plan_type = str(entry.get("plan_type", "")).strip() or "-"
        account_group = str(entry.get("account_group", "")).strip() or "-"
        recover_at = normalise_iso(str(entry.get("quota_next_recover_at", "")).strip())
        if recover_at == "-":
            recover_at = normalise_iso(str(entry.get("next_retry_after", "")).strip())
        model_count = int(entry.get("model_count", 0) or 0)
        source = str(entry.get("source", "")).strip() or "-"
        prefix = str(entry.get("prefix", "")).strip() or "-"
        print(f"{state:<12} {account_type:<8} {account:<32} {plan_type:<8} {account_group:<12} {recover_at:<19} {model_count:>6} {source:<8} {prefix}")
        if args.details:
            status = str(entry.get("status", "")).strip() or "-"
            unavailable = bool(entry.get("unavailable", False))
            quota_exceeded = bool(entry.get("quota_exceeded", False))
            status_message = str(entry.get("status_message", "")).strip()
            base_url = str(entry.get("base_url", "")).strip()
            print(f"  - status={status} unavailable={str(unavailable).lower()} quota_exceeded={str(quota_exceeded).lower()}")
            if base_url:
                print(f"  - base_url={base_url}")
            if status_message:
                print(f"  - message={status_message}")
    return 0


def command_antigravity_status(args: argparse.Namespace) -> int:
    auths = get_runtime_auths("antigravity")
    if args.prefix:
        wanted = args.prefix.strip().lower()
        auths = [entry for entry in auths if str(entry.get("prefix", "")).strip().lower() == wanted]

    if args.json:
        print(json.dumps(auths, ensure_ascii=False, indent=2))
        return 0

    if not auths:
        print("No Antigravity auths found.")
        return 0

    print(f"{'STATE':<12} {'PREFIX':<8} {'EMAIL':<34} {'MODELS':>6} {'ERROR':<24} {'MESSAGE'}")
    for entry in auths:
        state = str(entry.get("state", "")).strip() or "-"
        prefix = str(entry.get("prefix", "")).strip() or "-"
        email = str(entry.get("email", "")).strip() or display_account(entry) or "-"
        model_count = int(entry.get("model_count", 0) or 0)
        status_message = str(entry.get("status_message", "")).strip()
        error_type = extract_error_type(status_message)
        message = compact_message(status_message)
        print(f"{state:<12} {prefix:<8} {email:<34} {model_count:>6} {error_type:<24} {message}")
        if args.details:
            auth_id = str(entry.get("id", "")).strip() or "-"
            expires_at = normalise_iso(str(entry.get("expires_at", "")).strip())
            next_retry_after = normalise_iso(str(entry.get("next_retry_after", "")).strip())
            print(f"  - id={auth_id} expires_at={expires_at} next_retry_after={next_retry_after}")
    return 0


def command_antigravity_toggle(args: argparse.Namespace, disabled: bool) -> int:
    entry = resolve_antigravity_auth(args.identifier)
    auth_id = str(entry.get("id", "")).strip()
    if not auth_id:
        fail("matched auth is missing id")
    patch_auth_status(auth_id, disabled)
    action = "disabled" if disabled else "enabled"
    prefix = str(entry.get("prefix", "")).strip() or "-"
    account = str(entry.get("email", "")).strip() or display_account(entry) or auth_id
    print(f"{action}: {account} (prefix={prefix}, id={auth_id})")
    return 0


def command_antigravity_enable(args: argparse.Namespace) -> int:
    return command_antigravity_toggle(args, disabled=False)


def command_antigravity_disable(args: argparse.Namespace) -> int:
    return command_antigravity_toggle(args, disabled=True)


def command_antigravity_test(args: argparse.Namespace) -> int:
    prefix = args.prefix.strip()
    if not prefix:
        fail("prefix is required")

    entry = resolve_antigravity_auth(prefix)
    model = args.model.strip()
    if "/" not in model:
        model = f"{prefix}/{model}"

    client_api_key = resolve_client_api_key(args.api_key)
    payload = {
        "model": model,
        "messages": [{"role": "user", "content": args.prompt}],
        "stream": False,
    }
    status, response = local_api_json("POST", "/v1/chat/completions", client_api_key, payload)

    if args.json:
        print(json.dumps({"http_status": status, "response": response}, ensure_ascii=False, indent=2))
        return 0 if 200 <= status < 300 else 1

    print(f"prefix={prefix}")
    print(f"auth_id={str(entry.get('id', '')).strip() or '-'}")
    print(f"email={str(entry.get('email', '')).strip() or display_account(entry) or '-'}")
    print(f"model={model}")
    print(f"http_status={status}")
    if isinstance(response, dict):
        print(json.dumps(response, ensure_ascii=False, indent=2))
    else:
        print(str(response))
    return 0 if 200 <= status < 300 else 1


def command_antigravity_quota(args: argparse.Namespace) -> int:
    auths = get_runtime_auths("antigravity")
    if args.prefix:
        wanted = args.prefix.strip().lower()
        auths = [entry for entry in auths if str(entry.get("prefix", "")).strip().lower() == wanted]

    rows = [build_antigravity_quota_row(entry, args.model, args.timeout) for entry in auths]

    if args.json:
        print(json.dumps(rows, ensure_ascii=False, indent=2))
        return 0

    if not rows:
        print("No Antigravity auths found.")
        return 0

    if args.model:
        model_name = args.model.strip()
        print(f"Quota note: Antigravity quota is model-specific. Showing live remaining quota for model: {model_name}")
        print()
        print(f"{'STATE':<12} {'PREFIX':<8} {'EMAIL':<34} {'LEFT':>6} {'USED':>6} {'RESET':<19} {'STATUS'}")
        for row in rows:
            matched = row.get("matched_model")
            left = "-"
            used = "-"
            reset_at = "-"
            status = "-"
            if isinstance(matched, dict):
                remaining = matched.get("remaining_percent")
                used_percent = matched.get("used_percent")
                left = "-" if remaining is None else str(remaining)
                used = "-" if used_percent is None else str(used_percent)
                reset_at = str(matched.get("reset_at", "")).strip() or "-"
                status = "ok"
            elif row.get("fetch_error"):
                status = compact_message(row.get("fetch_error", ""), 90)
            else:
                status = "model_not_found"

            print(
                f"{str(row.get('state', '-')):<12} {str(row.get('prefix', '-')):<8} "
                f"{str(row.get('email', '-')):<34} {left:>6} {used:>6} {reset_at:<19} {status}"
            )
            if args.details:
                print(
                    f"  - auth_id={str(row.get('id', '-'))} project_id={str(row.get('project_id', '-'))} "
                    f"tracked_models={int(row.get('tracked_model_count', 0) or 0)}"
                )
        return 0

    print("Quota note: Antigravity quota is model-specific. Default summary shows representative public models.")
    print("Use `antigravity-quota --model gemini-3-flash` to compare one model across all accounts.")
    print()
    print(f"{'STATE':<12} {'PREFIX':<8} {'EMAIL':<34} {'TRACK':>5} {'LOW':>5} {'HIGH':>5} {'NEXT RESET':<19} {'SUMMARY'}")
    for row in rows:
        low = row.get("min_remaining_percent")
        high = row.get("max_remaining_percent")
        summary = compact_message(row.get("summary", "-"), 90)
        if row.get("fetch_error"):
            summary = f"error={compact_message(row.get('fetch_error', ''), 82)}"
        print(
            f"{str(row.get('state', '-')):<12} {str(row.get('prefix', '-')):<8} "
            f"{str(row.get('email', '-')):<34} {int(row.get('tracked_model_count', 0) or 0):>5} "
            f"{'-' if low is None else str(low):>5} {'-' if high is None else str(high):>5} "
            f"{str(row.get('next_reset_at', '-')):<19} {summary}"
        )
        if args.details:
            print(
                f"  - auth_id={str(row.get('id', '-'))} project_id={str(row.get('project_id', '-'))} "
                f"runtime_models={int(row.get('runtime_model_count', 0) or 0)} recent_error={str(row.get('recent_error_type', '-'))}"
            )
            if row.get("status_message"):
                print(f"  - status_message={compact_message(row.get('status_message', ''), 160)}")
            for model in row.get("models", []):
                remaining = model.get("remaining_percent")
                used = model.get("used_percent")
                reset_at = str(model.get("reset_at", "")).strip() or "-"
                print(
                    f"  - {str(model.get('name', '-'))}: left={'-' if remaining is None else str(remaining)} "
                    f"used={'-' if used is None else str(used)} reset={reset_at}"
                )
    return 0


def command_oauth_quota(args: argparse.Namespace) -> int:
    rows = fetch_management_oauth_quota_rows(args.probe, args.probe_model, args.probe_timeout)
    if rows is None:
        rows = merge_oauth_auth_file_rows(collect_oauth_quota_rows(args.log_dir))
        if args.probe:
            rows = apply_oauth_probe(rows, args.probe_model, args.probe_timeout)
    rows = hide_stale_subscription_until(rows)

    if args.json:
        print(json.dumps(rows, ensure_ascii=False, indent=2))
        return 0

    if args.probe:
        print("Probe note: --probe sends one small live request to each OAuth account and consumes a small amount of quota.")
        print("Displayed quota comes from the live probe when it succeeds; otherwise it falls back to the latest seen log headers.")
    else:
        print("Quota note: this is the latest X-Codex-* quota header seen for each OAuth account.")
        print("If an account has not handled a recent request yet, its remaining quota stays unknown.")

    if not rows:
        print()
        print("No OAuth upstream accounts found.")
        return 0

    print()
    print(
        f"{'ACCOUNT':<32} {'STATE':<12} {'PLAN':<8} {'PLUS-UNTIL':<19} "
        f"{'P-LEFT':>6} {'P-USED':>6} {'P-RESET':<19} "
        f"{'S-LEFT':>6} {'S-USED':>6} {'S-RESET':<19} "
        f"{'LAST SEEN':<19}"
    )
    for row in rows:
        account = str(row.get("account", "")).strip() or "-"
        state = str(row.get("state", "")).strip() or "-"
        plan_type = str(row.get("plan_type", "")).strip() or "-"
        subscription_active_until = str(row.get("subscription_active_until", "")).strip() or "-"
        p_left = str(row.get("primary_remaining_percent", "")).strip() or "-"
        p_used = str(row.get("primary_used_percent", "")).strip() or "-"
        p_reset = str(row.get("primary_reset_at", "")).strip() or "-"
        s_left = str(row.get("secondary_remaining_percent", "")).strip() or "-"
        s_used = str(row.get("secondary_used_percent", "")).strip() or "-"
        s_reset = str(row.get("secondary_reset_at", "")).strip() or "-"
        last_seen = str(row.get("last_seen", "")).strip() or "-"
        print(
            f"{account:<32} {state:<12} {plan_type:<8} {subscription_active_until:<19} "
            f"{p_left:>6} {p_used:>6} {p_reset:<19} "
            f"{s_left:>6} {s_used:>6} {s_reset:<19} "
            f"{last_seen:<19}"
        )
        if args.details:
            source_log = str(row.get("source_log", "")).strip() or "-"
            auth_id = str(row.get("auth_id", "")).strip() or "-"
            p_after = str(row.get("primary_reset_after_seconds", "")).strip() or "-"
            s_after = str(row.get("secondary_reset_after_seconds", "")).strip() or "-"
            print(f"  - source_log={source_log} auth_id={auth_id}")
            print(f"  - primary_reset_after_seconds={p_after} secondary_reset_after_seconds={s_after}")
            if args.probe:
                probe_model = str(row.get("probe_model", "")).strip() or "-"
                probe_status_code = str(row.get("probe_status_code", "")).strip() or "-"
                probe_error = str(row.get("probe_error", "")).strip() or "-"
                print(f"  - probe_model={probe_model} probe_status_code={probe_status_code} probe_error={probe_error}")
    return 0


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Manage CLIProxyAPI client keys and codex upstreams locally.")
    sub = parser.add_subparsers(dest="command", required=True)

    add_parser = sub.add_parser("add", help="add a teammate key")
    add_parser.add_argument("username", help="username label used in the generated key prefix")
    add_parser.add_argument("--key", help="use a specific key instead of generating one")
    add_parser.add_argument("--replace", action="store_true", help="replace existing keys for the same username")
    add_parser.add_argument("--show-full", action="store_true", help="print the full key instead of masking it")
    add_parser.set_defaults(func=command_add)

    revoke_parser = sub.add_parser("revoke", help="revoke by full key or username")
    revoke_parser.add_argument("identifier", help="full key or username")
    revoke_parser.set_defaults(func=command_revoke)

    list_parser = sub.add_parser("list", help="list configured client keys")
    list_parser.add_argument("--full", action="store_true", help="show full keys")
    list_parser.set_defaults(func=command_list)

    usage_parser = sub.add_parser("usage", help="show usage grouped by client API key")
    usage_parser.add_argument("identifier", nargs="?", help="optional full key or username filter")
    usage_parser.add_argument("--json", action="store_true", help="print raw JSON")
    usage_parser.add_argument("--details", action="store_true", help="show per-model totals")
    usage_parser.add_argument("--full", action="store_true", help="show full keys")
    usage_parser.set_defaults(func=command_usage)

    routing_parser = sub.add_parser("routing", help="show or set upstream routing strategy")
    routing_parser.add_argument("strategy", nargs="?", help="round-robin or fill-first")
    routing_parser.set_defaults(func=command_routing)

    list_codex_parser = sub.add_parser("list-codex", help="list configured codex upstream accounts")
    list_codex_parser.add_argument("--full", action="store_true", help="show full upstream API keys")
    list_codex_parser.set_defaults(func=command_list_codex)

    add_codex_parser = sub.add_parser("add-codex", help="add a codex upstream API-key account")
    add_codex_parser.add_argument("api_key", help="upstream codex/OpenAI-compatible API key")
    add_codex_parser.add_argument("--prefix", help="optional upstream prefix for manual pinning, e.g. oa1")
    add_codex_parser.add_argument("--base-url", help="upstream base URL; defaults to the first existing codex upstream base URL")
    add_codex_parser.add_argument("--proxy-url", help="optional upstream proxy URL")
    add_codex_parser.add_argument(
        "--copy-models-from",
        type=int,
        help="copy model mappings from an existing codex upstream index (defaults to 0)",
    )
    add_codex_parser.add_argument("--replace", action="store_true", help="replace an existing entry matched by the same API key")
    add_codex_parser.add_argument("--websockets", action="store_true", help="enable codex websocket mode for this upstream")
    add_codex_parser.add_argument("--show-full", action="store_true", help="print the full upstream key instead of masking it")
    add_codex_parser.set_defaults(func=command_add_codex)

    remove_codex_parser = sub.add_parser("remove-codex", help="remove a codex upstream by index, prefix, or API key")
    remove_codex_parser.add_argument("identifier", help="index, prefix, full API key, or API key prefix")
    remove_codex_parser.set_defaults(func=command_remove_codex)

    upstream_status_parser = sub.add_parser("upstream-status", help="show runtime upstream auth state")
    upstream_status_parser.add_argument("provider", nargs="?", help="provider filter, defaults to codex")
    upstream_status_parser.add_argument("--json", action="store_true", help="print raw JSON")
    upstream_status_parser.add_argument("--details", action="store_true", help="show extra status fields")
    upstream_status_parser.set_defaults(func=command_upstream_status)

    oauth_quota_parser = sub.add_parser("oauth-quota", help="show the latest seen upstream OAuth quota headers")
    oauth_quota_parser.add_argument("--json", action="store_true", help="print raw JSON")
    oauth_quota_parser.add_argument("--details", action="store_true", help="show source log and reset-after values")
    oauth_quota_parser.add_argument(
        "--probe",
        action="store_true",
        help="actively query each OAuth account with a small live request",
    )
    oauth_quota_parser.add_argument(
        "--probe-model",
        default=DEFAULT_OAUTH_PROBE_MODEL,
        help=f"model used for live probes (default: {DEFAULT_OAUTH_PROBE_MODEL})",
    )
    oauth_quota_parser.add_argument(
        "--probe-timeout",
        type=int,
        default=DEFAULT_OAUTH_PROBE_TIMEOUT_SECONDS,
        help=f"timeout in seconds for each live probe (default: {DEFAULT_OAUTH_PROBE_TIMEOUT_SECONDS})",
    )
    oauth_quota_parser.add_argument(
        "--log-dir",
        default=DEFAULT_LOG_DIR,
        help=f"request log directory (default: {DEFAULT_LOG_DIR})",
    )
    oauth_quota_parser.set_defaults(func=command_oauth_quota)

    anti_status_parser = sub.add_parser("antigravity-status", help="list Antigravity auths with prefix, state, error type, and model count")
    anti_status_parser.add_argument("--prefix", help="optional prefix filter, e.g. ag2")
    anti_status_parser.add_argument("--json", action="store_true", help="print raw JSON")
    anti_status_parser.add_argument("--details", action="store_true", help="show extra fields")
    anti_status_parser.set_defaults(func=command_antigravity_status)

    anti_enable_parser = sub.add_parser("antigravity-enable", help="enable one Antigravity auth by prefix, email, or id")
    anti_enable_parser.add_argument("identifier", help="prefix, email, account, or auth id")
    anti_enable_parser.set_defaults(func=command_antigravity_enable)

    anti_disable_parser = sub.add_parser("antigravity-disable", help="disable one Antigravity auth by prefix, email, or id")
    anti_disable_parser.add_argument("identifier", help="prefix, email, account, or auth id")
    anti_disable_parser.set_defaults(func=command_antigravity_disable)

    anti_test_parser = sub.add_parser("antigravity-test", help="send a targeted test request through one Antigravity auth prefix")
    anti_test_parser.add_argument("prefix", help="Antigravity auth prefix, e.g. ag2")
    anti_test_parser.add_argument("--model", default="gemini-3-flash", help="model name without prefix by default")
    anti_test_parser.add_argument("--prompt", default="Reply with OK only.", help="test prompt")
    anti_test_parser.add_argument("--api-key", help="client API key used to call the local proxy")
    anti_test_parser.add_argument("--json", action="store_true", help="print raw JSON with HTTP status")
    anti_test_parser.set_defaults(func=command_antigravity_test)

    anti_quota_parser = sub.add_parser("antigravity-quota", help="show live Antigravity remaining quota per account")
    anti_quota_parser.add_argument("--prefix", help="optional prefix filter, e.g. ag2")
    anti_quota_parser.add_argument("--model", help="compare one specific model across all accounts")
    anti_quota_parser.add_argument("--timeout", type=int, default=30, help="timeout in seconds for each upstream quota fetch")
    anti_quota_parser.add_argument("--json", action="store_true", help="print raw JSON")
    anti_quota_parser.add_argument("--details", action="store_true", help="show full per-model remaining quota")
    anti_quota_parser.set_defaults(func=command_antigravity_quota)

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
