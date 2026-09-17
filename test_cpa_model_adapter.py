import json
import contextlib
import io
import os
import pathlib
import shutil
import subprocess
import sys
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
    def test_bundled_parser_handles_toml(self):
        from _vendor import tomli

        parsed = tomli.loads('''# preserved config syntax
model_provider = "example-cpa"
limits = [1, 2, 3]
[model_providers."example-cpa"]
base_url = "https://example.test/v1"
env_key = "EXAMPLE_API_KEY"
''')
        self.assertEqual(parsed["limits"], [1, 2, 3])
        self.assertEqual(parsed["model_providers"]["example-cpa"]["env_key"], "EXAMPLE_API_KEY")
        with self.assertRaises(tomli.TOMLDecodeError):
            tomli.loads('broken = [')

    def test_install_preserves_config_in_directory_with_spaces(self):
        with tempfile.TemporaryDirectory(prefix="CPA test with spaces ") as directory:
            config_path = pathlib.Path(directory) / "config.toml"
            output_dir = pathlib.Path(directory) / "model files"
            original = '''model = "example-model"
model_provider = "example-cpa"
[model_providers.example-cpa]
base_url = "https://example.test/v1"
env_key = "EXAMPLE_API_KEY"
[mcp_servers.example]
command = "keep-this-command"
'''
            config_path.write_text(original, encoding="utf-8")
            arguments = cpa_model_adapter.build_parser().parse_args([
                "install", "--codex-config", str(config_path), "--output-dir", str(output_dir)
            ])
            source = {"fallback_model": template(), "models": [template("example-model")]}
            with mock.patch.dict(os.environ, {"EXAMPLE_API_KEY": "private-test-key"}), \
                 mock.patch.object(cpa_model_adapter, "request_json", return_value={"data": [{"id": "example-model"}]}), \
                 mock.patch.object(cpa_model_adapter, "fetch_template_catalog", return_value=source), \
                 contextlib.redirect_stdout(io.StringIO()) as output:
                arguments.handler(arguments)
            updated = config_path.read_text(encoding="utf-8")
            setting = cpa_model_adapter.render_config(output_dir / "models.json")
            self.assertEqual(updated.replace(setting, ""), original)
            self.assertEqual(len(list(config_path.parent.glob("config.toml.backup-*"))), 1)
            self.assertTrue((output_dir / "models.json").is_file())
            self.assertNotIn("private-test-key", output.getvalue())

    def test_missing_stdlib_uses_bundled_parser(self):
        program = '''import sys, unittest
sys.modules["tomllib"] = None
import cpa_model_adapter
assert cpa_model_adapter.tomllib.__name__ == "_vendor.tomli"
tests = unittest.defaultTestLoader.loadTestsFromNames([
    "test_cpa_model_adapter.CPAModelAdapterTests.test_install_preserves_config_in_directory_with_spaces",
    "test_cpa_model_adapter.CPAModelAdapterTests.test_merge_updates_only_existing_catalog_setting",
    "test_cpa_model_adapter.CPAModelAdapterTests.test_bundled_parser_handles_toml",
])
result = unittest.TextTestRunner().run(tests)
sys.exit(0 if result.wasSuccessful() else 1)
'''
        result = subprocess.run(
            [sys.executable, "-S", "-c", program], capture_output=True, text=True,
            cwd=pathlib.Path(__file__).resolve().parent, timeout=30,
        )
        self.assertEqual(result.returncode, 0, result.stderr)

    def test_launcher_without_stdlib_in_path_with_spaces(self):
        source = pathlib.Path(__file__).resolve().parent
        with tempfile.TemporaryDirectory(prefix="CPA launcher with spaces ") as directory:
            destination = pathlib.Path(directory) / "CPAModelAdapter project"
            destination.mkdir()
            for filename in ("CPAModelAdapter", "cpa_model_adapter.py"):
                shutil.copy2(source / filename, destination / filename)
            shutil.copytree(source / "_vendor", destination / "_vendor")
            program = '''import pathlib, runpy, sys
sys.modules["tomllib"] = None
entrypoint = sys.argv[1]
sys.path.insert(0, str(pathlib.Path(entrypoint).parent))
sys.argv = [entrypoint, "--help"]
runpy.run_path(entrypoint, run_name="__main__")
'''
            result = subprocess.run(
                [sys.executable, "-S", "-c", program, str(destination / "CPAModelAdapter")],
                cwd=directory, capture_output=True, text=True, timeout=30,
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn("usage: CPAModelAdapter", result.stdout)

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

    def test_astra_has_bundled_efforts_without_api_metadata(self):
        for fallback_efforts in ([], [{"effort": "medium", "description": "Medium"}]):
            with self.subTest(fallback_efforts=fallback_efforts):
                fallback = template()
                fallback["supported_reasoning_levels"] = fallback_efforts
                catalog, skipped = cpa_model_adapter.prepare_catalog(
                    ["gpt-6-astra"], {"gpt-6-astra": {"id": "gpt-6-astra"}},
                    {"fallback_model": fallback, "models": [template("known-model")]},
                    cpa_model_adapter.DEFAULT_EXCLUDES,
                )
                model = catalog["models"][0]
                self.assertEqual(
                    [entry["effort"] for entry in model["supported_reasoning_levels"]],
                    ["low", "medium", "high", "xhigh", "max"],
                )
                self.assertEqual(model["default_reasoning_level"], "medium")
                self.assertEqual(skipped, [])

    def test_deepseek_has_bundled_efforts_without_api_metadata(self):
        for fallback_efforts in ([], [{"effort": "medium", "description": "Medium"}]):
            with self.subTest(fallback_efforts=fallback_efforts):
                fallback = template()
                fallback["supported_reasoning_levels"] = fallback_efforts
                catalog, skipped = cpa_model_adapter.prepare_catalog(
                    ["deepseek-flash"], {"deepseek-flash": {"id": "deepseek-flash"}},
                    {"fallback_model": fallback, "models": [template("known-model")]},
                    cpa_model_adapter.DEFAULT_EXCLUDES,
                )
                model = catalog["models"][0]
                self.assertEqual(
                    [entry["effort"] for entry in model["supported_reasoning_levels"]],
                    ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"],
                )
                self.assertEqual(model["default_reasoning_level"], "high")
                self.assertEqual(skipped, [])

    def test_deepseek_live_efforts_override_bundled_efforts(self):
        model = cpa_model_adapter.build_model_entry(
            "deepseek-flash", {"thinking": {"levels": ["low", "high"]}}, {}, template(), 1
        )
        self.assertEqual(
            [entry["effort"] for entry in model["supported_reasoning_levels"]],
            ["low", "high"],
        )
        self.assertEqual(model["default_reasoning_level"], "high")

    def test_astra_live_efforts_override_bundled_efforts(self):
        model = cpa_model_adapter.build_model_entry(
            "gpt-6-astra", {"thinking": {"levels": ["low", "high"]}}, {}, template(), 1
        )
        self.assertEqual(
            [entry["effort"] for entry in model["supported_reasoning_levels"]],
            ["low", "high"],
        )

    def test_astra_exact_template_overrides_bundled_efforts(self):
        model = cpa_model_adapter.build_model_entry(
            "gpt-6-astra", {}, {"gpt-6-astra": template("gpt-6-astra")}, template(), 1
        )
        self.assertEqual(
            [entry["effort"] for entry in model["supported_reasoning_levels"]], ["medium"]
        )

    def test_bundled_efforts_only_apply_to_exact_model_id(self):
        model = cpa_model_adapter.build_model_entry("gpt-6-astra-custom", {}, {}, template(), 1)
        self.assertEqual(
            [entry["effort"] for entry in model["supported_reasoning_levels"]], ["medium"]
        )

    def test_astra_bundled_efforts_ignore_model_id_case(self):
        model = cpa_model_adapter.build_model_entry("GPT-6-ASTRA", {}, {}, template(), 1)
        self.assertEqual(
            [entry["effort"] for entry in model["supported_reasoning_levels"]],
            ["low", "medium", "high", "xhigh", "max"],
        )

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
        self.assertEqual(merged.replace(generated, ""), existing)
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
            generated.strip(),
        )
        self.assertEqual(merged, expected)


if __name__ == "__main__":
    unittest.main()
