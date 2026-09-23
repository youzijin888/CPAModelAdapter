# CPAModelAdapter：旧版 Python 用法

本页保留旧版仅适配模型的命令说明。新版数字供应商管理请参阅仓库根目录 README。

CPA 模型适配器：读取用户已经配置好的 CPA（CLIProxyAPI 或兼容接口），
将其模型列表适配为 Codex 的 `models.json`。不是供应商配置器。

**实验性工具。接口可调用、目录可解析，不等于当前运行中的 Codex 后台已加载模型元数据。**

## 使用边界

- 只读当前配置的 `model_provider`、对应 `base_url` 和 `env_key`。
- 使用当前进程已有的环境变量进行认证，不生成、打印或保存 API 密钥。
- 不修改供应商、认证方式、默认模型、MCP、权限、插件或历史记录。
- `generate` 只生成文件；`install` 只添加或更新顶层 `model_catalog_json`，先备份原配置。
- 不重启 Codex 后台，不重启 CPA，不修改系统环境变量。

## 环境要求

- Python 3.8 或更新版本；无需 `pip install` 或手动安装依赖。
- Python 3.11+ 使用内置 `tomllib`；Python 3.8–3.10 自动使用随仓库附带的 Tomli 解析器。
- 已配置的自定义 Provider，包含 `base_url` 和 `env_key`。
- 在运行适配器的终端中，`env_key` 指定的变量必须已存在。
- CPA 提供兼容的 `/v1/models` 接口。

默认读取 `$CODEX_HOME/config.toml`，未设置 `CODEX_HOME` 时读取 `~/.codex/config.toml`。
本版本不自动合并 Codex 的 profile、项目配置或命令行覆盖，也不解析其它认证方案。

## 快速开始

下载或克隆本仓库后，在项目目录运行：

```bash
python3 cpa_model_adapter.py list
python3 cpa_model_adapter.py generate
python3 cpa_model_adapter.py validate
```

也可使用同名入口：

```bash
./CPAModelAdapter generate
```

检查生成结果后，显式安装模型目录引用：

```bash
./CPAModelAdapter install
```

### 从旧版更新（包括 Mac 的 `No module named 'tomllib'` 报错）

在克隆的项目目录执行：

```bash
git pull --ff-only
./CPAModelAdapter generate
```

如果是下载 ZIP 的方式安装，请重新下载并解压整个仓库，不要只替换单个脚本。
需要保留 `_vendor/` 目录。兼容解析器已随程序附带，无需升级系统 Python。
项目路径可以包含空格。联网读取 CPA 和模型模板的要求不变。

安装修改的是现有配置中的这一项，不是用生成文件替换整个配置：

```toml
model_catalog_json = "/absolute/path/to/CPAModelAdapter/generated/models.json"
```

安装后不要随意移动项目目录：这里记录的是模型文件的绝对路径。
备份保存于目标 `config.toml` 同目录，名称形如 `config.toml.backup-<UTC时间>`。

## 参数

```bash
./CPAModelAdapter generate --codex-config /path/to/config.toml
./CPAModelAdapter generate --provider another-existing-provider
./CPAModelAdapter generate --output-dir /path/to/generated
./CPAModelAdapter generate --exclude 'unwanted-*'
./CPAModelAdapter generate --template-file /path/to/model-catalog.json
./CPAModelAdapter --help
```

`--provider` 只选择读取哪个已有供应商，不改变 Codex 当前供应商。
`install` 支持与 `generate` 相同的选项；`validate --output-dir` 校验指定输出目录。

## 生成文件

文件默认存放在程序所在目录的 `generated/`：

| 文件 | 用途 |
| --- | --- |
| `models.json` | Codex 模型目录 |
| `config.toml` | 仅含 `model_catalog_json` 的预览片段，不可拿来整份覆盖用户配置 |
| `manifest.json` | 模型列表、排除项、来源等生成信息，可能包含私人接口地址 |
| `.template-catalog-cache.json` | 下载的模板缓存 |

整个生成目录被 Git 忽略，不应上传。使用自定义输出目录时，需自行确保其不会进入版本控制。

## 适配方式与限制

1. 从用户现有 Provider 查询模型列表。
2. 对已知模型使用精确名称匹配的模板，并采用响应中可读取的元数据。
3. 缺少精确模板时，使用已确认的内置推理档位补充信息，其余字段继续使用回退模板。
4. 过滤图像、音频、嵌入等非目标模型，以及 `codex-auto-review`。
5. 校验目录必要字段，再逐文件原子写入。

推理档位的优先级：CPA 响应中的有效 `thinking.levels` → 精确模型模板 →
内置补充信息 → 通用回退模板。均缺失时才默认 `medium`。

当前内置补充信息包含 `gpt-6-astra` 的 `low / medium / high / xhigh / max`，
依据是 2026-09-05 核对的 CPA `model-definitions/codex` 模型定义。
它只在实时响应、精确模板未提供档位时使用，仅匹配完整模型名称，不套用到其它别名。
这只补充模型目录的 `supported_reasoning_levels`；不会修改全局 `model_reasoning_effort`。
本工具运行时不调用管理接口、不需要管理密钥，也不需要开启远程管理。

内置补充不是对所有供应商实际能力的保证；未知模型的其它回退元数据仍需核实。
如果之前的 Astra 只有 `medium`，更新程序后重新执行 `generate` 或 `install` 即可重建文件；
当前 Codex 运行时仍需加载更新后的目录。

实现参考 EasyCLIProxyAPI 的“运行时模型列表 + 精确模板 + 回退模板”思路。
程序默认在运行时从 `router-for-me/EasyCLIProxyAPI` 下载模型模板，失败时使用已有缓存；
首次使用需要可访问模板源，或使用 `--template-file` 提供兼容模板。
本仓库不包含第三方模板内容；第三方内容的使用权限以其原项目为准。

### 元数据警告和模型菜单

如果出现 `Model metadata for ... not found`，说明发出警告的运行时没有找到该模型的元数据，
不能只因为 `codex -m <model>` 能调用就认定适配完成。

可用 `codex debug models` 检查一个新启动的 CLI 读取到的目录。
但此命令与已运行的 app-server 可能使用不同的内存目录。
旧后台不会仅因再次生成文件或退出前端就一定刷新。

需要按实际启动方式重新启动后台；只有由 `codex app-server daemon` 管理的后台
才能使用它的重启命令。非托管后台应通过原来的启动程序或服务管理器处理。
重启可能中断其它任务，本程序不自动执行此操作。

目前不承诺所有客户端的 `/model` 菜单行为一致，也不承诺未知模型不会出现兼容性问题。

## 测试

```bash
python3 -m unittest -v
```

单元测试不访问真实 CPA、不需要真实密钥，也不写入用户 Codex 配置。

附带解析器的来源与许可证见 `THIRD_PARTY_NOTICES.md`。
