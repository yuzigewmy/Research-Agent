# 工程项目预览

用「工程项目」模板建项。在「文本工作区」保存单个 `.html`/`.htm` 文件，或保存包含入口 HTML 及关联静态资源（`.css`、`.js`、`.svg`、`.json`、`.txt`）的多文件静态工程。到「工程预览」刷新文件列表，选择已保存的入口 HTML 版本并启动。预览显示固定入口文件路径、版本、SHA-256 及已冻结挂载的关联静态资源清单。

每个工程项目有一个活动预览。启动预览时除冻结入口 HTML 版本外，还会按启动时刻的版本快照冻结同目录及子目录下最多 24 份关联静态资源。再次启动相同入口版本会复用原地址。保存入口文件或关联静态资源的新版本后，当前已冻结预览继续展示原快照内容，且跨服务重启保持一致。选择新版本入口重新启动后，旧预览地址及旧资源立即失效；停止预览或归档项目后，文档与全部静态资源同样立即返回 404。

预览采用隔离的浏览器框架。页面脚本在独立的沙箱源（`sandbox allow-scripts`）中运行，响应强制携带 `X-Content-Type-Options: nosniff`，内容安全策略允许内联脚本/样式、数据 URL 图片以及当前预览实例专属前缀 `/api/v1/projects/{id}/preview/{previewId}/` 下的已冻结同源静态资源，严格禁止越权访问其他项目路径、表单提交、外部网络或路径穿越（如 `../` 与 `%2e%2e`）。此版面向单文件与多文件纯静态前端工程；依赖安装、服务端代码运行、预览日志与远端执行节点属于后续工程能力。 [浏览器沙箱指令](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Content-Security-Policy/sandbox)

管理接口为 `/api/v1/projects/{id}/preview`。`GET` 读取活动预览及已冻结关联资源清单，`POST` 提交 `versionId` 启动预览。`DELETE /{previewId}` 关闭指定预览，旧编号不能关闭后续开启的实例。入口文档位于 `GET /{previewId}/document`，关联静态资源位于 `GET /{previewId}/{assetPath...}`（按扩展名返回规范 `Content-Type` 与 `X-Source-SHA256`），便于入口 HTML 直接以 `./style.css`、`./app.js` 等相对路径加载。

Go 回归核对版本固定、多文件冻结快照、跨重启一致、防路径穿越、模板和项目范围、替换、停止与归档失效。Chrome 联测核对单文件与多文件（`app/index.html` + `app/style.css` + `app/app.js`）样式渲染与脚本 DOM 交互、改版冻结隔离及停止后资源失效。 [工程预览回归](<../internal/workbench/engineering_preview_test.go>)
