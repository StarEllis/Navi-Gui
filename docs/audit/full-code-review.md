# Navi-Gui 系统性代码审查报告

审查基准：分支 `codex/artwork-cache-gfriends`，提交 `3ed7671`。本轮没有修改、格式化或升级任何已跟踪源码。

## A. 执行摘要

### 结论

当前项目属于“功能型 Beta”：核心媒体库流程已经形成闭环，能够成功完成 TypeScript、Vite、Go 和 Wails 生产构建；但扫描删除、覆盖刷新、NFO 保存和远程访问仍存在会影响真实用户数据或系统可用性的 P1 风险。

未发现已证实的 P0 远程代码执行。确认 4 项 P1：

1. 删改刷新在目录离线、无权限或遍历不完整时可能误删数据库记录及关联的收藏、观看历史。
2. 覆盖刷新在扫描成功前先永久清空媒体和系列记录，失败后没有回滚。
3. NFO 编辑直接覆盖原文件，且会丢弃未建模 XML 字段和部分演员子字段。
4. 开启远程服务后，未认证请求也会无限追加日志，可被局域网攻击者用于磁盘耗尽。

### 最大优点

- 前端媒体卡片已做真正的行级虚拟化，不是简单渲染整库 DOM。
- 媒体删除关联清理已经集中到 Repository，并使用事务处理。
- FFprobe/FFmpeg 使用独立参数而不是 shell 拼接，并配置了 30 秒/2 分钟超时。
- Everything 结果有根目录边界校验，失败后可回退文件遍历。
- Wails 事件监听、主要异步列表请求、推荐请求大多有解绑或竞态令牌。
- 设置文件已经采用临时文件替换方式写入。
- 缩略图任务通过数据库状态、锁超时、重试时间和固定批次进行调度，不是无界 goroutine 队列。

### 最大风险

风险不在“代码不够优雅”，而在几个状态转换缺少失败原子性：

```text
目录不可访问
  → 遍历结果不完整
  → 未见到旧路径
  → 任意 Stat 错误被视为删除
  → 删除媒体及观看历史/收藏
```

```text
用户选择覆盖刷新
  → 先删媒体、系列和缓存
  → 后台扫描
  → 扫描失败
  → 原索引和用户状态无法恢复
```

### 最值得投入的三个方向

1. 先把扫描改成“确认根目录完整可访问后才允许删除”，并提供取消和失败明细。
2. 建立文件身份、数据库唯一约束、迁移版本和备份恢复机制。
3. 将整库一次性加载改为服务端分页/搜索，控制详情预取和图片缓存并发。

当前不适合继续叠加大型功能。应先完成 P1、数据库生命周期和大型库性能基线，再进入插件、实时监控或更完整 Jellyfin 兼容。

---

## 项目地图

### 1. 启动流程

`main()` 在 [main.go:32](/C:/Users/Philo/Desktop/alex-desktop/main.go:32) 中创建 `App`，配置嵌入式前端、`/local/` 文件处理器、窗口参数和 Wails 生命周期，然后绑定整个 `App`。

启动链：

```text
main
→ NewApp
→ wails.Run
→ App.startup
→ 打开工作目录下 navi.db
→ 设置 SQLite PRAGMA
→ model.AutoMigrate
→ NewRepositories
→ NewScannerService
   → 立即启动 1～3 个元数据 worker
→ 创建 ArtworkCache / GfriendsAvatarService / ThumbnailWorker
→ 500ms 后：
   → 缩略图历史状态迁移
   → 启动 ThumbnailWorker
   → 同步 Windows 启动项/托盘
   → 按 settings.json 启动 Jellyfin sidecar
```

退出链目前只有：

```text
App.shutdown
→ shutdownDesktopIntegration
→ shutdownRemoteServices
```

没有停止扫描元数据 worker、缩略图 worker、活动扫描或关闭数据库连接。

### 2. Go 后端模块关系

- `main/app.go`：Wails facade，同时直接承担数据库初始化、设置、扫描编排、查询、播放、NFO、缓存和 Windows 命令调用。
- `remote_access.go`：同进程 Jellyfin 兼容 HTTP sidecar、认证、媒体流和观看进度。
- `config/`：当前实际只是 FFmpeg/FFprobe 路径和硬编码缓存目录的最小 shim；`database.yaml`、`logging.yaml`、`cache.yaml` 等没有进入当前加载流程。
- `model/`：核心媒体模型和大量 V3/V4/未来功能模型。
- `repository/`：GORM 查询、媒体删除事务、推荐缓存和大量尚未接入 UI 的 Repository。
- `service/`：扫描、NFO、媒体元数据、缩略图、图片缓存、演员头像、推荐和事件桥接。
- `desktop_integration_*`、`exec_*`：Windows 托盘、注册表启动项和隐藏子进程窗口的平台隔离。

依赖方向为 `main → service/repository/model/config`、`service → repository/model/config`、`repository → model`，未见 Go 包循环依赖。

### 3. Wails 前后端边界

Wails 当前导出 31 个 `App` 方法，见生成声明 [App.d.ts:8](/C:/Users/Philo/Desktop/alex-desktop/frontend/wailsjs/go/main/App.d.ts:8)。

边界包括：

- 库管理：创建、更新、删除、扫描、选择目录。
- 列表与聚合：媒体、演员、分类、系列。
- 详情：文件、预览图、推荐。
- 修改操作：收藏、已看、删除数据库条目、NFO 保存。
- 系统操作：播放、打开目录、打开 NFO、选择播放器、重启。
- 设置：完整读取和覆盖 `DesktopSettings`，包括远程密码。

边界较宽，前端 renderer 一旦被不可信内容控制，就同时拥有文件读取、文件启动、NFO 写入和明文设置访问能力。当前没有发现可利用的前端 XSS，因此相关攻击链标为“待验证”。

### 4. 主要调用链

**扫描和入库**

```text
App.tsx
→ ScanLibraryWithMode
→ 单媒体库扫描锁
→ 可选：覆盖模式先删库内媒体、系列和缓存
→ goroutine
→ ScannerService.ScanLibraryWithOptions
→ movie / tvshow / mixed
→ Everything HTTP，失败则 filepath.Walk
→ 读取 sidecar、NFO、文件时间
→ 每 100 条批量快速入库
→ metadata queue
→ 1～3 个 worker 调用 FFprobe/NFO/字幕
→ 更新 Media
→ 缩略图状态进入 DB 队列
→ scan/media metadata Wails 事件
```

**播放**

```text
MediaCard / MediaDetail
→ PlayFile(filePath)
→ 读取 settings.json
→ exec.Command(播放器, 文件) 或 rundll32
→ Start + Process.Release
→ 立即写入已看状态
→ 追加 play_latency.log
```

**NFO**

```text
MediaDetail
→ GetNFOEditorData
→ resolveMediaNFOPath
→ LoadEditorData/xml.Unmarshal

保存：
SaveNFOEditorData
→ 使用前端返回的 nfo_path
→ SaveEditorData
→ os.WriteFile 覆盖 NFO
→ ParseMovieNFO
→ 更新 media 字段
→ 重建演员关系
→ 失效推荐缓存
```

**缩略图**

```text
快速扫描
→ resolveThumbnailState
→ media.thumbnail_status=pending/stale
→ ThumbnailWorker 每 5 秒取最多 10 条
→ DB 条件锁
→ FFmpeg 生成 poster/fanart/previews
→ 状态、重试时间和错误写回 DB
→ media:metadata-updated
```

**远程访问**

```text
UpdateDesktopSettings / startup
→ syncRemoteServices
→ net.Listen
→ Jellyfin mux
→ AuthenticateByName
→ 内存 token session
→ Items/Image/Video/PlaybackInfo
→ GORM/本地文件 ServeContent
→ Playing/Progress/Stopped 写 WatchHistory
```

### 5. 数据模型和数据库关系

核心关系：

- `Library 1:N Media`
- `Library 1:N Series`
- `Series 1:N Media`
- `Media N:M Person`，中间表 `MediaPerson`
- `User + Media → WatchHistory/Favorite`
- `Media → Thumbnail task state`
- 推荐缓存使用 `AICacheEntry` 和 `SystemSetting`

模型上声明了关联，但运行配置没有启用 `PRAGMA foreign_keys=ON`。当前删除安全主要依赖 [repo_media.go:49](/C:/Users/Philo/Desktop/alex-desktop/repository/repo_media.go:49) 的显式事务清理。

`AutoMigrate` 每次启动迁移核心表以及 AI、家庭社交、直播、云同步、标签、分享等大量模型，见 [model.go:738](/C:/Users/Philo/Desktop/alex-desktop/model/model.go:738)。这些旧/未来 Repository 会被构造，但除推荐缓存等少数路径外，大部分没有当前 UI/Wails 调用方。

### 6. 前端页面和状态流

`App.tsx` 约 780 行，维护：

- 当前库、视图、搜索、筛选、排序、详情。
- 扫描状态和 Wails 事件。
- 列表变更、滚动位置和 localStorage 库缓存。
- `Sidebar`、`TopBar`、`MediaGrid`、`CategoryGrid`、`MediaDetail`、`SettingsPage`。

`MediaGrid` 约 712 行：

- 后端一次性取整库。
- 为所有媒体建立本地文字/拼音搜索索引。
- 使用 request token 防止旧请求覆盖。
- 只渲染可视行和 2 行 overscan。
- 缓存最多 10 个完整列表到 localStorage。

`MediaDetail` 约 1066 行：

- 并行加载详情 bundle 和推荐。
- 内存 LRU 最多 24 条。
- 监听元数据更新事件。
- 管理播放、收藏、已看、删除、NFO、预览图和推荐跳转。

### 7. 外部依赖与外部进程

- Go：Wails、GORM、glebarez SQLite、Zap、UUID、`x/sys/windows`。
- 前端：React 18、Vite 3、TypeScript 4.6、Lucide、pinyin-pro。
- 进程：FFprobe、FFmpeg、外部播放器、`rundll32`、`explorer`、应用重启进程。
- 网络：Everything HTTP、Gfriends GitHub/CDN、Jellyfin 局域网 HTTP。
- 本地存储：`navi.db`、`settings.json`、`cache/`、`remote_access.log`、`play_latency.log`。

### 8. 当前测试覆盖

共有 39 个 Go 测试：

- Repository：媒体删除关联、孤儿清理、带引号搜索。
- Scanner：仅覆盖增量扫描的 sidecar/video fingerprint 决策。
- NFO：裸 `&`、演员解析、合集 round-trip。
- Thumbnail/artwork：状态、缓存尺寸、移除。
- Recommendation：分类、路由填充。
- Gfriends：别名、并发索引下载、大小和类型限制。
- Root：Jellyfin happy path、播放历史去重、扫描锁、设置原子写入、重启失败。
- Windows 桌面：启动命令、临时路径、托盘生命周期。

没有前端测试，也没有 ESLint 脚本、组件测试、Wails 契约测试或真实 FFmpeg/Everything/网络盘集成测试。

---

## B. 已验证结果

| 命令 | 结果 | 说明 |
|---|---|---|
| `git status --short` | 成功 | 初始已有未跟踪 `.codegraph/`、`.gocache/`、`alex.db`、`navi.db`、`remote_access.log` |
| `go test ./...` | 部分失败 | 默认缓存先因沙箱访问 `%LocalAppData%\go-build` 失败；改用仓库已有 `.gocache` 后，仅 Windows 托盘生命周期测试失败 |
| `go test` 排除托盘测试 | 成功 | root 9 个测试通过，覆盖率 18.5% |
| `go test -cover ./repository ./service` | 成功 | Repository 6.9%，Service 22.9% |
| `go vet ./...` | 成功 | 无输出 |
| `npm ci` | 成功 | 安装锁定的 74 个包；有 `sourcemap-codec` 弃用警告 |
| `npm run build` | 成功 | 1762 个模块；TS 和 Vite 构建成功 |
| `wails build` | 成功 | 生成 `build/bin/Navi.exe` |
| `git diff --name-only` | 成功 | 没有已跟踪文件变化 |

失败的托盘测试错误为：

```text
TestNewTrayIconLifecycle
newTrayIcon() error = add tray icon: Unspecified error
```

这更符合当前非交互桌面/沙箱环境限制，不足以证明生产托盘代码有缺陷。其余 root 测试已单独通过。

构建警告：

- Vite：第三方模块的 `"use client"` 指令被忽略。
- Wails bindings：多次输出 `Not found: time.Time`，但生成和生产构建成功；当前生成类型把时间保留为 `any`。

SQLite 只读诊断显示：

- `journal_mode=wal`
- `foreign_keys=0`
- `navi.db` 当前媒体数为 0。
- `alex.db` 有 4 条媒体记录，但源码硬编码启动数据库为 `navi.db`，不能确认 `alex.db` 是当前活动运行库。
- `alex.db` 的 4 条路径均使用 Windows 反斜杠。

需要透明说明一个环境副作用：`sqlite3 -readonly` 打开 WAL 数据库时创建了 `alex.db-shm`、`navi.db-shm`（各 32 KiB）及两个 0 字节 `-wal` 文件。它们不是源码或数据库内容修改，但属于未跟踪运行时 sidecar。因为你明确要求不删除任何文件，我没有清理它们。

---

## C. 问题清单

| ID | 严重度 | 类型 | 文件/函数 | 证据 | 影响 | 触发条件 | 建议 | 工作量 |
|---|---|---|---|---|---|---|---|---|
| NAVI-01 | P1 | 数据丢失 | `listMovieEntriesWithWalk`、`syncDeletedMovieRecords` | 遍历错误被吞掉：[scanner.go:4387](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:4387)；随后任何 `os.Stat` 失败都会进入删除：[scanner.go:2129](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:2129) | 媒体、收藏、观看历史等被事务永久删除 | 电影库执行“删改刷新”，根目录离线、网络盘断开、访问拒绝或部分遍历失败 | 跟踪每个根目录是否完整遍历；只有完整且可访问时才同步删除；只把明确 `IsNotExist` 视为缺失 | 中 |
| NAVI-02 | P1 | 数据丢失 | `ScanLibraryWithMode` 覆盖模式 | [app.go:2228](/C:/Users/Philo/Desktop/alex-desktop/app.go:2228) 在启动异步扫描前删除媒体、系列和缓存 | 扫描失败后库为空，ID、收藏、历史不可恢复 | 覆盖刷新时目录无权限、离线、数据库中途失败 | 使用 staging scan 或先验证全部根目录；成功后再事务切换；最低限度先做 DB 备份 | 中至大 |
| NAVI-03 | P1 | NFO 数据损坏 | `SaveEditorData` | 只建模部分 XML：[nfo.go:55](/C:/Users/Philo/Desktop/alex-desktop/service/nfo.go:55)；演员按名字重建：[nfo.go:261](/C:/Users/Philo/Desktop/alex-desktop/service/nfo.go:261)；直接覆盖：[nfo.go:520](/C:/Users/Philo/Desktop/alex-desktop/service/nfo.go:520) | 未知顶层标签、演员 role/thumb 等丢失；写入中断可留下截断文件 | 用户在内置编辑器保存现有 NFO | 保留未知 XML；同目录临时文件、Flush/Close、可选 `.bak`、原子替换；补编码和故障注入测试 | 中 |
| NAVI-04 | P1 | 远程可用性/安全 | Jellyfin 请求日志 | Ping 等接口未认证：[remote_access.go:207](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:207)；每个请求追加文件且无轮转：[remote_access.go:298](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:298) | 局域网攻击者可持续请求，令日志无限增长直至磁盘耗尽 | 开启 Jellyfin，默认绑定 `0.0.0.0` | 日志轮转和总量上限；公共端点速率限制；不要逐请求同步打开文件 | 小至中 |
| NAVI-05 | P2 | 正确性 | TV/mixed 删除同步 | `ScanLibraryWithOptions` 对 TV/mixed 调旧扫描器：[scanner.go:1091](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:1091)；删除同步只在电影扫描：[scanner.go:2451](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:2451) | 已删除剧集继续显示为幽灵条目 | TV 或 mixed 库选择“删改刷新” | 统一枚举结果和按根目录删除同步；为 TV 删除单集/整季写回归测试 | 中 |
| NAVI-06 | P2 | 数据库完整性 | `DeleteLibrary` | [app.go:2207](/C:/Users/Philo/Desktop/alex-desktop/app.go:2207) 对媒体删除错误只记日志后继续删 Library，且没有删除 Series；Library 删除本身仅软删除：[repo_library.go:34](/C:/Users/Philo/Desktop/alex-desktop/repository/repo_library.go:34) | 媒体或系列成为不可见孤儿；部分失败无法恢复 | SQLite 锁冲突、关联清理失败或任何删除错误 | 把媒体、系列、库删除放入同一事务；任何一步失败均返回错误并回滚 | 中 |
| NAVI-07 | P2 | 文件身份/重复 | `Media.FilePath`、路径查找 | `file_path` 无唯一约束：[model.go:218](/C:/Users/Philo/Desktop/alex-desktop/model/model.go:218)；路径仅 `filepath.Clean`：[scanner.go:925](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:925)；查找不带 LibraryID：[repo_media.go:225](/C:/Users/Philo/Desktop/alex-desktop/repository/repo_media.go:225) | Windows 大小写变化可重复入库；重叠媒体库可能更新错误记录；移动/重命名丢失原 ID 和状态 | Everything/网络盘返回不同大小写；多个库包含同一路径；文件移动 | 设计规范化路径键和 `(library_id,path_key)` 唯一约束；移动匹配使用文件 ID/指纹并保留 Media ID | 中至大 |
| NAVI-08 | P2 | 状态一致性 | Favorite/WatchHistory toggle | 状态表没有 `(user_id,media_id)` 唯一约束：[model.go:314](/C:/Users/Philo/Desktop/alex-desktop/model/model.go:314)；Toggle 把所有查询错误都当作不存在：[app.go:1604](/C:/Users/Philo/Desktop/alex-desktop/app.go:1604) | 并发操作可产生重复行；数据库锁错误可能被后续 Create 覆盖，列表 join 可能重复 | 远程进度与桌面操作并发、SQLite 锁冲突 | 加组合唯一索引并使用 upsert；只在 `ErrRecordNotFound` 时创建 | 小至中 |
| NAVI-09 | P2 | 生命周期 | scanner/thumbnail/shutdown | 元数据 worker 无限循环：[scanner.go:1252](/C:/Users/Philo/Desktop/alex-desktop/service/scanner.go:1252)；Thumbnail 有 `Stop`：[thumbnail_worker.go:61](/C:/Users/Philo/Desktop/alex-desktop/service/thumbnail_worker.go:61) 但 shutdown 未调用：[remote_access.go:88](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:88) | 退出或重启时 FFprobe、缓存写入、数据库更新被强制终止；无法取消扫描 | 扫描中退出、托盘退出、`RestartApp` | App 级 context、WaitGroup、scanner Stop、thumbnail Stop、等待活动事务和子进程 | 中 |
| NAVI-10 | P2 | 大型库性能 | `MediaGrid`、`GetMediaList` | 前端显式请求 `size=0` 整库：[MediaGrid.tsx:507](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/components/MediaGrid.tsx:507)，并保存最多 10 份列表：[persistentCache.ts:255](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/utils/persistentCache.ts:255) | DOM 虚拟化仍无法避免整库查询、JSON 传输、搜索索引和 localStorage 序列化 | 数千至数万媒体 | 后端分页/服务端搜索；窗口化数据加载；缓存 ID 和轻量摘要而非 10 份整库 | 大 |
| NAVI-11 | P2 | 启动/列表卡顿 | `GetMediaList` backfill | 每次按加入时间显示时调用两个 backfill：[app.go:714](/C:/Users/Philo/Desktop/alex-desktop/app.go:714)；每条缺失记录 `Stat + UPDATE`：[app.go:769](/C:/Users/Philo/Desktop/alex-desktop/app.go:769) | 首次打开列表可能产生 O(N) 文件 IO 和 O(N) SQLite 写入；永远无法修复的路径会反复扫描 | 旧库升级、网络盘、缺失文件 | 独立迁移任务分批执行并记录完成版本；失败项设置重试时间 | 中 |
| NAVI-12 | P2 | 缓存性能/磁盘 | `ArtworkCache` | 每次读取预览都扫描整个目录：[artwork_cache.go:179](/C:/Users/Philo/Desktop/alex-desktop/service/artwork_cache.go:179)；每媒体删除扫描 poster/fanart/preview 三个目录：[artwork_cache.go:201](/C:/Users/Philo/Desktop/alex-desktop/service/artwork_cache.go:201) | 删除 N 个媒体时接近 O(N×缓存文件数)；旧 source-key 文件无总量上限 | 大库覆盖刷新、删除库、海报多次更新 | 每媒体子目录或缓存索引；批量删除；容量、年龄和孤儿清理策略 | 中 |
| NAVI-13 | P2 | Jellyfin 性能 | `queryJellyfinItems`/DTO | 先加载全部再筛选分页：[remote_access.go:923](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:923)；系列逐条加载剧集：[remote_access.go:1117](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:1117)；DTO 又逐媒体查用户状态和系列：[remote_access.go:1225](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:1225) | `/Items` 在大库上产生全量内存和 N+1 查询 | Infuse/Jellyfin 客户端递归浏览或筛选 | 把筛选、排序、分页放入 SQL；批量加载用户状态、系列、图片 tag | 中至大 |
| NAVI-14 | P2 | 远程认证 | auth/session/server | 默认监听全部接口：[remote_access.go:32](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:32)；HTTP 明文认证且无速率限制：[remote_access.go:569](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:569)；接受 query token：[remote_access.go:1665](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:1665)，日志记录完整 URI；body 无上限：[remote_access.go:1860](/C:/Users/Philo/Desktop/alex-desktop/remote_access.go:1860) | 密码可被暴力尝试；明文凭据/token 可被同网段观察；`api_key` 可能写日志；大 body/慢请求耗资源 | 开启远程访问，尤其公共 Wi-Fi/访客网络 | 默认 `127.0.0.1`；显式确认 LAN 暴露；限速、body 限制、Read/Write/IdleTimeout、token 过期和日志脱敏；TLS 反代说明 | 中 |
| NAVI-15 | P2 | 前端竞态/资源 | 详情预取和事件刷新 | 每次卡片悬停预取：[MediaCard.tsx:52](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/components/MediaCard.tsx:52)；后端详情又启动无界缓存 goroutine：[app.go:1219](/C:/Users/Philo/Desktop/alex-desktop/app.go:1219)；事件触发的 `refreshDetailAndPreviews` 无 active/token 检查：[MediaDetail.tsx:540](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/components/MediaDetail.tsx:540) | 快速扫过卡片可集中触发 DB/目录/图片解码；切换详情时旧请求可能覆盖新媒体状态 | 鼠标快速移动、元数据更新与详情切换并发 | 前端预取并发池和延迟；后端 singleflight/worker；所有详情刷新使用 media ID token | 中 |
| NAVI-16 | P2 | 数据库升级/并发，待验证 | startup/AutoMigrate | 每次启动直接 AutoMigrate：[app.go:89](/C:/Users/Philo/Desktop/alex-desktop/app.go:89)；没有迁移版本、前置备份或恢复；PRAGMA 仅在一个 GORM 执行路径设置，未限制连接池：[app.go:282](/C:/Users/Philo/Desktop/alex-desktop/app.go:282) | 迁移失败可能留下半升级库；多连接上的 `busy_timeout` 是否一致待验证；并发 worker 可能出现锁错误 | 升级大库、远程写入+扫描+缩略图并发 | 版本化迁移和升级前备份；获取 `sql.DB` 配置连接数；并发锁冲突压力测试 | 中 |
| NAVI-17 | P2 | 播放状态/隐私 | `PlayFile` | 子进程只要启动成功就立即标为已看：[app.go:2141](/C:/Users/Philo/Desktop/alex-desktop/app.go:2141)；完整文件和播放器路径永久追加日志：[app.go:2174](/C:/Users/Philo/Desktop/alex-desktop/app.go:2174) | 未实际观看也进入“已看”；日志泄露本地目录/用户名且无轮转 | 播放器启动后报错、立即退出、误点 | 外部播放器无法回传时改为“最近播放”而非“已看”；已看由用户或进度阈值决定；日志轮转和路径脱敏 | 小至中 |
| NAVI-18 | P3 | 错误体验/类型/可访问性 | App/Grid/Card/Settings | 列表错误只写 console 后显示“没有内容”：[MediaGrid.tsx:523](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/components/MediaGrid.tsx:523)；大量 `any`；卡片外层为不可聚焦 `div`：[MediaCard.tsx:48](/C:/Users/Philo/Desktop/alex-desktop/frontend/src/components/MediaCard.tsx:48) | 用户无法区分空库和数据库错误；契约漂移编译期难发现；键盘用户不能打开卡片 | DB/API 失败、仅键盘操作 | 明确 error state；复用生成类型；卡片使用 button/role/tabIndex；Modal 增加 dialog/focus trap | 中 |
| NAVI-19 | P3 | 维护风险 | app/config/V3 | `app.go` 同时做 facade、SQL、文件、进程、设置和缓存；`NewRepositories` 构造大量未使用仓储：[repository.go:60](/C:/Users/Philo/Desktop/alex-desktop/repository/repository.go:60)；配置加载实际只处理两个二进制路径：[config.go:27](/C:/Users/Philo/Desktop/alex-desktop/config/config.go:27) | 修改一个用户流程需跨大文件和隐式全局状态；大量表迁移但没有产品入口 | 继续增加扫描任务、插件或远程写接口 | 仅围绕真实风险拆分：扫描任务协调、设置/密钥、播放、远程服务；先确认 V3 调用后再决定兼容策略 | 中 |
| NAVI-20 | P2，待验证 | renderer 安全边界 | `/local/`、NFO path | `/local/` 解码任意路径后直接 `ServeFile`：[main.go:20](/C:/Users/Philo/Desktop/alex-desktop/main.go:20)；NFO 保存接受前端传回路径：[app.go:1782](/C:/Users/Philo/Desktop/alex-desktop/app.go:1782) | 若 renderer 出现 XSS 或可导航到不可信内容，可读任意本地文件并写任意可写路径 | 当前未发现前端 XSS，攻击前提尚未证实 | `/local/` 改为媒体 ID/受控根目录；保存时忽略前端路径，后端按 Media 重新解析 | 中 |

### 已确认的安全正向控制

- 外部播放器和 FFmpeg 均使用 `exec.Command(executable, args...)`，未发现 shell 字符串拼接，因此没有已证实的命令注入。
- Jellyfin 图片和视频路径来自通过 item ID 查出的数据库记录，且相应路由有 token 包装。
- Everything 结果会经过大小写不敏感的根目录边界检查。
- token 使用加密随机数生成，密码比较使用 constant-time compare。
- Gfriends 下载有 HTTP timeout、32 MiB/2 MiB 限制和图片内容检查。
- 未发现 `Access-Control-Allow-Origin: *`；当前不是宽松 CORS 风险。
- Go `encoding/xml` 不会自动解析外部实体，未发现可证实 XXE 路径。

### 性能热点及测量建议

| 热点 | 触发条件和调用链 | 复杂度/消耗 | 是否值得 | 推荐测量 | 推荐方案 |
|---|---|---|---|---|---|
| 整库前端加载 | MediaGrid → `GetMediaList(size=0)` → 全量 hydrate/search index/localStorage | O(N) DB、传输、JS 对象和索引；最多缓存约 10N 摘要 | 高 | 1k/10k/50k 媒体的 Wails 调用时间、heap、JSON 大小、首次可交互时间 | 服务端分页和搜索、窗口数据源 |
| 列表时间回填 | `GetMediaList` → 两个 backfill → `Stat + UPDATE` | O(N) 文件系统访问和最多 O(N) 写事务 | 高 | 在 UNC/离线文件上记录列表 P50/P95、SQLite 写次数 | 独立迁移任务、批量更新、失败重试状态 |
| 扫描内存 | existing signatures + entries + pendingList + scan-local sidecar map | O(N) 常驻扫描内存；全量排序 | 中高 | 50k/200k 文件 heap profile、GC pause | 流式枚举、有限批次、避免同时保存多份路径 |
| 持久 sidecar cache | Scanner 的 `sidecarCache` 只增不淘汰 | O(访问过的目录数)，进程生命周期持续增长 | 中 | 扫描多库后 map 大小和 heap profile | LRU/代际清理、扫描结束淘汰 |
| 图片缓存删除 | DeleteLibrary/overwrite → 每媒体 `RemoveMedia` → 3 次全目录 ReadDir | 近似 O(N×C) | 高 | 1k/10k 媒体、每媒体 5～10 缓存文件的删除耗时 | 每媒体子目录、批量前缀索引 |
| Jellyfin `/Items` | 全量加载 → 内存筛选 → series N+1 → media userData N+1 | O(N+S) 内存，查询可达 O(N+S) | 高 | GORM query logger、`httptest` 10k 项 P95、allocs | SQL 分页/筛选、批量状态和系列预载 |
| 悬停预取 | 卡片 pointer enter → detail bundle → 图片缓存 goroutine | 并发量取决于鼠标速度，不受 24 条完成缓存限制 | 中高 | 记录同时进行的 detail/cache 请求、磁盘读取和 goroutine 数 | 150～300ms hover 延迟、并发 2～4、singleflight |
| 随机播放 | `ORDER BY RANDOM()` | SQLite O(N) 全表随机排序 | 中低 | 10k/100k 行查询耗时 | 随机 offset、rowid/预生成候选 |

缩略图任务本身不是“无限队列”：元数据 channel 容量为 16384，worker 最多 3 个；缩略图每批 10 个、单 worker。但 channel 满后扫描 goroutine会阻塞，且目前无法取消，应纳入扫描任务状态。

### 测试质量与缺口

**必须补充的回归测试**

- 删改刷新遇到根目录不存在、权限拒绝、UNC 断线时不删除记录。
- 覆盖扫描枚举失败时保留原媒体、收藏和历史。
- TV/mixed 删除单集、整季和根目录。
- NFO 写入失败不破坏原文件；未知标签、演员 role/thumb、UTF-8/UTF-16/特殊字符保存后保留。
- 相同路径大小写变化、重叠库、重复文件。
- 移动/重命名后保留 Media ID、收藏和观看历史。
- SQLite 锁冲突下 DeleteLibrary、Toggle、扫描的事务结果。
- Jellyfin 未认证访问、错误密码、token 日志脱敏、body 上限。
- 扫描取消和退出时 worker/FFmpeg 停止。
- 前后端生成类型与 JSON 契约。

**值得补充的单元测试**

- FFmpeg/FFprobe 不存在、超时、损坏 JSON 和损坏视频。
- 外部播放器参数中包含空格、引号、`&`、Unicode 时仍作为单独参数。
- 推荐 limit、空库、全部已看、缓存损坏边界。
- 缩略图任务去重、重试、锁回收和停止。
- Windows 路径大小写、UNC、`\\?\` 长路径、junction 边界。
- 非法 NFO、裸实体、空字段清除、评分和 runtime 非法值。
- Favorite/WatchHistory 并发 upsert。

**成本较高但有价值的集成测试**

- 真实 FFmpeg 对短视频、损坏视频、字幕流的探测和取消。
- Everything stale index、分页、HTTP 故障与 Walk fallback。
- 网络盘断线/恢复和 Windows junction/symlink。
- 50k 项 SQLite + Wails + React 性能基线。
- Infuse/Jellyfin 客户端兼容和 Range 流媒体请求。
- Wails WebView 的 `/local/`、导航策略和 renderer 安全边界。

**低收益，不建议优先**

- 对每个未接入产品的 V3 Repository 做完整 CRUD 测试。
- 大量 CSS 像素快照。
- UUID `BeforeCreate` 等简单 getter/hook 的穷举测试。
- 在扫描安全和数据库事务尚未覆盖前追求高覆盖率数字本身。

---

## D. Top 10 优化项

| 排名 | 问题 | 用户影响 | 修改范围 | 风险 | 预计工作量 | 验证方式 |
|---|---|---|---|---|---|---|
| 1 | 删改刷新只在完整、可访问的根目录上执行删除 | 防止离线盘误删索引、历史和收藏 | scanner + repository tests | 中 | 2～4 人日 | 权限拒绝、目录不存在、部分遍历故障注入 |
| 2 | 覆盖刷新改为非破坏式 staging/事务切换 | 扫描失败不再清空库 | app、scanner、repository | 中高 | 4～8 人日 | 扫描任意阶段失败后比较全部旧数据 |
| 3 | NFO 原子保存并保留未知 XML | 防止用户 sidecar 元数据损坏 | nfo service、editor contract | 中 | 3～5 人日 | unknown tag、断盘/写失败、编码 round-trip |
| 4 | App 级扫描取消和 worker shutdown | 退出不截断任务，用户可停止错误扫描 | App、Scanner、ThumbnailWorker、前端进度 | 中 | 4～7 人日 | goroutine 泄漏、FFmpeg 取消、退出超时测试 |
| 5 | 数据库唯一约束、连接池和版本化迁移 | 减少重复、锁冲突和升级事故 | model、migration、repository | 高 | 5～10 人日 | 旧库升级副本、并发写、回滚和恢复 |
| 6 | MediaGrid 服务端分页/搜索 | 大型库显著降低内存和启动等待 | GetMediaList、MediaGrid、cache | 中高 | 6～12 人日 | 1k/10k/50k 性能基线和搜索一致性 |
| 7 | 远程服务限速、body/timeout、日志轮转和 token 脱敏 | 减少暴力破解、磁盘 DoS 和凭据泄露 | remote_access、settings、tests | 中 | 3～6 人日 | 高频未认证请求、超大 body、query token |
| 8 | 图片缓存按媒体分区并限制容量 | 删除库和详情预览不再随缓存总量恶化 | ArtworkCache、清理任务 | 中 | 3～6 人日 | 10k 媒体缓存删除和孤儿清理 benchmark |
| 9 | Jellyfin 查询 SQL 化和批量 hydration | Infuse 大库浏览不再 N+1 | remote_access、repository | 中 | 5～8 人日 | query count、P95、返回契约对比 |
| 10 | 建立扫描/数据库/前端契约测试矩阵 | 防止上述风险回归 | Go tests、前端 test runner、CI | 低 | 4～8 人日首期 | 回归场景全部自动执行，记录覆盖与性能阈值 |

---

## E. 功能候选池

以下完全基于当前代码基础和“轻量、本地优先”定位，没有照搬外部产品功能。

### 近期高价值

| 功能 | 解决的用户问题 | 当前基础 | 修改模块 | DB 变更 | 难度 | 价值 | 维护成本/架构冲突 | 版本 |
|---|---|---|---|---|---|---|---|---|
| 扫描失败文件列表与重试 | 用户不知道哪些文件失败、为何失败 | metadata phase、thumbnail error、事件 | scanner、App、前端进度页 | 新增 scan_job/scan_error 较合适 | 中 | 高 | 低；与任务化扫描一致 | 下个补丁/次版本 |
| 扫描取消 | 选错目录或网络盘异常时能立即停止 | 已有 app context、FFmpeg timeout | scanner、worker、App、前端 | 可先不改 DB | 中 | 高 | 低 | 下个补丁 |
| 扫描暂停/恢复 | 大库扫描跨时段进行 | 有快速入库和 DB 队列 | scanner、任务仓储、UI | 需要 checkpoint/job | 高 | 中高 | 会改变扫描状态机 | 后续次版本 |
| 媒体库健康检查 | 找出离线文件、损坏视频、孤儿状态、缩略图失败 | fingerprint、thumbnail status、metadata phase | service、repository、前端 | 可新增 health result/cache | 中 | 高 | 低 | 下个次版本 |
| 缺失 NFO/海报/字幕筛选 | 用户快速补齐 sidecar | 相关字段和 sidecar 扫描已存在 | GetMediaList、筛选 UI | 不需要 | 低 | 高 | 很低 | 下个补丁 |
| 数据库自动备份与恢复 | 防止升级、覆盖扫描或磁盘故障造成不可逆损失 | SQLite 单文件、设置原子写已有范例 | startup、settings、backup service | 不需要表；需要 backup metadata 可选 | 中 | 高 | 低 | 下个补丁 |
| 设置/库配置导入导出 | 换机器或重装时恢复配置 | settings.json、Library 模型 | App、settings UI | 可选导出 DB 子集 | 中 | 高 | 低 | 下个次版本 |
| 大型媒体库性能模式 | 减少整库加载和 UI 卡顿 | 列表参数已有 page/size，前端已有虚拟化 | App、repository、MediaGrid、search | 增加复合索引 | 中高 | 高 | 中；需调整缓存契约 | 下个次版本 |

### 中期增强

| 功能 | 解决的用户问题 | 当前基础 | 修改模块 | DB 变更 | 难度 | 价值 | 维护成本/架构冲突 | 版本 |
|---|---|---|---|---|---|---|---|---|
| 文件移动/改名智能匹配 | 保留收藏、已看、NFO 和 Media ID | size+mtime fingerprint、VersionGroup | scanner、repository | 增加 path_key、file identity/hash | 中高 | 高 | 中；必须先定义误匹配规则 | 次版本 |
| 重复视频检测 | 发现多份相同内容和浪费空间 | FileSize、指纹字段 | scanner、health service、UI | 增加快速 hash/完整 hash | 中 | 高 | 中；哈希需限速 | 次版本 |
| 文件夹实时监控和增量更新 | 不必手动刷新 | `EnableFileWatch` 字段已存在但未使用 | 新 watcher service、scan queue | 可复用 scan_job | 中高 | 高 | Windows rename/debounce/网络盘维护成本中高 | 次版本 |
| 批量编辑 NFO | 修正系列、演员、标签时减少重复操作 | NFO editor/service | NFO service、批量预览 UI | 不需要 | 中 | 高 | 必须提供 dry-run/备份 | 次版本 |
| 批量重命名和整理 | 统一文件结构 | 路径和 NFO 已可解析 | 新 plan service、scanner、UI | 需要 operation journal | 高 | 中高 | 文件破坏风险高，必须可回滚 | 后续次版本 |
| 自定义合集与播放列表 | 用户按主题组织内容 | Playlist/PlaylistItem 模型已存在 | App facade、repository、前端 | 现有表基本够用 | 中 | 中高 | 低；符合本地定位 | 次版本 |
| 字幕管理 | 看见语言、缺失、转换和外置字幕 | subtitle paths、probe、extract/convert 已有 | scanner、subtitle service、详情 UI | 建议规范化 subtitle 表 | 中高 | 中高 | FFmpeg/编码维护成本中 | 次版本 |
| 日志诊断包导出 | 用户反馈扫描/FFmpeg问题时提供证据 | 已有多处日志 | logging、settings、导出 UI | 不需要 | 低 | 高 | 需默认脱敏路径、密码和 token | 下个次版本 |
| 媒体统计与空间分析 | 找出最大目录、重复内容、缓存占用 | FileSize、目录聚合、SumFileSize | repository、stats service、前端 | 可不改 | 低中 | 中高 | 很低 | 次版本 |

### 长期探索

| 功能 | 解决的用户问题 | 当前基础 | 修改模块 | DB 变更 | 难度 | 价值 | 维护成本/架构冲突 | 版本 |
|---|---|---|---|---|---|---|---|---|
| 多版本媒体 | 将 1080p/4K/导演剪辑版聚合展示 | StackGroup/VersionGroup/VersionTag 已存在 | scanner、detail、player selection | 现有字段需唯一/关联约束 | 中高 | 中高 | 中 | 大版本 |
| 观看进度多设备同步 | 桌面与 Infuse 使用统一进度 | Jellyfin progress 已写桌面用户 | remote、user model、同步策略 | 需要设备/用户和冲突版本 | 高 | 高 | 与当前单用户假设冲突 | 大版本 |
| 外部播放器进度回传 | 不再一启动就算已看 | 当前只有进程启动 | player adapter、IPC/文件监控 | 进度表已有基础 | 高 | 高 | 强依赖具体播放器 | 大版本、先支持少数播放器 |
| 元数据提供方/插件机制 | 用户按内容类型选择刮削来源 | config、V3 task/cache 有部分基础 | 新 provider interface、任务、UI | provider/job/cache 迁移 | 高 | 中高 | 插件安全和版本兼容成本高 | 大版本 |
| Jellyfin 兼容度提升 | 更多客户端可稳定使用 | 已有 sidecar 和 DTO | remote、契约测试 | 可能需要用户/会话表 | 高 | 中高 | 容易偏离轻量定位；限定 direct-play | 大版本 |
| LAN 安全增强 | 家庭网络之外更安全 | token auth 已有 | remote、settings、证书管理 | session/token 表可选 | 中高 | 高 | TLS 证书 UX 有成本 | 次版本至大版本 |
| 自动更新与 Release 安装器 | 普通用户可靠升级 | 当前仅裸 exe/restart | 构建、签名、更新服务、UI | 版本状态可选 | 高 | 高 | 代码签名和供应链维护成本高 | 大版本 |

### 不建议近期增加

| 功能 | 解决的用户问题 | 当前基础 | 修改模块 | DB 变更 | 难度 | 价值 | 维护成本/架构冲突 | 版本 |
|---|---|---|---|---|---|---|---|---|
| 完整实时转码服务器 | 异网播放兼容 | 只有少量 TranscodeTask 模型 | HTTP、转码调度、带宽、缓存 | 大量 | 高 | 中 | 与轻量、本地优先冲突大 | 暂不建议 |
| 直播/录制 | IPTV 管理 | 有未接入 Live* 模型 | 几乎全栈新建 | 已有表也需重做 | 高 | 低中 | 高维护、偏离核心 | 不建议 |
| 家庭社交/评论/点赞 | 多用户互动 | 有 V3 表但无产品流 | 用户、权限、远程、UI | 大量安全迁移 | 高 | 低 | 与单机媒体库定位冲突 | 不建议 |
| 通用云同步 | 跨设备同步全部数据 | 有 dormant Sync* 模型 | 身份、冲突、加密、服务端 | 大量 | 高 | 中 | 运维和隐私成本极高 | 不建议 |
| 全自动重命名+在线刮削默认开启 | 降低整理成本 | 有 NFO/匹配规则雏形 | 文件写入、provider、任务 | 中 | 中 | 错误会直接影响用户原文件 | 不建议默认开启 |

---

## F. 建议路线图

### 下一个补丁版本：稳定性和数据安全

- 修复 NAVI-01：不完整扫描禁止删除。
- 覆盖刷新增加根目录预检和失败保护；至少升级前/覆盖前自动备份 DB。
- NFO 临时文件原子保存、备份和未知字段保留。
- DeleteLibrary 事务化并清理 Series。
- Favorite/WatchHistory 增加组合唯一约束。
- 远程日志轮转、token 脱敏、body/timeout 和登录限速。
- 补齐对应回归测试。
- 对用户暴露失败文件列表和明确错误状态。

发布门槛：所有 P1 有自动回归测试；网络盘不可访问时 DB 行数、收藏和历史保持不变。

### 下一个次版本：性能和用户体验

- `GetMediaList` 服务端分页、排序和搜索。
- 独立执行 file-time migration，不阻塞列表。
- 图片缓存分区、容量管理和批量删除。
- 详情预取并发池。
- Jellyfin 批量查询，消除主要 N+1。
- 扫描取消、任务历史、失败重试。
- 缺失 NFO/海报/字幕筛选。
- 数据库/设置导入导出、诊断包和空间统计。
- 引入前端测试框架和 Wails JSON 契约测试。

发布门槛：10k 媒体首次列表、搜索、删除库和 Jellyfin `/Items` 有明确 P95/内存目标。

### 后续大版本：任务化架构和大型功能

- 版本化迁移、自动备份、恢复向导。
- 统一文件身份，移动/改名保留媒体 ID。
- 实时 watcher 与任务队列共享同一增量同步逻辑。
- 多版本媒体、播放列表、字幕规范化。
- 多设备观看进度。
- 受控元数据 provider/plugin 接口。
- 更完整但仍以 direct-play 为主的 Jellyfin sidecar。
- 签名安装器和自动更新。

不建议为这些功能创建新的平行架构；应复用当前 scanner、repository、NFO、thumbnail 状态和现有 Wails facade。

---

## G. 优先实施建议

第一项只选择：

**让“删改刷新”在目录遍历不完整、离线或无权限时绝不删除已有记录，并补齐故障注入回归测试。**

选择依据：

- 这是当前最容易被普通用户触发的 P1 数据丢失路径。
- 网络盘、移动硬盘和 Windows 权限失败都属于媒体库真实场景。
- 当前调用链和根因已明确，不依赖大规模架构重做。
- 修复后能为覆盖刷新、实时监控、暂停恢复、移动匹配建立正确的“扫描完整性”基础。
- 验证标准客观：无论枚举在哪一步失败，媒体、收藏、观看历史和系列数量必须保持不变；只有一次完整成功的扫描才能提交删除。

历史记录仅用于提醒我复核此前的数据关联和 Everything 运行态问题；以上问题、构建结果和行号均以本轮当前源码与命令为准。
