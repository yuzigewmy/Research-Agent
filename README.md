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
- 按显示名或研究题目检索项目，切换当前与归档列表。项目管理可修改显示名、归档和恢复。归档项目仍能通过原地址打开。
- 创建请求带有请求编号，重复提交同一内容会返回相同的项目。
- 在项目详情向模型提问。对话和回复进度保存在本机，刷新或返回项目后可继续查看。
- 提议研究计划，确认后进入项目队列。任务可取消，生成的计划可下载为 Markdown。重启后可查看已完成的成果，未完成的独立任务会显示为已中断。
- 实验项目可按顺序生成研究范围、实验计划和验收清单。开启计划检查点后，工作流等到用户确认再继续。重启保留检查点和已完成的 Markdown。排队的步骤可自动接续，执行中断的步骤可手动继续。
- 项目资料可依次上传多个文件，按名称和 SHA-256 区分同名版本。每份文件上限为 64 MiB。可下载原件并预览 UTF-8 文本的前 64 KiB。备份时保留数据目录中的数据库和资料对象。
- 每轮对话可引用最多四份文本资料。模型收到所选文件各自最多 8 KiB 的 UTF-8 正文节选。会话历史保存文件名和版本，重开后仍可下载当时引用的原件。
- 文本工作区支持创建和编辑项目文件，例如 `paper/main.md`。单文件支持 512 KiB 的 UTF-8 正文。并发保存冲突时，编辑器保留本页草稿。恢复旧稿会把选中的内容保存为新版本。
- 启用 Word 组件后，可从当前稿件或指定历史版本生成 DOCX。标题、段落、基本强调、列表、代码和表格可以在 Word 中编辑。每个稿件版本保存自己的导出结果，可重复下载。
- 在问卷分析中选择项目内不超过 8 MiB 的 UTF-8 CSV，设置必答列和查重列，复核待处理答卷。每次分析保存来源文件版本、判定原因和有效答卷的统计汇总，可下载为 JSON。汇总会排除常见的姓名、电话和邮箱列。带 `__synthetic__` 列的资料会标记为演示数据。
- 文献综述项目支持在 [Crossref](https://www.crossref.org/documentation/retrieve-metadata/rest-api/) 检索文献，按 DOI 核对登记元数据并保存至项目文献库，可导出 BibTeX。检索词和待核对的 DOI 会发送给 Crossref，记录只代表元数据核对。
- 文献综述工作流从已核验库固定本轮来源，生成来源清单、主题规划和综述稿件。纳入上限默认为 20 条，可设为 10 至 80 条。可在主题规划后暂停确认。后续步骤使用同一份来源摘要，引用只能使用该清单的键。每步成果均可下载为 Markdown。完成的综述可保存到文本工作区，继续编辑并导出 Word。每轮稿件有独立路径，重复保存保留后续编辑。
- 论文写作项目可选择中文或英文，可粘贴已有大纲。工作流固定本轮设置和已核验文献，依次生成大纲、正文和带行号的结构检查，支持在大纲完成后暂停确认。检查报告包含所查稿件的内容摘要。稿件可保存到文本工作区编辑并导出 Word。
- 在编辑器和历史版本中，可将 64 KiB 以内的 Markdown 稿件提交模拟评审。报告按稿件版本与投稿目标保存，包括评分、判断、核对过的原文摘录和行号。可下载报告，失败或中断时可重试。提交和重试时，指定版本的正文和投稿目标会发送给已配置的模型。
- 在已保存的 Markdown 中选择最多 8 KiB 的文字，填写修改要求后生成建议。模型收到选区、相邻节选和修改要求。确认应用后只替换原选区并保存新版本。如果稿件已更新，界面保留建议并提示重新加载。
- 从评审报告选择一条编号意见，可据原文摘录生成定向修订。确认应用前可核对原文、替换稿和理由。稿件改版后，定向修订需要在当前稿件中唯一定位原文所在行。旧版报告可配合手工选区改稿。

## 启用 Word 导出

在专用 Python 环境安装 [Word 导出依赖](internal/wordexport/requirements.txt)，并把 `RESEARCH_WORD_PYTHON` 设为该环境的 Python 可执行文件绝对路径。重启服务即可在 Markdown 文件的编辑器和版本历史中导出。组件支持基础 Markdown，链接、图片和 HTML 作为文本保留。

```powershell
python -m venv .local/word
.\.local\word\Scripts\python.exe -m pip install -r internal/wordexport/requirements.txt
$env:RESEARCH_WORD_PYTHON = (Resolve-Path .local/word/Scripts/python.exe).Path
```

## 连接对话模型

启动前设置 `RESEARCH_MODEL_BASE_URL` 和 `RESEARCH_MODEL_NAME`。模型服务需要支持 Chat Completions 流式接口。按服务要求设置 `RESEARCH_MODEL_API_KEY`。基地址填写服务前缀，例如 `https://api.deepseek.com`，应用会附加 `/chat/completions`。项目和已有会话可离线浏览。一个数据目录运行一个服务实例。

使用 GPT-6.2 Sol 的最高推理档位时，将 `RESEARCH_MODEL_NAME` 设为 `gpt-6.2-sol`，将 `RESEARCH_MODEL_REASONING_EFFORT` 设为 `max`。推理请求的 8192 token 上限包含推理和正文。
