# Bundled Codex catalog

`codex-models.json` is an unmodified snapshot of OpenAI Codex's model catalog.

- Repository: `https://github.com/openai/codex`
- Commit: `ddf04ad26789d040f9ef6a96736f76602e35a6cc`
- Source path: `codex-rs/models-manager/models.json`
- Retrieved: 2026-09-06
- SHA256: `d7136a413cfac1b5b1686d9e0dcc5c80ca05bebed5e9fc3911376561d0ef6ee8`
- License: Apache-2.0; see `docs/licenses/CODEX-APACHE-2.0.txt` and
  `docs/licenses/CODEX-NOTICE.txt` at the repository root.

Go embeds this file into the executable. No template download is performed by
`init`, `add`, `use`, or `sync`. These commands contact only the user-specified
provider's models endpoint. Explicit `CPA_TEMPLATE_FILE` overrides remain supported.

At runtime the adapter adds `base_instructions` from
`model_messages.instructions_template` when necessary, and supplies its own
conservative fallback for catalogs without `fallback_model`. It does not replace
the native `model_messages`, reasoning levels, or speed metadata of exact models.

Update this snapshot deliberately, retain upstream licensing, update its checksum
and provenance here, and run the bundled-catalog compatibility tests. Do not copy
private provider catalogs or credentials into this directory.
