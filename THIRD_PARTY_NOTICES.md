# Bundled third-party components

## Tomli 2.2.1

- Upstream: `https://github.com/hukkin/tomli`
- Release: `https://pypi.org/project/tomli/2.2.1/`
- License: MIT; full text retained in `_vendor/tomli/LICENSE`.
- Purpose: offline TOML parsing on Python 3.8–3.10, which lack `tomllib`.
- Source artifact: `tomli-2.2.1-py3-none-any.whl` from PyPI.
- Artifact SHA256: `cb55c73c5f4408779d0cf3eef9f762b9c9f147a77de7b258bef0a5628adc85cc`.

The four Python source files are included unchanged under `_vendor/tomli/`.
Python 3.11 and newer use the standard library parser instead.

## Go binary dependencies

### Bundled model catalog — OpenAI Codex, Apache-2.0

The executable embeds an unmodified model catalog from OpenAI Codex commit
`ddf04ad26789d040f9ef6a96736f76602e35a6cc`, source path
`codex-rs/models-manager/models.json`. See `cmd/cpa/assets/README.md` for the
retrieval date and checksum. Compatibility normalization occurs at runtime;
the adapter adds a legacy base-instructions field where needed and its own
conservative fallback. Original exact-model metadata is preserved.

The full Apache-2.0 license and upstream NOTICE are retained in
`docs/licenses/CODEX-APACHE-2.0.txt` and `docs/licenses/CODEX-NOTICE.txt` and
included in release archives. Copyright 2025 OpenAI.

### Go libraries

The Go binary includes go-toml v2 (MIT) and golang.org/x/term,
golang.org/x/sys, and the Go runtime (BSD-3-Clause). Exact module versions
and integrity checksums are recorded in go.mod and go.sum.
Tomli is only used by the legacy Python entrypoint, not by the Go binary.

### go-toml v2 — MIT

Copyright (c) 2021 - 2023 Thomas Pelletier

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.

### Go runtime, x/term, x/sys — BSD-3-Clause

Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
