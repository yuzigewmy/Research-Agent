# 首批并行任务

四项任务可从同一份 GitHub `main` 独立开工。每项使用一个分支和一份回归记录。功能编号对应 [验收台账](<acceptance.md>)。

## 开工与交接

1. 从最新的 `origin/main` 创建下表对应的分支。
2. 按任务卡的范围实现。验证一条实际使用路径，按改动运行定向 Go 测试或前端类型检查与构建。界面改动用 Chrome 核对一次。提交说明用中文，写清行为和验证结果。
3. 用 `git push -u origin HEAD` 推送任务分支，用 `git rev-parse HEAD` 取得提交 SHA。交回仓库地址、分支名、提交 SHA 和验证结果。主线开发者按该提交审核差异并复跑关键验证。审核通过后，由仓库维护者提 PR 并合并。

```bash
git clone https://github.com/yuzigewmy/Research-Agent.git
cd Research-Agent
git fetch origin main
git switch -c feat/material-name-search origin/main
```

上面的分支名对应第一项。其他任务使用表中的分支名。测试使用固定样例，提交内容限任务相关源码、测试与必要说明。命名与中文文案沿用项目约定。环境凭据和本机测试数据留在各自环境。

| 任务 | 建议分支 | 对应验收 |
|---|---|---|
| 资料按名称检索 | `feat/material-name-search` | F02 |
| 答卷按行号定位 | `feat/survey-row-search` | F10、T06 |
| 记住项目列表范围 | `feat/project-list-scope-preference` | F18 |
| 提示核对 AI 使用披露 | `feat/manuscript-ai-disclosure-check` | F12、T16 |

## 1. 资料按名称检索

在「项目资料」加文件名搜索。`GET /api/v1/projects/{id}/materials` 接受可选的 `q`，去除首尾空白后做字面子串匹配。先过滤本项目的资料，再按现有游标分页。筛选结果保留所有匹配版本。

- **通过条件**　上传两版同名资料与一个其他文件，按部分文件名可找到两版，原件下载照常。另一项目的同名资料不会出现在结果里。
- **分页**　切换或清空关键词时回到第一页。载入下一页沿用同一关键词。已选的引用和上传状态保留。
- **验证**　增加一个 API 回归，运行 `go test ./internal/workbench -run 'TestMaterials'`。通过 `pnpm --dir web run typecheck` 和 `pnpm --dir web run build` 后用 Chrome 验证搜索、清除与下载。

范围为 [materials_http.go](<../internal/workbench/materials_http.go>)、[materials_store.go](<../internal/workbench/materials_store.go>)、[materials_test.go](<../internal/workbench/materials_test.go>)、[materials.ts](<../web/src/materials.ts>)、[MaterialPanel.tsx](<../web/src/MaterialPanel.tsx>)。

## 2. 答卷按行号定位

在「问卷分析 → 答卷明细」添加按正整数定位。输入可匹配报告的答卷序号或 XLSX 原行号。两种编号各自对应不同答卷时显示两条，并保留界面现有的编号区分。空输入显示当前状态筛选的全部答卷。

- **通过条件**　对带空行的工作簿，输入原行号定位到对应答卷。CSV 可按答卷序号定位。状态筛选与行号条件同时生效，换条件后从第一页开始。
- **复核**　搜索或清除后保留未提交的复核草稿。已有报告读取与重算流程沿用原设置。
- **验证**　通过 `pnpm --dir web run typecheck` 和 `pnpm --dir web run build`，用 Chrome 验证在 XLSX 报告中过滤、清除及保留复核草稿。

范围为 [SurveyPanel.tsx](<../web/src/SurveyPanel.tsx>) 的答卷明细。直接使用报告中已有的 `row` 与 `sourceRow`。

## 3. 记住项目列表范围

记录「当前项目／归档项目」选择，同一浏览器重新打开工作台时恢复。首次打开或存储值无效时选择「当前项目」。沿用现有的项目搜索与分页逻辑。

- **通过条件**　切到归档项目后刷新，归档按钮的选中状态与列表一致。切回当前项目后刷新，恢复当前项目。搜索清除和项目深链仍可使用。
- **验证**　通过 `pnpm --dir web run typecheck` 和 `pnpm --dir web run build`，用 Chrome 验证两种列表范围切换后的刷新行为。

范围为 [ProjectList.tsx](<../web/src/ProjectList.tsx>)。使用浏览器本地偏好存储。

## 4. 提示核对 AI 使用披露

在确定性的「稿件结构检查」中补一项。识别常见中英文披露标题，例如「AI 使用披露」「人工智能使用声明」和「AI Use Disclosure」。报告记录标题行号。缺少对应标题时，提醒按投稿要求核对。识别结果只确认标题存在。

- **通过条件**　普通标题可识别并报告正确行号。位于代码围栏内的同名标题不计入。已有章节、待补提示和稿件摘要照常输出。
- **验证**　用小型 Markdown 样例覆盖标题存在、缺失及代码围栏。运行 `go test ./internal/workbench -run 'TestPaperCheck|TestPaperWorkflow|TestConversationManuscriptCheck'`。

范围为 [paper_check.go](<../internal/workbench/paper_check.go>) 和同包的 `paper_check_test.go`。

## 后续独立任务

- `feat/workspace-text-zoom` 对应 F13。编辑器提供四档字号并保存浏览器偏好。Chrome 核对调整时的草稿与选区，以及刷新和跨项目恢复。 [编辑器](<../web/src/WorkspacePanel.tsx>) · [样式](<../web/src/workspace.css>)
- `feat/manuscript-caption-number-check` 对应 F12、T16。稿件结构报告标出图和表各自的重复编号与源行号。用固定 Markdown 样例核对中英文题注、前导零、引用和代码围栏。 [结构检查](<../internal/workbench/paper_check.go>) · [结构检查回归](<../internal/workbench/paper_check_test.go>)

## 主线工作

资料库迁移、备份与恢复、受限执行与作业恢复、模型与多模态接入、工作流状态和跨版本产物由主线推进。各分支分别审核，优先合并已通过的任务。合并一批后依据改动范围复跑关键路径。
