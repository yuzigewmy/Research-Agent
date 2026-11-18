# 研究工作台

研究工作台通过浏览器管理科研项目。项目记录保存在本机 SQLite 数据库中，关闭页面或重启服务后可重新打开。

## 本机运行

开发需安装 Go 1.27、Node.js 22.12 或更高版本和 pnpm 10.30.3。在仓库根目录执行以下命令。

```powershell
pnpm --dir web install --frozen-lockfile
pnpm --dir web run typecheck
pnpm --dir web run build
go test ./...
go build -trimpath -o ./dist/research-agent.exe ./cmd/research-agent
./dist/research-agent.exe --listen 127.0.0.1:8080 --data-dir ./data
```

在浏览器打开 <http://127.0.0.1:8080>。默认数据目录为 `data/`，数据库文件为 `projects.sqlite3`。可以用 `--web-dir` 指定构建后的界面目录，默认为 `web/dist`。开发前端时可在服务运行期间另行执行 `pnpm --dir web run dev`。

## 当前功能

- 从自由研究、文献综述、论文写作和实验计划四种入口创建项目。
- 浏览项目列表、打开项目详情，并通过地址中的项目编号再次打开同一项目。
- 创建请求带有请求编号，重复提交同一内容会返回相同的项目。
- 在项目详情向模型提问。对话和回复进度保存在本机，刷新或返回项目后可继续查看。
- 提议研究计划，确认后进入项目队列。任务可取消，生成的计划可下载为 Markdown。重启后可查看已完成的成果，未完成的独立任务会显示为已中断。
- 实验项目可按顺序生成研究范围、实验计划和验收清单。开启计划检查点后，工作流等到用户确认再继续。重启保留检查点和已完成的 Markdown。排队的步骤可自动接续，执行中断的步骤可手动继续。

## 连接对话模型

启动前设置 `RESEARCH_MODEL_BASE_URL` 和 `RESEARCH_MODEL_NAME`。模型服务需要支持 Chat Completions 流式接口。按服务要求设置 `RESEARCH_MODEL_API_KEY`。基地址填写服务前缀，例如 `https://api.deepseek.com`，应用会附加 `/chat/completions`。项目和已有会话可离线浏览。一个数据目录运行一个服务实例。

使用 GPT-6.2 Sol 的最高推理档位时，将 `RESEARCH_MODEL_NAME` 设为 `gpt-6.2-sol`，将 `RESEARCH_MODEL_REASONING_EFFORT` 设为 `max`。推理请求的 8192 token 上限包含推理和正文。
