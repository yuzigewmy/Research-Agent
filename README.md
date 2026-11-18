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
