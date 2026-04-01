#!/usr/bin/env python3
import argparse
import json
import os
import re
import secrets
import subprocess
import sys
import uuid
from datetime import UTC, datetime, timedelta, timezone
from typing import Any


DEFAULT_CONTAINER = os.environ.get("CLIPROXY_CONTAINER", "cli-proxy-api")
DEFAULT_MGMT_KEY = os.environ.get(
    "CLIPROXY_MANAGEMENT_KEY",
    "mgmt-change-me",
)
DEFAULT_PUBLIC_API = os.environ.get("CLIPROXY_PUBLIC_API", "https://tradetd.cloud-ip.cc/v1")
LOCAL_MGMT_HOST = os.environ.get("CLIPROXY_LOCAL_MGMT_HOST", "127.0.0.1")
LOCAL_MGMT_PORT = int(os.environ.get("CLIPROXY_LOCAL_MGMT_PORT", "8317"))
DEFAULT_MODEL_EXAMPLE = os.environ.get("CLIPROXY_MODEL_EXAMPLE", "gpt-5.4-mini")
DEFAULT_LOG_DIR = os.environ.get(
    "CLIPROXY_LOG_DIR",
    os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))), "logs"),
)
DEFAULT_OAUTH_PROBE_TIMEOUT_SECONDS = int(os.environ.get("CLIPROXY_OAUTH_PROBE_TIMEOUT_SECONDS", "30"))
DEFAULT_OAUTH_PROBE_MODEL = os.environ.get("CLIPROXY_OAUTH_PROBE_MODEL", DEFAULT_MODEL_EXAMPLE)
DEFAULT_OAUTH_PROBE_ENDPOINT = os.environ.get(
    "CLIPROXY_OAUTH_PROBE_ENDPOINT",
    "https://chatgpt.com/backend-api/codex/responses/compact",
)
DEFAULT_OAUTH_PROBE_TEXT = os.environ.get("CLIPROXY_OAUTH_PROBE_TEXT", "Reply with OK only.")

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


def command_upstream_status(args: argparse.Namespace) -> int:
    provider = args.provider.strip().lower() if args.provider else "codex"
    auths = get_runtime_auths(provider)

    if args.json:
        print(json.dumps(auths, ensure_ascii=False, indent=2))
        return 0

    if not auths:
        print(f"No runtime upstream auths found for provider: {provider}")
        return 0

    print(f"{'STATE':<12} {'TYPE':<8} {'ACCOUNT':<32} {'PLAN':<8} {'RECOVER AT':<19} {'MODELS':>6} {'SOURCE':<8} {'PREFIX'}")
    for entry in auths:
        state = str(entry.get("state", "")).strip() or "-"
        account_type = str(entry.get("account_type", "")).strip() or "-"
        account = display_account(entry)
        plan_type = str(entry.get("plan_type", "")).strip() or "-"
        recover_at = normalise_iso(str(entry.get("quota_next_recover_at", "")).strip())
        if recover_at == "-":
            recover_at = normalise_iso(str(entry.get("next_retry_after", "")).strip())
        model_count = int(entry.get("model_count", 0) or 0)
        source = str(entry.get("source", "")).strip() or "-"
        prefix = str(entry.get("prefix", "")).strip() or "-"
        print(f"{state:<12} {account_type:<8} {account:<32} {plan_type:<8} {recover_at:<19} {model_count:>6} {source:<8} {prefix}")
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


def command_oauth_quota(args: argparse.Namespace) -> int:
    rows = merge_oauth_auth_file_rows(collect_oauth_quota_rows(args.log_dir))
    if args.probe:
        rows = apply_oauth_probe(rows, args.probe_model, args.probe_timeout)

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
        f"{'ACCOUNT':<32} {'STATE':<12} {'PLAN':<8} "
        f"{'P-LEFT':>6} {'P-USED':>6} {'P-RESET':<19} "
        f"{'S-LEFT':>6} {'S-USED':>6} {'S-RESET':<19} "
        f"{'LAST SEEN':<19}"
    )
    for row in rows:
        account = str(row.get("account", "")).strip() or "-"
        state = str(row.get("state", "")).strip() or "-"
        plan_type = str(row.get("plan_type", "")).strip() or "-"
        p_left = str(row.get("primary_remaining_percent", "")).strip() or "-"
        p_used = str(row.get("primary_used_percent", "")).strip() or "-"
        p_reset = str(row.get("primary_reset_at", "")).strip() or "-"
        s_left = str(row.get("secondary_remaining_percent", "")).strip() or "-"
        s_used = str(row.get("secondary_used_percent", "")).strip() or "-"
        s_reset = str(row.get("secondary_reset_at", "")).strip() or "-"
        last_seen = str(row.get("last_seen", "")).strip() or "-"
        print(
            f"{account:<32} {state:<12} {plan_type:<8} "
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

    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    return int(args.func(args))


if __name__ == "__main__":
    raise SystemExit(main())
