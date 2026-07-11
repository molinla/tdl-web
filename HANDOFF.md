# tdl-web 改造交接

## 架构

同仓库解耦：

| 部分 | 路径 | 说明 |
|------|------|------|
| API 服务 | `tdl web` → `app/web/` | 只提供 JSON API + SSE，不再内嵌 HTML |
| 前端 | `web/` | Vite + React Netflix 风预览站 |

```powershell
# 终端 1：API
.\tdl-web.exe web -f .\result.json -d "F:\telegram" --takeout --skip-same --continue --type id -i 100,500

# 终端 2：前端
cd web
npm install
npm run dev
```

打开：`http://127.0.0.1:5173/`  

前端媒体请求直连 `http://127.0.0.1:8080`（不走 Vite 代理），否则大视频 Range 流会被卡住导致“点播放没反应”。可用环境变量 `VITE_API_BASE` 覆盖 API 地址。

## CLI 范围过滤

- `--type id|time`：空 = 不过滤
- `-i from,to`：闭区间（只给一个数时 to = MaxInt）

`id` 按消息 ID 过滤；`time` 优先用 JSON `date_unixtime`，缺省再按 Telegram `msg.Date` 过滤。过滤后再算 resume fingerprint。

## 下载暂停 / 续传

- **全局并发**：所有落盘共用一个 `-l/--limit` 队列（默认 2），不会无限制并行打 Telegram
- **视频**：导入后保持 `queued`；**播放**时优先入队落盘；**关闭播放器**会 `POST /api/items/{id}/pause`，保留 `.tmp`
- **图片**：导入结束后按 `-l` 排队自动落盘（不再一上来全开）
- **下载全部** / 单条 cache：同样进入全局队列
- `--continue` 启动后会自动续传已有 `.tmp` / `paused` 的项（走同一队列；不会自动下全部 queued 视频）
- 续传：单线程按 offset 顺序写，从已有 `.tmp` 大小继续
- 卡片经 SSE 实时显示 `caching` / `paused` / `completed` 与百分比
- meta 缓存带 `expected_total`；导入中途不写缓存，残缺缓存会自动重建

导入改为边解析边推送：每处理约 25 条通过 SSE/`/api/items` 刷新列表。

进度字段：`import_total`、`import_done`、`import_items`（已入库条数）。`POST /api/import` 立即返回，后台继续导入。

## 列表如何生成（渲染原理）

以前：JSON 只取 message id → **每条** `GetSingleMessage` 打 Telegram → 很慢（“每 25 条”只是 SSE 刷新批次，不是批量 API）。

现在：官方导出 JSON（如 `file_name` / `file_size` / `mime_type` / `duration_seconds` / `media_type`）**直接生成列表**；只有点击播放/下载/封面时才按需请求 Telegram 拿 file location。本地已有文件仍走本地。

解析结果落盘到 `{cache-dir}/meta/{fingerprint}.json`。

- 默认：同 JSON/范围再次启动时直接读缓存秒开列表（仍会解析 JSON 算 fingerprint，但不再逐条打 Telegram）
- `--refresh-meta`：忽略缓存，强制重拉
- 本地已有文件时播放仍走本地；未缓存文件在播放/下载时按需补拉 location

| 方法 | 路径 | 用途 |
|------|------|------|
| POST | `/api/import` | 上传 JSON；form/query: `type`,`from`,`to` |
| GET | `/api/items` | 列表快照 |
| GET | `/api/events` | SSE |
| GET | `/api/items/{id}/thumb` | 封面/缩略图 |
| GET | `/api/items/{id}/preview` | 图片直预览 |
| GET | `/api/items/{id}/stream` | 视频边播边下到 `--dir` |
| POST | `/api/items/{id}/cache` | 仅落盘 |
| POST | `/api/items/download` | 批量落盘 `{ "ids": [] }` |
| GET | `/api/health` | 健康检查 |

Item 字段要点：`type`=`video|image|file`，视频含 `duration`/`cover`/`stream_url`，图片含 `preview_url`。

## 关键源码

- `cmd/web.go` — CLI + 范围参数
- `app/web/server.go` — 启动、CORS、路由
- `app/web/import.go` — 导入 + 范围 + resume
- `app/web/download.go` — 落盘缓存
- `app/web/handlers.go` — HTTP handlers
- `app/web/media.go` — 元数据 / 时长
- `web/` — 前端

## 构建验证

```powershell
go test ./app/web ./app/dl ./cmd
go build -o outputs\tdl-web.exe .
cd web && npm run build
```

## 已知限制

- 续传仍是条目级（`--continue`），不复用 `.tmp` 字节
- 大 JSON 时页面先开，列表等后台解析
- 前端不打进 exe；需双进程
