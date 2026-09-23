# CPAModelAdapter · `cpa`

用数字编号管理 **Codex 连接的 API 供应商**，自动生成并切换模型目录。
不操作 CLIProxyAPI Docker、代理、CF Tunnel 或网络切换服务。

新版用 Go 编译为单个可执行文件，运行时不需要 Python、Go 或额外依赖。
源码、安装脚本和旧 Python 入口可以共存；旧用法见 [兼容说明](docs/legacy-python.md)。

## 命令

```text
cpa init       初始化，创建配置 1
cpa add        添加地址和密钥，自动分配新编号
cpa status     查看当前配置及本地检查结果，不联网
cpa list       列出全部编号和地址，标记当前项，不显示密钥
cpa use 2      验证接口、生成模型并切换到配置 2
cpa delete 2   确认后删除配置 2
cpa sync       更新当前配置的模型列表
cpa restart    确认后重启当前配置对应的 Linux Codex 后端
cpa -help      帮助（也支持 --help、-h）
```

单独执行 `cpa` 只提示使用 `cpa -help`，以非零状态退出，不进入菜单。

### 初始化与添加

`init` 自动检测已有 Codex 配置：提示具体接管范围、先确认、再导入地址和当前进程环境中的密钥。
没有可导入的配置时手动输入 API 地址及密钥。密钥输入不回显，需要交互终端，不能写在命令参数里。
接口地址默认使用 HTTPS；根地址自动补 `/v1`，自定义路径原样保留，勿填 `/models`。
模型模板已内置在可执行文件中，首次运行只需能读取你填写的 API 的 `/models` 接口。
不需要访问 GitHub，不需要提前准备模板缓存，不会请求 CPA 管理接口。

模型自动生成，无需再分别执行 `generate` 和 `install`。
默认模型如果不存在，提示选择；原推理档位不适用于新模型时也会要求明确选择，不静默改动。

`add` 只添加，不改变当前选择，最后询问是否立即切换。
重复 `init` 拒绝覆盖，提示用 `add`。

### 编号与删除

- 初始化编号为 `1`，后续单调递增；删除 `2` 后，`3` 不会变成 `2`，下次添加是 `4`。
- 删除非当前项会移除其地址、密钥及模型文件。
- 删除当前项必须先选择另一项，并且目标验证和切换成功后才完成删除。
- 不允许删除唯一的当前项；先添加另一项。
- 删除不是安全擦除，不能保证磁盘、文件系统快照或用户备份中不存在历史副本。

## 安装

使用自己系统和 CPU 架构对应的构建包：Linux/macOS 的 `cpa`，Windows 的 `cpa.exe`。
包内附安装脚本；安装器不下载程序、不使用管理员权限，不会初始化或迁移 Codex。
可执行文件安装到用户的 `.local/bin`；终端集成让你能在任意目录使用 `cpa`。

**Linux / macOS（Bash、Zsh）**

在解压后的目录运行：

```sh
sh install.sh
```

重新打开终端，然后：

```sh
cpa -help
cpa init
cpa status
```

从源码构建后可指定二进制路径，例如 Radxa ARM64：

```sh
sh install.sh ./dist/cpa-linux-arm64
```

**Windows（PowerShell）**

```powershell
.\install.ps1
# 重新打开 PowerShell
cpa -help
cpa init
```

若组织的执行策略阻止脚本，请按本机策略处理；安装器不自动降低全局执行策略。
Windows cmd.exe、Fish 暂无环境刷新集成；不要把二进制能运行误认为当前终端密钥已经刷新。
macOS 下载的未签名二进制可能需要按系统提示授权；目前不提供签名/公证承诺。

### 安装器修改什么

- 复制程序到用户 `.local/bin`。
- Bash/Zsh：生成 `~/.cpa/shell.sh`，在 `.bashrc`、`.zshrc`、`.zshenv` 和 Bash 实际登录配置中添加带标记的加载块。`.bashrc` 和 `.zshenv` 的加载块放在文件开头，确保 SSH 非交互启动不会被提前 `return` 跳过。
- Windows：生成 `~/.cpa/shell.ps1`，添加到检测到的 Windows PowerShell / PowerShell 7 用户 profile。
- 修改已有终端配置前保存私密备份；只更新自己的标记块，重复安装不重复追加。
- 不修改系统 PATH、系统环境变量、旧供应商密钥声明、Codex 配置或会话数据。

第一次安装后需重开终端（或者执行安装器输出的加载命令），因为子进程不能改变父终端环境。
不要在源码构建目录随意执行不受信任版本的安装器。

## 统一配置与安全边界

默认用户数据目录：`~/.cpa`，Windows 为用户主目录下的 `.cpa`。

```text
~/.cpa/
  providers.json        # 唯一长期凭据文件：全部地址、密钥、当前编号、下一个编号
  models/1.json         # 各供应商独立模型目录
  models/2.json
  shell.sh / shell.ps1  # 无凭据的终端集成
  backups/              # 原 Codex/终端配置备份（按私密数据保护）
```

全新 Codex 配置默认使用以下 ID；已有自定义供应商保留原 ID（例如 `xmlearningtech`），并在私密状态中记录 `codex_provider`，后续 `use` / `sync` 不再改名：

```toml
model_provider = "cpa"
model_catalog_json = "/absolute/path/to/.cpa/models/1.json"

[model_providers.cpa]
name = "CPA"
base_url = "https://example.com/v1"
env_key = "CPA_API_KEY"
wire_api = "responses"
```

这只是所管理字段的示例，**程序不会整份覆盖用户的 config.toml**。

- 接管根级供应商选择、模型目录、默认模型和当前供应商节点。推理档位仅在不兼容且用户明确选择后调整。
- 从旧供应商迁移时保留其 ID，仅接管对应节点；其他供应商保留。共享该节点的 profile 必须先解除引用，避免静默改变其认证设置。
- 其他 TOML 内容按字节保留，并做语义一致性校验；不删除 MCP、权限、插件、auth.json 或会话文件。
- 不修改会话数据库或历史文件。旧版已经改为 `cpa` 的安装不会猜测原 ID；恢复时需依据初始化前备份，同步恢复根级 ID、对应 provider 表和 `providers.json` 的 `codex_provider`。旧版没有该字段的状态仍按 `cpa` 读取。
- 活跃的默认 profile、冲突的 `cpa` 节点、无法安全编辑的内联/点号供应商写法、符号链接目标会明确拒绝，不强行重写。
- 项目级配置、profile、命令行覆盖仍可能优先于用户配置。工具不会擅自接管这些位置。

密钥以**明文**保存，Linux/macOS 目录 `0700`、文件 `0600`；Windows 设置仅当前用户及 SYSTEM 访问的 ACL。
这不是加密凭据库，管理员或同账号程序仍可能读取。
私密备份可能包含旧配置内原有的敏感内容，不能上传。进程中断恢复日志可能短暂包含凭据，正常事务完成后删除。

### 一个环境变量如何切换

终端包装层在新终端启动时，以及成功命令结束后，从私密文件加载当前密钥到 **`CPA_API_KEY`**。
密钥不会重复写入 shell 配置或系统环境变量存储。不使用 `eval` 解析密钥。

安装后从正常终端运行 `cpa use 2`，然后重新启动 Codex CLI。
直接运行绝对路径的二进制可修改配置，但**不能**刷新父终端；程序会明确提醒。
`cpa status` 区分“凭据文件已配置”和“当前进程环境匹配”，不以本地状态声称接口联网正常。

已经运行的 Codex/App 不会热切换。SSH 启动链路通过 Bash `.bashrc` 或 Zsh `.zshenv` 加载凭据后，必须重启远程 Codex 后端；仅在另一个终端 `source` 或重连已有常驻后端不够。`cpa status` 只检查当前进程，不宣称已运行的后端也有密钥。绕过这些 shell 启动文件的服务或桌面启动器仍需显式加载环境。
内部 `_key` 是供包装层管道读取的敏感接口，不供手动使用；不要开启 PowerShell 语句级调试/转录来记录凭据加载过程。

### Linux SSH 后端重启

`init`、`use`、`sync` 以及实际切换了当前配置的 `add` / `delete` 成功保存配置后，会检测当前用户、当前 `CODEX_HOME` 对应的 Linux 原生 `codex app-server` 进程，并询问是否现在重启。输入 `y` 才会继续；回车、拒绝或输入结束都不会停止进程。重启可能中断这些后端中的运行任务，程序不能仅凭进程信息判断它们空闲。

也可稍后从独立 SSH 终端执行 `cpa restart`。程序不处理普通 Codex CLI、其他用户、其他配置目录或无法安全识别的 profile / provider 命令行覆盖；不会重启当前命令所属的 Codex 祖先进程。Linux 内核必须支持 pidfd，程序核对进程启动时间后仅发送 `SIGTERM`，不使用模糊 `pkill` 或 `SIGKILL`。

凭据文件锁在重启前释放，避免新 SSH 启动时 `_key` 因锁冲突读不到密钥。之后等待最多 15 秒，只有确认旧进程退出、足够数量的新后端出现且其 `CPA_API_KEY` 与当前配置一致，才报告重启验证成功。该检查不代表模型接口调用成功。

重新启动由已连接的 Codex 桌面端负责；如果客户端没有自动重连，程序会明确提示在桌面端重新连接，不会创建没有客户端的替代后端。重启失败不会回滚已经保存的配置，也不需要重复 `cpa init`。macOS / Windows 暂不提供自动后端重启，请手动退出并重新启动 Codex。

### 失败与恢复

接口失败、无可用模型、取消选择时不切换当前配置。
写入采用私密恢复日志和原子文件替换；中途写入失败会回退，进程中断后下一次修改命令会恢复未完成事务。
恢复未完成时 `status/list` 和凭据读取会拒绝读取混合状态。
跨进程文件锁防止两条 CPA 命令同时修改。
写入前备份 Codex 配置，并检查交互期间的外部编辑。

这不是磁盘断电/硬件损坏的完整容灾系统，也不能控制同时运行的第三方配置编辑器。

## 模型适配

沿用原 Python 适配器的模型过滤、精确模板与未知模型回退策略。
程序内置固定版本的 OpenAI Codex 官方模型目录，来源和校验值见 `cmd/cpa/assets/README.md`。
兼容旧版 `fallback_model + models` 和新版仅 `models` 的模板结构；自动补齐从
`model_messages.instructions_template` 迁移的指令字段，保留原始 `model_messages`。
没有明确模板的未知模型使用自有保守回退（缺少实际元数据时上下文默认 32768），不继承其他模型的高级工具策略。
精确模板里的 Fast 等字段会保留；未知模型不会凭空宣称 Fast/WebSocket/搜索能力。
目录包含某个字段不等于当前 Codex 前端一定显示相应开关。
默认不下载模板，也不依赖旧的 `template-cache.json`。
模型 ID 列表仍从当前供应商实时读取，内置模板随程序版本更新；可显式指定本地模板覆盖。

高级/测试覆盖：

```text
CPA_HOME           独立数据目录
CPA_CODEX_CONFIG   显式指定 config.toml
CODEX_HOME         未指定 CPA_CODEX_CONFIG 时，从这里找 config.toml
CPA_TEMPLATE_FILE  指定本地 JSON 模型模板（models 必需，fallback_model 可选）
```

数据目录绑定初始化时的 Codex 配置路径，不允许无提示地跨用户/配置写入。

## 开发与构建

仅开发者需要 Go，终端用户不需要：

```sh
go test ./cmd/cpa
go vet ./cmd/cpa
go build -trimpath -o dist/cpa ./cmd/cpa
go run ./tools/build
```

最后一条命令生成 Windows、macOS、Linux 的 AMD64/ARM64 可执行文件、带安装器的压缩包和 SHA256 校验清单。
CI 在三种系统运行 Go 测试，并保留旧 Python 测试。交叉编译通过不等于 Windows/macOS 真机运行已验证。

测试使用隔离目录、虚构密钥与本机模拟 HTTP 服务，不连接真实供应商或改动开发者的 Codex 配置。
