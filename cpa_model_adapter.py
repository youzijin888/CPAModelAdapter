#!/usr/bin/env python3
"""Adapt a preconfigured CPA provider's models into a Codex model catalog."""

from __future__ import annotations

import argparse
import copy
import datetime as dt
import fnmatch
import json
import os
import pathlib
import re
import sys
import urllib.error
import urllib.request
import urllib.parse
from typing import Any

try:
    import tomllib
except ModuleNotFoundError:
    from _vendor import tomli as tomllib


DEFAULT_OUTPUT_DIR = str(pathlib.Path(__file__).resolve().parent / "generated")
DEFAULT_CODEX_CONFIG = str(
    pathlib.Path(os.environ.get("CODEX_HOME") or "~/.codex").expanduser() / "config.toml"
)
DEFAULT_TEMPLATE_URL = (
    "https://raw.githubusercontent.com/router-for-me/EasyCLIProxyAPI/main/"
    "src-tauri/resources/codex_models/model-catalog.json"
)
ALLOWED_REASONING_LEVELS = (
    "none",
    "minimal",
    "low",
    "medium",
    "high",
    "xhigh",
    "max",
    "ultra",
)
BUNDLED_REASONING_LEVELS = {
    "deepseek-flash": ALLOWED_REASONING_LEVELS,
    "gpt-6-astra": ("low", "medium", "high", "xhigh", "max"),
}
REASONING_DESCRIPTIONS = {
    "none": "No reasoning",
    "minimal": "Minimal reasoning",
    "low": "Fast responses with lighter reasoning",
    "medium": "Balances speed and reasoning depth for everyday tasks",
    "high": "Greater reasoning depth for complex problems",
    "xhigh": "Extra high reasoning depth for complex problems",
    "max": "Maximum available reasoning depth for complex problems",
    "ultra": "Highest available reasoning depth",
}
DEFAULT_EXCLUDES = (
    "gpt-image-*",
    "*-image-*",
    "*embedding*",
    "*audio*",
    "*realtime*",
    "codex-auto-review",
)
MODEL_REQUIRED_FIELDS = (
    "slug",
    "display_name",
    "base_instructions",
    "context_window",
    "max_context_window",
    "supported_reasoning_levels",
    "shell_type",
    "visibility",
    "supported_in_api",
    "default_reasoning_summary",
    "support_verbosity",
    "truncation_policy",
    "supports_parallel_tool_calls",
    "experimental_supported_tools",
    "priority",
)


class SyncError(RuntimeError):
    pass


def request_json(url: str, bearer: str, timeout: int = 20) -> Any:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/json",
            "Authorization": f"Bearer {bearer}",
            "User-Agent": "cpa-model-adapter/1.0",
        },
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        raise SyncError(f"HTTP {exc.code} from {url}") from exc
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise SyncError(f"Unable to read JSON from {url}: {exc}") from exc


def read_codex_provider(
    config_path: pathlib.Path, requested_provider: str | None = None
) -> tuple[str, str, str]:
    """Read an existing Codex provider and resolve its API key from the environment."""
    try:
        data = tomllib.loads(config_path.read_text(encoding="utf-8"))
    except OSError as exc:
        raise SyncError(f"Unable to read Codex config {config_path}: {exc}") from exc
    except tomllib.TOMLDecodeError as exc:
        raise SyncError(f"Invalid Codex config {config_path}: {exc}") from exc

    provider_id = requested_provider or data.get("model_provider")
    if not isinstance(provider_id, str) or not provider_id.strip():
        raise SyncError("Codex config has no selected model_provider; use --provider")
    providers = data.get("model_providers")
    provider = providers.get(provider_id) if isinstance(providers, dict) else None
    if not isinstance(provider, dict):
        raise SyncError(f"Codex config has no model_providers.{provider_id} table")

    base_url = provider.get("base_url")
    env_key = provider.get("env_key")
    if not isinstance(base_url, str) or not base_url.strip():
        raise SyncError(f"Provider {provider_id} has no base_url")
    if not isinstance(env_key, str) or not env_key.strip():
        raise SyncError(f"Provider {provider_id} has no env_key")
    api_key = os.environ.get(env_key)
    if not api_key:
        raise SyncError(
            f"Environment variable {env_key} required by provider {provider_id} is not set"
        )
    return provider_id, base_url.strip(), api_key


def provider_models_url(base_url: str) -> str:
    parsed = urllib.parse.urlsplit(base_url.rstrip("/"))
    path = parsed.path.rstrip("/")
    if path.endswith("/v1"):
        path += "/models"
    else:
        path += "/v1/models"
    return urllib.parse.urlunsplit((parsed.scheme, parsed.netloc, path, "", ""))


def extract_model_ids(payload: Any) -> list[str]:
    if isinstance(payload, dict):
        values = payload.get("data") or payload.get("models")
    else:
        values = payload
    if not isinstance(values, list):
        raise SyncError("CPA model response does not contain a data/models array")
    result: list[str] = []
    seen: set[str] = set()
    for item in values:
        if isinstance(item, str):
            model_id = item.strip()
        elif isinstance(item, dict):
            model_id = next(
                (
                    str(item[key]).strip()
                    for key in ("slug", "id", "name", "model", "value")
                    if item.get(key)
                ),
                "",
            )
        else:
            model_id = ""
        normalized = model_id.casefold()
        if model_id and normalized not in seen:
            seen.add(normalized)
            result.append(model_id)
    return result


def extract_definitions(payload: Any) -> dict[str, dict[str, Any]]:
    values = (payload.get("data") or payload.get("models")) if isinstance(payload, dict) else payload
    if not isinstance(values, list):
        return {}
    result: dict[str, dict[str, Any]] = {}
    for item in values:
        if not isinstance(item, dict):
            continue
        model_id = str(item.get("id") or item.get("slug") or "").strip()
        if model_id:
            result.setdefault(model_id.casefold(), item)
    return result


def is_excluded(model_id: str, definition: dict[str, Any], patterns: tuple[str, ...]) -> bool:
    lowered = model_id.casefold()
    if any(fnmatch.fnmatch(lowered, pattern.casefold()) for pattern in patterns):
        return True
    outputs = definition.get("supportedOutputModalities") or definition.get(
        "supported_output_modalities"
    )
    if isinstance(outputs, list) and outputs and "text" not in {str(v).casefold() for v in outputs}:
        return True
    parameters = definition.get("supported_parameters")
    if isinstance(parameters, list) and parameters and "tools" not in {
        str(v).casefold() for v in parameters
    }:
        return True
    return False


def fetch_template_catalog(args: argparse.Namespace, output_dir: pathlib.Path) -> dict[str, Any]:
    cache_path = output_dir / ".template-catalog-cache.json"
    if args.template_file:
        try:
            payload = json.loads(pathlib.Path(args.template_file).read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise SyncError(f"Unable to read template catalog: {exc}") from exc
        validate_template_catalog(payload)
        return payload

    request = urllib.request.Request(
        args.template_url,
        headers={"Accept": "application/json", "User-Agent": "cpa-model-adapter/1.0"},
    )
    try:
        with urllib.request.urlopen(request, timeout=25) as response:
            payload = json.load(response)
        validate_template_catalog(payload)
        atomic_write(cache_path, json.dumps(payload, ensure_ascii=False, indent=2) + "\n", 0o600)
        return payload
    except (OSError, urllib.error.URLError, urllib.error.HTTPError, json.JSONDecodeError, SyncError) as exc:
        if cache_path.is_file():
            try:
                payload = json.loads(cache_path.read_text(encoding="utf-8"))
                validate_template_catalog(payload)
                return payload
            except (OSError, json.JSONDecodeError, SyncError):
                pass
        raise SyncError(f"Unable to obtain a valid template catalog: {exc}") from exc


def validate_template_catalog(payload: Any) -> None:
    if not isinstance(payload, dict):
        raise SyncError("Template catalog root must be an object")
    fallback = payload.get("fallback_model")
    models = payload.get("models")
    if not isinstance(fallback, dict) or not isinstance(models, list) or not models:
        raise SyncError("Template catalog requires fallback_model and a non-empty models array")
    validate_model_entry(fallback, require_slug=False)
    for model in models:
        if not isinstance(model, dict):
            raise SyncError("Every template model must be an object")
        validate_model_entry(model, require_slug=True)


def validate_model_entry(model: dict[str, Any], require_slug: bool = True) -> None:
    required = MODEL_REQUIRED_FIELDS if require_slug else MODEL_REQUIRED_FIELDS[1:]
    missing = [field for field in required if field not in model]
    if missing:
        raise SyncError(f"Model template is missing fields: {', '.join(missing)}")
    if not isinstance(model.get("base_instructions"), str) or not model["base_instructions"].strip():
        raise SyncError("Model template base_instructions must be non-empty")
    for key in ("context_window", "max_context_window"):
        if not isinstance(model.get(key), int) or model[key] <= 0:
            raise SyncError(f"Model template {key} must be a positive integer")
    if model["max_context_window"] < model["context_window"]:
        raise SyncError("max_context_window cannot be smaller than context_window")
    if not isinstance(model.get("supported_reasoning_levels"), list):
        raise SyncError("supported_reasoning_levels must be an array")
    if not isinstance(model.get("experimental_supported_tools"), list):
        raise SyncError("experimental_supported_tools must be an array")


def reasoning_levels(
    definition: dict[str, Any], template: dict[str, Any], model_id: str,
    *, exact_template: bool,
) -> list[str]:
    thinking = definition.get("thinking")
    raw = thinking.get("levels") if isinstance(thinking, dict) else None
    levels = [
        str(level).casefold()
        for level in raw or []
        if str(level).casefold() in ALLOWED_REASONING_LEVELS
    ]
    if levels:
        return list(dict.fromkeys(levels))
    if exact_template:
        levels = [
            str(item.get("effort", "")).casefold()
            for item in template.get("supported_reasoning_levels", [])
            if isinstance(item, dict)
            and str(item.get("effort", "")).casefold() in ALLOWED_REASONING_LEVELS
        ]
        if levels:
            return list(dict.fromkeys(levels))
    return list(BUNDLED_REASONING_LEVELS.get(model_id.casefold(), ALLOWED_REASONING_LEVELS))


def default_reasoning(model_id: str, levels: list[str]) -> str:
    preferred_by_model = {
        "deepseek-flash": "high",
        "gpt-5.6-sol": "low",
    }
    preferred = preferred_by_model.get(model_id.casefold(), "medium")
    if preferred in levels:
        return preferred
    return levels[0]


def build_model_entry(
    model_id: str,
    definition: dict[str, Any],
    templates: dict[str, dict[str, Any]],
    fallback: dict[str, Any],
    priority: int,
) -> dict[str, Any]:
    known = model_id.casefold() in templates
    model = copy.deepcopy(templates.get(model_id.casefold(), fallback))
    model["slug"] = model_id
    model["display_name"] = str(definition.get("display_name") or model_id)
    if definition.get("description"):
        model["description"] = str(definition["description"])
    model["visibility"] = "list"
    model["priority"] = priority

    context = definition.get("context_length") or definition.get("context_window")
    if isinstance(context, int) and context > 0:
        model["context_window"] = context
        model["max_context_window"] = max(context, int(model.get("max_context_window") or context))

    modalities = definition.get("supportedInputModalities") or definition.get("input_modalities")
    if isinstance(modalities, list):
        normalized = list(
            dict.fromkeys(
                str(value).casefold()
                for value in modalities
                if str(value).casefold() in {"text", "image"}
            )
        )
        if normalized:
            model["input_modalities"] = normalized
            model["supports_image_detail_original"] = "image" in normalized

    levels = reasoning_levels(definition, model, model_id, exact_template=known)
    model["supported_reasoning_levels"] = [
        {"effort": level, "description": REASONING_DESCRIPTIONS[level]} for level in levels
    ]
    model["default_reasoning_level"] = default_reasoning(model_id, levels)

    if not known:
        model["prefer_websockets"] = False
        model["supports_search_tool"] = False
        model["web_search_tool_type"] = "text"
        model["default_service_tier"] = None
        model["service_tiers"] = []
        model["additional_speed_tiers"] = []
        model.pop("minimal_client_version", None)
        model["upgrade"] = None
        model["availability_nux"] = None

    validate_model_entry(model)
    return model


def prepare_catalog(
    model_ids: list[str], definitions: dict[str, dict[str, Any]], template_catalog: dict[str, Any],
    excludes: tuple[str, ...]
) -> tuple[dict[str, Any], list[str]]:
    fallback = template_catalog["fallback_model"]
    templates = {
        str(model["slug"]).casefold(): model
        for model in template_catalog["models"]
        if isinstance(model, dict) and model.get("slug")
    }
    entries: list[dict[str, Any]] = []
    skipped: list[str] = []
    for model_id in model_ids:
        definition = definitions.get(model_id.casefold(), {})
        if is_excluded(model_id, definition, excludes):
            skipped.append(model_id)
            continue
        entries.append(
            build_model_entry(model_id, definition, templates, fallback, len(entries) + 1)
        )
    if not entries:
        raise SyncError("No Codex-compatible text/tool models remained after filtering")
    catalog = {"models": entries}
    validate_generated_catalog(catalog)
    return catalog, skipped


def validate_generated_catalog(catalog: Any) -> None:
    if not isinstance(catalog, dict) or not isinstance(catalog.get("models"), list):
        raise SyncError("Generated catalog must contain a models array")
    seen: set[str] = set()
    for model in catalog["models"]:
        if not isinstance(model, dict):
            raise SyncError("Generated model entry must be an object")
        validate_model_entry(model)
        slug = str(model["slug"]).casefold()
        if slug in seen:
            raise SyncError(f"Duplicate generated model slug: {model['slug']}")
        seen.add(slug)


def toml_string(value: str) -> str:
    return json.dumps(value, ensure_ascii=False)


def render_config(catalog_path: pathlib.Path) -> str:
    """Render only the model-catalog setting; provider/auth remain user-owned."""
    return f"model_catalog_json = {toml_string(str(catalog_path.resolve()))}\n"


def split_line_ending(line: str) -> tuple[str, str]:
    if line.endswith("\r\n"):
        return line[:-2], "\r\n"
    if line.endswith("\n"):
        return line[:-1], "\n"
    return line, ""


def upsert_keys_in_lines(
    lines: list[str], start: int, end: int, assignments: dict[str, str]
) -> None:
    """Update known TOML keys in one table without touching unrelated content."""
    remaining = dict(assignments)
    key_pattern = re.compile(r"^\s*([A-Za-z0-9_-]+)\s*=")
    for index in range(start, end):
        body, ending = split_line_ending(lines[index])
        match = key_pattern.match(body)
        if not match or match.group(1) not in remaining:
            continue
        key = match.group(1)
        indent = body[: len(body) - len(body.lstrip())]
        lines[index] = f"{indent}{key} = {remaining.pop(key)}{ending or chr(10)}"

    if remaining:
        lines[end:end] = [f"{key} = {value}\n" for key, value in remaining.items()]


def merge_codex_config(existing: str, generated: str) -> str:
    """Upsert only model_catalog_json while preserving every other setting."""
    try:
        generated_data = tomllib.loads(generated)
        if existing.strip():
            tomllib.loads(existing)
    except tomllib.TOMLDecodeError as exc:
        raise SyncError(f"Refusing to merge invalid TOML: {exc}") from exc

    if "model_catalog_json" not in generated_data:
        raise SyncError("Generated config is missing model_catalog_json")
    root_assignments = {
        "model_catalog_json": toml_string(str(generated_data["model_catalog_json"]))
    }

    lines = existing.splitlines(keepends=True)
    if lines and not lines[-1].endswith(("\n", "\r\n")):
        lines[-1] += "\n"

    first_table = next(
        (index for index, line in enumerate(lines) if line.lstrip().startswith("[")),
        len(lines),
    )
    upsert_keys_in_lines(lines, 0, first_table, root_assignments)

    merged = "".join(lines)
    try:
        tomllib.loads(merged)
    except tomllib.TOMLDecodeError as exc:
        raise SyncError(f"Merged Codex config is invalid; nothing was written: {exc}") from exc
    return merged


def atomic_write(path: pathlib.Path, content: str, mode: int) -> None:
    path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
    temporary = path.with_name(f".{path.name}.tmp-{os.getpid()}")
    try:
        descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, mode)
        with os.fdopen(descriptor, "w", encoding="utf-8") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        os.chmod(path, mode)
    finally:
        try:
            temporary.unlink()
        except FileNotFoundError:
            pass


def generate(args: argparse.Namespace) -> None:
    output_dir = pathlib.Path(args.output_dir).resolve()
    output_dir.mkdir(parents=True, exist_ok=True, mode=0o700)
    os.chmod(output_dir, 0o700)

    provider_id, base, api_key = read_codex_provider(
        pathlib.Path(args.codex_config).expanduser(), args.provider
    )
    model_payload = request_json(provider_models_url(base), api_key)
    model_ids = extract_model_ids(model_payload)
    definitions = extract_definitions(model_payload)
    template_catalog = fetch_template_catalog(args, output_dir)
    catalog, skipped = prepare_catalog(
        model_ids, definitions, template_catalog, tuple(DEFAULT_EXCLUDES) + tuple(args.exclude)
    )
    generated_ids = [str(model["slug"]) for model in catalog["models"]]

    catalog_path = output_dir / "models.json"
    config_path = output_dir / "config.toml"
    manifest_path = output_dir / "manifest.json"

    catalog_text = json.dumps(catalog, ensure_ascii=False, indent=2) + "\n"
    config_text = render_config(catalog_path)
    manifest = {
        "generated_at": dt.datetime.now(dt.timezone.utc).isoformat(),
        "base_url": base,
        "provider_id": provider_id,
        "models": generated_ids,
        "skipped": skipped,
        "template_source": args.template_file or args.template_url,
    }

    atomic_write(catalog_path, catalog_text, 0o644)
    atomic_write(config_path, config_text, 0o600)
    atomic_write(manifest_path, json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", 0o644)

    print(f"Generated {len(generated_ids)} Codex models")
    print(f"Config: {config_path}")
    print(f"Catalog: {catalog_path}")
    if skipped:
        print(f"Skipped: {', '.join(skipped)}")


def list_models(args: argparse.Namespace) -> None:
    _, base_url, api_key = read_codex_provider(
        pathlib.Path(args.codex_config).expanduser(), args.provider
    )
    payload = request_json(provider_models_url(base_url), api_key)
    for model_id in extract_model_ids(payload):
        print(model_id)


def validate_output(args: argparse.Namespace) -> None:
    output_dir = pathlib.Path(args.output_dir).resolve()
    catalog_path = output_dir / "models.json"
    config_path = output_dir / "config.toml"
    try:
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
        config = config_path.read_text(encoding="utf-8")
    except (OSError, json.JSONDecodeError) as exc:
        raise SyncError(f"Unable to read generated output: {exc}") from exc
    validate_generated_catalog(catalog)
    try:
        config_data = tomllib.loads(config)
    except tomllib.TOMLDecodeError as exc:
        raise SyncError(f"Generated config.toml is invalid: {exc}") from exc
    if set(config_data) != {"model_catalog_json"}:
        raise SyncError("Generated config.toml must contain only model_catalog_json")
    print(f"Valid Codex output with {len(catalog['models'])} models")


def install_output(args: argparse.Namespace) -> None:
    generate(args)
    output_dir = pathlib.Path(args.output_dir).resolve()
    generated_path = output_dir / "config.toml"
    target_path = pathlib.Path(args.codex_config).expanduser().resolve()
    try:
        generated = generated_path.read_text(encoding="utf-8")
        existing = target_path.read_text(encoding="utf-8") if target_path.exists() else ""
    except OSError as exc:
        raise SyncError(f"Unable to read config for installation: {exc}") from exc

    merged = merge_codex_config(existing, generated)
    if existing:
        stamp = dt.datetime.now(dt.timezone.utc).strftime("%Y%m%dT%H%M%SZ")
        backup_path = target_path.with_name(f"{target_path.name}.backup-{stamp}")
        atomic_write(backup_path, existing, 0o600)
        print(f"Backup: {backup_path}")
    atomic_write(target_path, merged, 0o600)
    print(f"Updated model_catalog_json in: {target_path}")
    print("All provider, authentication, model, and unrelated settings were preserved")


def add_common_arguments(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--codex-config", default=DEFAULT_CODEX_CONFIG)
    parser.add_argument("--provider", help="Provider id; defaults to model_provider in config.toml")


def add_generate_arguments(parser: argparse.ArgumentParser) -> None:
    add_common_arguments(parser)
    parser.add_argument("--output-dir", default=DEFAULT_OUTPUT_DIR)
    parser.add_argument("--template-url", default=DEFAULT_TEMPLATE_URL)
    parser.add_argument("--template-file")
    parser.add_argument("--exclude", action="append", default=[])


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(prog="CPAModelAdapter", description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)

    list_parser = subparsers.add_parser("list", help="List models exposed by the configured CPA")
    add_common_arguments(list_parser)
    list_parser.set_defaults(handler=list_models)

    generate_parser = subparsers.add_parser("generate", help="Generate Codex config and model catalog")
    add_generate_arguments(generate_parser)
    generate_parser.set_defaults(handler=generate)

    install_parser = subparsers.add_parser(
        "install", help="Generate models and upsert only model_catalog_json"
    )
    add_generate_arguments(install_parser)
    install_parser.set_defaults(handler=install_output)

    validate_parser = subparsers.add_parser("validate", help="Validate generated files")
    validate_parser.add_argument("--output-dir", default=DEFAULT_OUTPUT_DIR)
    validate_parser.set_defaults(handler=validate_output)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    try:
        args.handler(args)
    except SyncError as exc:
        print(f"cpa-model-adapter: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
