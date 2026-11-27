# 受限本机脚本执行规格

本文档确定研究工作台受限本机脚本（F09）的运行器、路径、网络、资源、状态流转与发布边界。该能力用于在同一项目权限边界内，经用户明确授权后运行项目文本工作区中已保存的固定版本 Python 脚本，并将校验通过的产物按版本发布回工作区。

## 1. 允许的运行器与环境边界

- **显式配置运行器**：仅支持部署端通过环境变量 `RESEARCH_SCRIPT_PYTHON` 显式指定的 Python 可执行文件路径。未配置或为空白时，该能力处于未启用状态，服务端直接返回 `503 CAPABILITY_MISSING`，绝不自动搜索系统 `PATH` 或猜测解释器。
- **隔离启动参数**：子进程统一以 `-I -B -X utf8 -c <内置启动包装器> <sandbox>/script.py` 启动：
  - `-I`：启用隔离模式，忽略用户 `site-packages`、`PYTHONPATH` 等 `PYTHON*` 环境变量，也不将工作目录加入模块搜索路径；
  - `-B`：禁止写入 `.pyc` 字节码缓存；
  - `-X utf8`：统一使用 UTF-8 读写标准输出、标准错误与文本文件。
- **最小环境变量白名单**：子进程不继承服务端完整环境。仅透传运行时启动所需的极简系统变量（`SYSTEMROOT`、`SYSTEMDRIVE`、`WINDIR`、`COMSPEC`、`LANG`、`LC_ALL`、`LC_CTYPE`），并显式注入：
  - `TEMP`、`TMP`、`TMPDIR`：全部指向本次运行独立沙箱内的 `tmp/` 目录；
  - `NO_PROXY=*`、`no_proxy=*`，并将 `HTTP_PROXY`、`HTTPS_PROXY`、`ALL_PROXY` 置空；
  - `RESEARCH_OUTPUT_DIR`：本次运行沙箱内的 `outputs/` 目录绝对路径；
  - `RESEARCH_INPUT_PATH`：若用户指定了输入文件版本，则为沙箱内 `inputs/<safeBasename>` 的绝对路径。
  - **严禁**透传任何模型 API Key、计算节点 Token 或其他部署凭据。

## 2. 网络限制边界

- 内置只读启动包装器在执行用户脚本之前，拦截标准库 `socket` 与底层 `_socket` 的网络连接入口（`socket`、`create_connection`、`create_server`、`socketpair`、`fromfd`），任何网络连接尝试都会抛出 `PermissionError("受限脚本模式禁止网络访问")`。
- 结合代理清空与 `NO_PROXY=*`，阻止标准库网络客户端（含 `socket`、`http.client`、`urllib` 等）发起对外或本机网络请求。
- 说明：该机制属于进程内标准库拦截，不替代操作系统级容器、沙箱命名空间或防火墙隔离；用户仍须在勾选授权前确认所选脚本内容可信。

## 3. 路径与产物边界

- **脚本输入**：仅允许执行当前项目文本工作区中已保存的 `.py` 文件版本，文件大小上限 **64 KiB**（65,536 字节）。
- **可选数据输入**：最多附加 1 份当前项目文本工作区中已保存的文件版本，文件大小上限 **512 KiB**（524,288 字节）。
- **项目隔离**：脚本版本与输入版本必须属于当前请求路径指定的项目，跨项目版本一律拒绝（返回 `404`）；跨项目查询、停止与产物发布均返回 `404`。
- **独立临时沙箱**：每次运行创建全新临时目录，包含固定结构：
  - `script.py`：所选脚本版本的原始字节（写入前复核 SHA-256）；
  - `inputs/<safeBasename>`：可选输入版本的原始字节（写入前复核 SHA-256，文件名仅保留单层安全名称）；
  - `outputs/`：唯一允许脚本写入产物的目录；
  - `tmp/`：临时文件目录。
  - 子进程工作目录固定为该沙箱根目录；运行结束后自动清理整个临时沙箱目录。
- **产物扫描与校验**：仅当脚本正常退出（退出码 `0`）且未被停止或超时时扫描 `outputs/`：
  - 仅接受单层常规文件（拒绝子目录、符号链接/重解析点、含路径分隔符或控制字符的文件名）；
  - 产物须为有效 UTF-8 文本且不含空字节（`\0`），便于后续发布至文本工作区；
  - 数量上限 **4 份**，单份上限 **512 KiB**（524,288 字节），总计上限 **1 MiB**（1,048,576 字节）；
  - 逐份计算字节长度与 SHA-256，以内容寻址对象写入对象库并登记于数据库。
- **校验后发布**：发布接口核对运行终态为 `SUCCEEDED`、产物名称与请求提供的 SHA-256 与运行清单一致，并重新读取对象库原字节复核长度与 SHA-256 后，写入项目文本工作区 `scripts/<runId>/<name>`。重复发布同一产物返回原工作区版本，不覆盖后续人工编辑。

## 4. 资源、时限、并发、进度与日志

- **明确授权与幂等**：提交请求必须携带 `requestId`、`scriptVersionId`、可选 `inputVersionId`、`timeoutSeconds` 与 `confirmed: true`。未显式传入 `confirmed: true` 返回 `400`。同一项目内相同 `requestId` 且参数一致的重放返回原运行回执（HTTP `200`）；同 `requestId` 但参数不同返回 `409`。
- **时限与并发**：`timeoutSeconds` 限制在 **1–30 秒**（默认 **10 秒**），登记在运行记录中（同时登记 `maxOutputFiles: 4`、`maxOutputFileBytes: 524288`、`maxLogBytes: 16384`）。同一项目同时最多运行 **1 笔**受限脚本作业，已有排队中/运行中/停止中作业时返回 `409`。
- **状态流转**：`QUEUED` → `RUNNING` → `SUCCEEDED` | `FAILED` | `TIMED_OUT` | `CANCELLING` → `CANCELLED` | `INTERRUPTED`。服务重启或从备份打开时，未完成作业统一标记为 `INTERRUPTED`。
- **进度阶段**：`已排队` → `正在准备沙箱` → `正在执行脚本` → `正在校验产物` → `已完成` / `已超时` / `已取消` / `执行失败` / `已中断`。
- **有界日志**：分别记录 `stdout` 与 `stderr`，各自上限 **16 KiB**（16,384 字节），超出部分截断并置位 `stdoutTruncated` / `stderrTruncated`。
- **停止语义**：停止请求将活动作业置为 `CANCELLING` 并终止子进程，终态记为 `CANCELLED`，且拒绝任何迟到产物入库。

## 5. HTTP 接口与数据库迁移（v63 → v64）

- `GET /api/v1/projects/{id}/script-runs`：返回运行器配置状态（`configured`）、技术上限元数据（`limits`）与最近运行记录（`items`）。
- `POST /api/v1/projects/{id}/script-runs`：提交并授权一次受限脚本运行。
- `GET /api/v1/projects/{id}/script-runs/{runId}`：查询单次运行状态、进度、日志与待发布产物清单（含 SHA-256）。
- `POST /api/v1/projects/{id}/script-runs/{runId}/cancel`：停止运行中的作业。
- `POST /api/v1/projects/{id}/script-runs/{runId}/artifacts/publish`：按 `{ "name", "sha256" }` 复核并发布产物至文本工作区 `scripts/<runId>/<name>`。
- 数据库第 64 版新增 `script_runs` 与 `script_run_artifacts` 表；工作区备份（v64）将 `script_run_artifacts` 引用的内容对象纳入清单并随备份恢复保留，同时保持对第 50–63 版历史备份的兼容升级。
