import json
import os
import pathlib
import tempfile
import unittest
from unittest import mock

import cpa_model_adapter


def template(slug=None):
    value = {
        "display_name": "Compatible Model",
        "base_instructions": "You are Codex, a coding agent.",
        "description": "Fallback",
        "context_window": 128000,
        "max_context_window": 128000,
        "supported_reasoning_levels": [{"effort": "medium", "description": "Medium"}],
        "shell_type": "shell_command",
        "visibility": "list",
        "supported_in_api": True,
        "default_reasoning_summary": "none",
        "support_verbosity": False,
        "truncation_policy": {"mode": "tokens", "limit": 10000},
        "supports_parallel_tool_calls": True,
        "experimental_supported_tools": [],
        "priority": 100,
    }
    if slug:
        value["slug"] = slug
    return value


class CPAModelAdapterTests(unittest.TestCase):
    def test_default_output_is_relative_to_program(self):
        self.assertEqual(
            pathlib.Path(cpa_model_adapter.DEFAULT_OUTPUT_DIR),
            pathlib.Path(cpa_model_adapter.__file__).resolve().parent / "generated",
        )

    def test_extract_definitions_from_standard_models_response(self):
        definition = {"id": "example-model", "context_length": 128000}
        self.assertEqual(
            cpa_model_adapter.extract_definitions({"data": [definition]}),
            {"example-model": definition},
        )

    def test_reads_existing_selected_provider_without_modifying_it(self):
        with tempfile.TemporaryDirectory() as directory:
            config_path = pathlib.Path(directory) / "config.toml"
            config_path.write_text(
                '''model_provider = "existing-cpa"
[model_providers.existing-cpa]
name = "Existing CPA"
base_url = "https://api.example.test/v1"
env_key = "EXISTING_CPA_KEY"
''',
                encoding="utf-8",
            )
            with mock.patch.dict(os.environ, {"EXISTING_CPA_KEY": "test-secret"}):
                provider_id, base_url, api_key = cpa_model_adapter.read_codex_provider(config_path)
            self.assertEqual(provider_id, "existing-cpa")
            self.assertEqual(base_url, "https://api.example.test/v1")
            self.assertEqual(api_key, "test-secret")

    def test_provider_models_url(self):
        self.assertEqual(
            cpa_model_adapter.provider_models_url("https://api.example.test/v1"),
            "https://api.example.test/v1/models",
        )
        self.assertEqual(
            cpa_model_adapter.provider_models_url("http://127.0.0.1:8317"),
            "http://127.0.0.1:8317/v1/models",
        )

    def test_extract_model_ids(self):
        payload = {"data": [{"id": "A"}, {"slug": "a"}, "B", {"name": "C"}]}
        self.assertEqual(cpa_model_adapter.extract_model_ids(payload), ["A", "B", "C"])

    def test_prepare_catalog_filters_non_codex_models(self):
        ids = ["gpt-5.6-sol", "gpt-image-2", "codex-auto-review"]
        definitions = {
            "gpt-5.6-sol": {
                "id": "gpt-5.6-sol",
                "display_name": "GPT 5.6 Sol",
                "context_length": 921000,
                "supported_parameters": ["tools"],
                "supportedInputModalities": ["text", "image"],
                "supportedOutputModalities": ["text"],
                "thinking": {"levels": ["low", "high", "max"]},
            },
            "gpt-image-2": {"id": "gpt-image-2"},
            "codex-auto-review": {"id": "codex-auto-review"},
        }
        source = {"fallback_model": template(), "models": [template("gpt-5.6-sol")]}
        catalog, skipped = cpa_model_adapter.prepare_catalog(
            ids, definitions, source, cpa_model_adapter.DEFAULT_EXCLUDES
        )
        self.assertEqual([item["slug"] for item in catalog["models"]], ["gpt-5.6-sol"])
        self.assertEqual(catalog["models"][0]["context_window"], 921000)
        self.assertEqual(catalog["models"][0]["default_reasoning_level"], "low")
        self.assertEqual(skipped, ["gpt-image-2", "codex-auto-review"])

    def test_unknown_model_uses_conservative_fallback(self):
        definitions = {
            "gpt-6-astra": {
                "id": "gpt-6-astra",
                "context_length": 272000,
                "supported_parameters": ["tools"],
                "supportedOutputModalities": ["text"],
                "thinking": {"levels": ["medium", "max"]},
            }
        }
        source = {"fallback_model": template(), "models": [template("known-model")]}
        catalog, _ = cpa_model_adapter.prepare_catalog(
            ["gpt-6-astra"], definitions, source, cpa_model_adapter.DEFAULT_EXCLUDES
        )
        model = catalog["models"][0]
        self.assertFalse(model["prefer_websockets"])
        self.assertFalse(model["supports_search_tool"])
        self.assertEqual(model["context_window"], 272000)

    def test_catalog_can_be_built_from_model_ids_without_management_api(self):
        source = {"fallback_model": template(), "models": [template("known-model")]}
        catalog, skipped = cpa_model_adapter.prepare_catalog(
            ["known-model", "unknown-model"], {}, source, cpa_model_adapter.DEFAULT_EXCLUDES
        )
        self.assertEqual(
            [model["slug"] for model in catalog["models"]],
            ["known-model", "unknown-model"],
        )
        self.assertEqual(skipped, [])

    def test_generated_config_contains_only_catalog_path(self):
        with tempfile.TemporaryDirectory() as directory:
            catalog_path = pathlib.Path(directory) / "models.json"
            content = cpa_model_adapter.render_config(catalog_path)
            self.assertEqual(
                content,
                f'model_catalog_json = "{catalog_path.resolve()}"\n',
            )

    def test_validate_generated_catalog_rejects_duplicates(self):
        model = template("duplicate")
        with self.assertRaises(cpa_model_adapter.SyncError):
            cpa_model_adapter.validate_generated_catalog({"models": [model, dict(model)]})

    def test_merge_adds_only_catalog_setting(self):
        existing = '''# keep this comment
model = "old-model"
model_provider = "old-provider"

[mcp_servers.example]
url = "https://example.test/mcp"

[model_providers.cliproxyapi]
name = "Old name"
base_url = "http://old.invalid/v1"
wire_api = "chat"
env_key = "OLD_KEY"
custom_setting = true
'''
        generated = cpa_model_adapter.render_config(pathlib.Path("/tmp/models.json"))
        merged = cpa_model_adapter.merge_codex_config(existing, generated)
        catalog_line = 'model_catalog_json = "/tmp/models.json"\n'
        self.assertEqual(merged.replace(catalog_line, ""), existing)
        self.assertIn('model = "old-model"', merged)
        self.assertIn('model_provider = "old-provider"', merged)
        self.assertIn('base_url = "http://old.invalid/v1"', merged)
        self.assertIn('env_key = "OLD_KEY"', merged)

    def test_merge_updates_only_existing_catalog_setting(self):
        existing = '''model = "gpt-5.6-sol"
model_provider = "existing-cpa"
model_catalog_json = "/old/models.json"

[model_providers.existing-cpa]
base_url = "https://example.test/v1"
env_key = "EXISTING_KEY"
'''
        generated = cpa_model_adapter.render_config(pathlib.Path("/tmp/models.json"))
        merged = cpa_model_adapter.merge_codex_config(existing, generated)
        expected = existing.replace(
            'model_catalog_json = "/old/models.json"',
            'model_catalog_json = "/tmp/models.json"',
        )
        self.assertEqual(merged, expected)


if __name__ == "__main__":
    unittest.main()
