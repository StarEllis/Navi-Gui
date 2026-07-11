# Navi-Gui 实施后终验审查

审查日期：2026-07-11
原始审查基准：`3ed76717d84d4bd4effd0696738fe574b6598fce`
当前 HEAD：`5fc3180278fcd45562d1bb1e363beab08d75cde0`
当前分支：`codex/artwork-cache-gfriends`
终验结论：**NOT READY**

本报告是独立、对抗式、零源码修改终验。结论不采信 backlog 的“已修复”描述，也不把已有测试通过本身视为闭环证据；每项均重新核对当前实现、调用路径、失败路径和测试触发条件。

## 1. 当前提交与工作区状态

- 审查开始时 `git status --short` 无输出，工作区干净。
- 分支上游为 `origin/codex/artwork-cache-gfriends`，相对上游 ahead/behind 为 `0/0`，不存在未推送提交。
- `main` 为 `25ec6f51385e820d0759adec3e4e27e23a5f57d6`，同时也是 `main...HEAD` 的 merge-base。
- 当前分支比 `main` 多 6 个提交；比基准 `3ed7671` 多 4 个提交：
  - `efcc6b8 Harden library deletion, NFO saves, and overwrite scans`
  - `94fd877 Add database lifecycle and scan task controls`
  - `650a024 Optimize Jellyfin queries and harden database migrations`
  - `5fc3180 Improve media performance and artwork processing`
- `npm ci`、前端构建和 Wails 构建生成的依赖目录、`frontend/dist`、`build/bin/Navi.exe` 属于忽略项。
- `wails build` 曾重写 `frontend/package.json.md5`、三个 `frontend/wailsjs` 生成文件和 `go.mod`；这些均是本次验证产生的跟踪文件副作用，已恢复为 HEAD 内容。报告创建前工作区重新干净。

## 2. 变更规模和模块地图

### 相对 `3ed7671..HEAD`

总计 90 个文件，新增 18,527 行，删除 2,371 行。

| 模块 | 文件数 | 新增 | 删除 | 主要变化 |
|---|---:|---:|---:|---|
| root | 16 | 3,011 | 288 | App 扫描任务、分页、NFO、远程/Jellyfin、测试 |
| service | 22 | 8,941 | 1,318 | scanner、NFO、artwork cache、thumbnail、process governor |
| database | 7 | 2,937 | 0 | 版本迁移、备份恢复、健康检查、索引 |
| repository | 9 | 783 | 17 | 原子删除、媒体分页/关联、路径身份 |
| model | 4 | 230 | 63 | 路径键、唯一约束、搜索字段 |
| frontend | 27 | 1,989 | 681 | 服务端分页接入、虚拟列表、事件状态、前端测试 |
| config | 1 | 21 | 4 | 缓存/工具配置 |
| docs | 3 | 610 | 0 | 原审查、backlog、批次记录 |
| `.codegraph` | 1 | 5 | 0 | 索引忽略配置 |

### 相对 `main...HEAD`

总计 98 个文件，新增 21,372 行，删除 2,212 行。除上述模块外，还包含 `036890b`、`3ed7671` 的 artwork cache、GFriends avatar、额外前端与 repository 变化，因此本轮也检查了图片缓存、缩略图和生成资源的交叉回归。

## 3. NAVI-01～NAVI-21 终验矩阵

| ID | 状态 | 合并阻断 | 终验摘要 |
|---|---|---|---|
| NAVI-01 | VERIFIED | 否 | 不完整枚举和非 `IsNotExist` Stat 错误会中止删除；故障注入覆盖旧数据保持。 |
| NAVI-02 | VERIFIED | 否 | 扫描外部 I/O 先完成，数据库阶段单事务提交；覆盖扫描多阶段失败回滚。 |
| NAVI-03 | PARTIAL | 是 | 支持的 UTF-8 布局已原子保存并保留未知 XML，但 UTF-16/前缀根命名空间未闭环，且文件成功后 DB 失败会分裂状态。 |
| NAVI-04 | OPEN | 是 | 远程日志仍逐请求打开、无限追加完整 URI，无公共端点限速。 |
| NAVI-05 | PARTIAL | 否 | movie/tvshow/mixed 共用删除同步并有全类型测试，但缺少明确的单集、整季、整目录逐级验收。 |
| NAVI-06 | VERIFIED | 否 | 媒体、关联、系列、库在同一事务删除，失败注入证明回滚；缓存仅提交后清理。 |
| NAVI-07 | PARTIAL | 是 | 大小写和库内唯一性已修复；文件移动/改名仍没有身份匹配，会创建新 ID 并删除旧状态。 |
| NAVI-08 | OPEN | 是 | 唯一索引和 repository upsert 已有，但桌面 Toggle 仍把任意查询错误当作不存在并直接 Create。 |
| NAVI-09 | PARTIAL | 是 | 扫描去重、context、worker shutdown 已加入；metadata queue 取消、提交后取消窗口和 RestartApp 仍未闭环。 |
| NAVI-10 | PARTIAL | 是 | 服务端分页、虚拟窗口和有界页缓存存在；50k/P95 未验证，且 NAVI-11 使首屏仍可执行 O(N) 文件 I/O。 |
| NAVI-11 | OPEN | 是 | `GetMediaList` 仍同步调用两个全库 backfill，原始根因仍在。 |
| NAVI-12 | PARTIAL | 否 | 分区、索引、容量、孤儿清理、reader reservation 已实现；缺少真实 10k/Windows 并发压力验收。 |
| NAVI-13 | PARTIAL | 否 | SQL 分页/筛选及批量 hydration 已实现并有查询数测试；P95 阈值和完整客户端兼容矩阵未成为可失败门槛。 |
| NAVI-14 | OPEN | 是 | 默认仍监听 `0.0.0.0`，无登录限速、body/header/Read/Write/Idle 完整限制、token 过期或日志脱敏。 |
| NAVI-15 | PARTIAL | 否 | detail cache 有 singleflight/24 项上限，事件按 media ID 过滤；悬停仍立即触发且不同媒体 in-flight 无界，事件刷新无完成令牌。 |
| NAVI-16 | VERIFIED | 否 | 版本迁移、WAL 一致备份、恢复、重复清理、唯一索引、单连接 PRAGMA 和旧库升级测试完整。 |
| NAVI-17 | OPEN | 是 | 播放器启动成功仍立即标已看，日志仍含完整文件/播放器路径且无限增长。 |
| NAVI-18 | PARTIAL | 否 | 列表错误状态和卡片键盘语义已加入；组件级行为、focus trap 和生成绑定一致性仍未闭环。 |
| NAVI-19 | DEFERRED | 否 | `app.go` 仍混合职责；作为 P3 维护性事项延期合理，未发现因本轮强拆导致的功能回归。 |
| NAVI-20 | OPEN | 是 | `/local/` 仍可读取任意非缓存路径，NFO 保存仍信任前端 `NFOPath`。 |
| NAVI-21 | DEFERRED | 否 | 失败历史仍只保留每库最近一次内存记录；P3 持久化延期可接受，但迁移前置条件现已满足。 |

### NAVI-01：不完整扫描误删

1. 文件/函数：`service/scanner.go` 的 `prepareOverwriteIO`、`scanLibraryWithOptionsCore`、`syncDeletedRecords`、`listMovieEntriesWithWalk`、`listMovieEntriesWithEverything`。
2. 机制：每个根建立完整快照；根缺失、权限错误、部分遍历或模糊 Stat 错误转为 `ScanIncompleteError`；仅完整快照进入删除事务。
3. 测试：`TestDeleteUpdateMissingRootPreservesMediaAndUserState`、`TestDeleteUpdatePartialTraversalPreservesMediaAndUserState`、`TestDeleteUpdateOfflineSecondRootPreservesAllRoots`、`TestDeleteUpdateAmbiguousStatPreservesMediaAndUserState`、覆盖扫描离线/部分枚举测试。
4. 原始触发：已覆盖根不存在、权限拒绝注入、部分 walk、第二根离线、非 NotExist Stat，且断言媒体、收藏、历史、系列保持。
5. 边界：真实 UNC 断线、junction/symlink、长路径仍未人工验证；Everything stale index 只通过 `totalResults` 数量一致性识别，无法识别“总数本身错误但响应自洽”。
6. 阻断：否。核心数据删除门槛已由实现和回归测试共同证明。

### NAVI-02：覆盖扫描先删旧数据

1. 文件/函数：`app.go` 的 `runScanTask`；`service/scanner.go` 的 `ScanLibraryAtomic`、`ScanLibraryOverwrite`、`prepareOverwriteIO`、`cloneForAtomicScan`。
2. 机制：文件遍历、NFO 读取和 probe 在事务前完成；数据库写入、删除、LastScan 和演员关系在单事务内执行；提交后才发事件、排 metadata 和清缓存。
3. 测试：`scanner_overwrite_test.go` 覆盖离线、部分枚举、取消、NFO/FFprobe/STRM 错误、媒体写失败、演员关系失败、LastScan/commit 失败和成功原位刷新。
4. 原始触发：任意准备阶段或事务阶段失败后均断言旧媒体 ID、收藏、历史、系列和演员关系可见；提交失败不会发成功事件或清缓存。
5. 边界：超大库长事务、真实磁盘耗尽和进程级突然断电未验证；这不改变当前“失败不先清库”的闭环。
6. 阻断：否。

### NAVI-03：NFO 数据完整性和原子保存

1. 文件/函数：`service/nfo.go` 的 `LoadEditorData`、`SaveEditorData`；`service/nfo_xml_edit.go` 的 `buildEditedNFO`、`writeNFOAtomically`；Windows `replaceNFOFileAtomic`；`app.go` 的 `SaveNFOEditorData`、`syncMediaFromNFO`。
2. 机制：对原 XML 做字节级 patch，保留未修改节点；同目录临时文件写入、Sync、Close、回读校验、并发指纹复核后使用 Windows `MoveFileEx(...REPLACE_EXISTING|WRITE_THROUGH)` 替换。
3. 测试：未知顶层字段、命名空间扩展、actor role/thumb/custom、嵌套 actors、显式空值、损坏 XML、外部修改、写/Sync/Close/replace 失败、只读/占用文件和 Unicode/LF 均有测试。
4. 原始触发：支持布局下，未知 XML 与 actor 子字段保留、写失败原文件字节不变已覆盖。
5. 边界：前缀根命名空间被明确拒绝；没有 UTF-16 round-trip；注释/编码声明主要依赖字节 patch 的间接保证。更重要的是 `SaveNFOEditorData` 在文件替换后才更新 DB，DB 事务失败不会回滚 NFO 文件，形成文件/数据库分裂。
6. 阻断：是。P1 验收未完整，且存在可复现的一致性窗口。

### NAVI-04：未认证请求和无限日志

1. 文件/函数：`remote_access.go` 的 `newJellyfinMux`、`logJellyfinRequests`、`appendRemoteAccessLog`。
2. 机制：当前没有修复机制；所有请求结束后仍同步 `os.OpenFile(...O_APPEND)` 并记录一行。
3. 测试：无日志轮转、总量上限、公共端点限速或高频未认证请求测试。
4. 原始触发：仍成立。`/`、Public Info、Ping、Branding、Public Users 和登录可未认证访问，并产生无限日志增长。
5. 边界：日志还记录 `RequestURI`、User-Agent、RemoteAddr；query token 会进入日志。
6. 阻断：是，P1 可用性/隐私风险未修复。

### NAVI-05：TV/mixed 删除一致性

1. 文件/函数：`scanTVShowLibrary`、`scanMixedLibrary`、`collectEpisodes`、`syncDeletedRecords`；repository `DeleteByIDsAndRepairSeries`。
2. 机制：三类库均把枚举文件加入根快照，统一按库删除缺失媒体，并在同事务内清关联、修复或删除 Series。
3. 测试：`TestDeleteUpdateAllLibraryTypesRemoveMissingEpisodesAndAssociations`、`TestOverwriteSuccessRefreshesInPlaceAndCleansConfirmedMissingForEveryLibraryType`，以及 repository 的系列计数/最终单集删除回滚测试。
4. 原始触发：全类型根删除和“保留一项、删除一项”已覆盖；关联清理已覆盖。
5. 边界：没有一个端到端 scanner 测试分别表达“只删单集但保留季”“删整季但保留系列”“删整目录删除系列”，SeasonCount/EpisodeCount 主要由 repository 测试间接证明。
6. 阻断：否，但发布前应补人工矩阵。

### NAVI-06：删除媒体库事务

1. 文件/函数：`repository/repo_library.go` 的 `DeleteLibraryAtomic`、`deleteLibraryScrapeRows`、`deleteLibrarySeriesAssociations`；`app.go` 的 `DeleteLibrary`。
2. 机制：加载媒体/系列 ID 后，在一个事务内删除 scrape、媒体关联、媒体、系列关联、系列和 Library；提交后才清 artwork cache。
3. 测试：repository 分别注入媒体关联、系列删除、库删除失败；App 测试覆盖提交失败不清缓存、成功后清缓存、缓存失败返回 committed warning。
4. 原始触发：锁/删除阶段失败的全图回滚和缓存顺序已覆盖。
5. 边界：真实跨进程 SQLite 锁竞争未做压力测试，但事务失败行为已故障注入。
6. 阻断：否。

### NAVI-07：路径身份、重叠库和移动

1. 文件/函数：`model.NormalizePathKey`、`LibraryPathKey`、Media/Series `BeforeSave`；`repository.MediaRepo.FindByFilePathInLibrary`、Create；数据库 v2 migration 和唯一索引。
2. 机制：统一分隔符、Clean、Windows 大小写折叠；媒体使用 `(library_id,path_key)`，系列使用 `(library_id,folder_path_key)`，库根集合使用稳定键；旧重复数据迁移合并关联。
3. 测试：`TestRepositoryUniquenessUpsertAndWindowsPathIdentity`、`TestLegacyPathDuplicatesMergeAndAudit`、`TestDuplicateSeriesMergeMovesAssociationsBeforeDelete`。
4. 原始触发：大小写变化、同一路径跨不同库隔离、旧重复合并已覆盖。
5. 边界：scanner 只按库内 PathKey 查现有媒体；`VideoFingerprint` 没有用于移动/改名匹配。移动会创建新 Media ID，delete_update 随后删除旧 ID 及收藏/历史。UNC、`\\?\`、junction 和包含重叠根的真实扫描未验证。
6. 阻断：是，原始“移动/改名状态保留”验收未实现。

### NAVI-08：Favorite/WatchHistory 唯一性和 upsert

1. 文件/函数：`model.Favorite`、`WatchHistory` 唯一索引；repository `Favorite.Add`、`WatchHistory.Upsert`；`app.go` 的 `ToggleFavorite`、`ToggleWatched`。
2. 机制：迁移会去重并建立组合唯一索引，repository 路径使用 upsert；但桌面 Toggle 仍先查询，任何非 nil 错误均进入 Create。
3. 测试：数据库测试并发调用 repository upsert 并断言各一行；远程播放状态有重复合并测试。没有 App Toggle 的锁错误/并发测试。
4. 原始触发：repository 并发写已覆盖；原始 App toggle 的“只处理 ErrRecordNotFound”没有覆盖且代码未实现。
5. 边界：锁错误、连接关闭或查询失败会被误判为不存在；两个并发 toggle 的最终布尔语义也未定义。
6. 阻断：是，数据完整性入口仍有原始根因。

### NAVI-09：扫描/worker 生命周期

1. 文件/函数：App scan registry、`CancelScan`、`shutdown`、`RestartApp`；scanner metadata worker/queue/Shutdown、process governor；thumbnail worker Stop/Shutdown。
2. 机制：同库任务互斥；App context 和 task context 传入扫描/Everything/FFprobe；shutdown 取消任务并在总超时内等待 scan、maintenance、thumbnail、scanner、artwork。
3. 测试：重复扫描、取消/重试、Everything HTTP 取消、FFprobe command context、scanner/thumbnail/governor shutdown 和幂等测试。
4. 原始触发：同库并发、主要子进程取消和普通 shutdown 已覆盖。
5. 边界：clone 的 `enqueueMetadataCompletion` 在 `metadataOwner` 分支把任务委托给 owner，owner 没有该 scan context；queue 满时任务取消不能打断等待。已入队 metadata 任务不带 task context，取消后仍可写 DB/发事件。事务提交后到 terminal 之间的取消也可能仍按 completed 结束。`RestartApp` 直接启动替代进程并 `os.Exit(0)`，绕过 graceful shutdown。
6. 阻断：是，取消、重启和满 channel 路径未闭环，race detector 又未能运行。

### NAVI-10：前端分页和大型库性能

1. 文件/函数：`app.go:GetMediaList`；`MediaGrid.tsx`；`mediaPagination.ts`；搜索字段迁移和索引。
2. 机制：后端 page/size 上限 200、稳定排序和服务端搜索；前端仅请求可见相邻页，页缓存上限 7，请求以 generation 隔离。
3. 测试：后端 10k 行分页、最后页收缩、筛选计数、完整搜索语义；前端 10k 虚拟窗口、有界缓存、旧 generation 拒绝和请求去重。
4. 原始触发：整库 JSON/多份整库缓存已消除；标题、编号、maker、label、year、演员、拼音和首字母搜索有回归测试。
5. 边界：没有 50k Wails/React/P95/内存基线；Node 测试只执行 utility，不渲染 MediaGrid。更关键的是默认排序仍触发 NAVI-11 的全库 Stat/UPDATE，因此首屏复杂度没有真正降为页级。
6. 阻断：是，与 NAVI-11 联合阻断“大型库性能已闭环”的声明。

### NAVI-11：列表请求文件时间 backfill

1. 文件/函数：`app.go:GetMediaList`、`backfillMediaNfoModTime`、`backfillMediaFileCreatedAt`。
2. 机制：当前仍在 `created_at/added_at/默认` 排序时同步查询所有缺失行，逐条 `os.Stat` 并 Update；没有版本标记、批次调度或失败重试时间。
3. 测试：无“列表请求不做文件 I/O/写入”测试。10k 分页测试反而会对 10k 个不存在路径执行 Stat，但不检测这一副作用。
4. 原始触发：完全仍在。旧库、离线路径和永久失败路径每次列表请求都会重复扫描。
5. 边界：网络盘会把一次 120 项首屏请求放大成全库网络文件 I/O；错误被静默忽略，用户只能感知卡顿。
6. 阻断：是，原 P2 根因未修复并抵消 NAVI-10。

### NAVI-12：图片缓存容量和读取保护

1. 文件/函数：`ArtworkCache`、`artwork_maintenance.go`、App `reserveArtworkPath`、Local/Jellyfin image handlers、thumbnail service/worker。
2. 机制：按媒体子目录、索引、high/low bytes、max files、LRU 近似清理、孤儿/reconcile、批次 temp 清理；Reserve 在 ServeFile 全生命周期增加 active reader；generateOnce 和 process governor 去重生成。
3. 测试：命中/版本键/无全树 walk、并发生成失败清理、索引重建、容量、reservation/eviction 原子性、索引失败重试、shutdown、跨批次维护和 Local/Jellyfin 响应持有 reservation。
4. 原始触发：正在读取/生成文件不被清理、索引与磁盘恢复、容量和按媒体删除机制已覆盖。
5. 边界：未执行真实 10k 删除耗时基线；Windows 杀毒/文件占用、scanner 与 thumbnail 同媒体并发、shutdown 超时下的长 ServeFile 仍需压力测试。Race detector 未运行。
6. 阻断：否，作为手工性能/并发验证项保留。

### NAVI-13：Jellyfin SQL 分页和 hydration

1. 文件/函数：`jellyfin_query.go` 的 `queryJellyfinItemsSQL`、resume/latest、`loadJellyfinItemRefs`、`hydrateJellyfinItems`；remote DTO。
2. 机制：union SQL 先筛选/count/稳定排序/limit/offset，再按种类批量加载；收藏、观看、series 名称和统计批量 hydration。
3. 测试：查询数上限、分页稳定性、筛选/排序/用户隔离、缺失关联、series live visibility、resume/latest 和 100/1k/10k 性能基线数据集。
4. 原始触发：全量后分页和主要 N+1 已消除；TotalRecordCount 和分页契约有测试。
5. 边界：性能测试主要记录日志，没有 P95/内存失败阈值；只实现部分 Jellyfin sort/filter 组合。真实 Infuse/Jellyfin 客户端、Range/HEAD 兼容仍未验证。
6. 阻断：否，但不能计为生产客户端兼容已通过。

### NAVI-14：远程访问安全

1. 文件/函数：`defaultRemoteBindHost`、`buildJellyfinServer`、`handleJellyfinAuthenticate`、`jellyfinSessionFromRequest`、请求日志。
2. 机制：当前仅有 `ReadHeaderTimeout=10s`、随机 token 和常量时间密码比较；其余建议未实现。
3. 测试：仅有正常登录/列项目流程。无错误密码速率、用户名/path/header/IP 绕过、超大 body、慢读写、token 过期、日志脱敏测试。
4. 原始触发：默认仍为 `0.0.0.0`；登录无限速；JSON body 无 `MaxBytesReader`；server 无 Read/Write/Idle/MaxHeaderBytes；接受 query `api_key`；session 无到期。
5. 边界：完整 URI 写日志泄 token；公共端点和登录可消耗 CPU/内存/日志；LAN 暴露不要求显式确认。
6. 阻断：是，安全验收大部分未实现。

### NAVI-15：详情预取和旧请求覆盖

1. 文件/函数：`MediaCard.tsx`、`mediaDetailCache.ts`、`MediaDetail.tsx`、后端 `GetMediaDetailBundle`。
2. 机制：同一 media ID 请求 singleflight，缓存最多 24 项；初始详情 effect 使用 active flag；metadata 事件先比较 media ID。
3. 测试：media card comparator/键盘 utility 有测试；没有真实 hover 并发池、MediaDetail 切换或事件竞态组件测试。
4. 原始触发：同 ID 重复请求受控；不同卡片快速扫过仍可为每个 ID 立即创建一个 in-flight 请求，没有 debounce 或全局并发上限。
5. 边界：事件触发的 `refreshDetailAndPreviews` 没有 active/generation 检查，旧媒体刷新完成后仍可能 setState 覆盖新详情；in-flight map 本身无容量上限。
6. 阻断：否，属于显著但非当前最高级别的交互/资源风险。

### NAVI-16：数据库迁移、备份和恢复

1. 文件/函数：`database.Open`、`applyMigrations`、`createBackupLocked`、`Restore`、DefaultMigrations、健康检查/索引校验。
2. 机制：连续版本表；每个 migration 单事务；升级前 `VACUUM INTO` WAL 一致备份；恢复先校验候选、保留当前库、安全替换并失败回退；默认单连接池，DSN 对每连接设置 WAL/foreign_keys/busy_timeout。
3. 测试：顺序/幂等、失败回滚、备份失败阻止迁移、WAL 最新行、保留策略、恢复成功/失败、旧重复清理、旧库直升、外键清理、损坏/新版本拒绝、索引修复、checkpoint。
4. 原始触发：旧数据库、WAL、重复数据、迁移失败、恢复失败和唯一索引前清理均有故障注入证明。
5. 边界：migration 2 失败时 version 1 可保留，这是版本级原子而非整个升级批次回退；测试明确验证该语义。自定义 `MaxOpenConns>1` 没有多连接压力测试，但产品默认是 1，且 `_pragma` 在 DSN 上。
6. 阻断：否。

### NAVI-17：播放状态和日志隐私

1. 文件/函数：`app.go:PlayFile`、`markWatchedByFilePath`、`appendPlayLatencyLog`。
2. 机制：当前没有修复；detached process 启动成功后立即标已看，并逐次 append 日志。
3. 测试：无立即退出不标已看、日志脱敏或日志轮转测试。
4. 原始触发：仍成立。播放器启动不代表达到观看阈值。
5. 边界：成功和失败日志均包含完整媒体路径、播放器路径，文件无大小/数量上限。
6. 阻断：是，状态语义和隐私验收均未实现。

### NAVI-18：错误状态、类型和可访问性

1. 文件/函数：`MediaGrid.tsx`、`MediaCard.tsx`、前端 generated models 和 utility tests。
2. 机制：列表有 error/stale/empty/loading 状态；卡片 `role=button`、`tabIndex=0`、Enter/Space；部分接口使用生成类型。
3. 测试：分页/错误 gate utility、card state 和键盘 utility 测试；`tsc` 构建通过。
4. 原始触发：列表错误不再简单伪装空库，卡片键盘入口已实现。
5. 边界：没有组件/浏览器级 retry、focus restore、modal focus trap 测试；仍大量使用 `any`。`wails build` 会重写绑定文件并报告多次 `Not found: time.Time`，说明生成契约未保持 clean-build 一致。
6. 阻断：否，但生成绑定漂移应在合并前处理或明确接受。

### NAVI-19：维护性拆分

1. 文件/函数：`app.go`、config、repository 聚合。
2. 机制：本轮仅增加 database/jellyfin 等独立文件，App 仍承担大量 facade、文件、进程和设置职责。
3. 测试：核心新增路径有测试，但没有结构性验收。
4. 原始触发：维护性问题仍存在，没有因为激进删除 V3 代码引入回归。
5. 边界：继续叠加任务会扩大 App 隐式状态。
6. 阻断：否；P3 延期合理。

### NAVI-20：renderer 本地文件边界

1. 文件/函数：`main.go:LocalFileHandler.ServeHTTP`、`app.go:reserveArtworkPath`、`SaveNFOEditorData`。
2. 机制：cache 内路径会 Reserve；但非 cache 路径 `reserveArtworkPath` 直接返回 true，随后 `ServeFile`。NFO 仅在 `NFOPath` 为空时重算路径，非空时直接使用前端值。
3. 测试：只有 artwork reservation 生命周期和不支持 NFO 布局不改文件测试；无越界读写安全测试。
4. 原始触发：仍成立。renderer 可构造 `/local/<absolute path>` 读取任意文件，也可把 `NFOPath` 指向任意可写 XML 路径。
5. 边界：即使当前未确认 XSS，Wails renderer 本身就是高权限边界；一旦内容注入或导航失控，读写能力直接可用。
6. 阻断：是，P2 安全问题未修复。

### NAVI-21：扫描失败历史持久化

1. 文件/函数：App `lastScanTasks`、`lastScanFailures`、`GetLastScanFailure`、`RetryFailedScan`；thumbnail failure map。
2. 机制：每媒体库只保留最近一次内存记录；成功或取消会删除失败；重试生成新 task ID；取消任务不可重试。
3. 测试：失败查询/重试新 ID、不完整根重试、取消不可重试；前端 terminal history 有界测试。
4. 原始触发：应用重启后历史消失仍未解决。
5. 边界：没有数据库表、保留数量/时间策略；但不会恢复已取消 context。
6. 阻断：否；P3 明确延期。版本迁移基础已完成，后续实现不再有前置阻塞。

## 4. 新发现问题清单

| 严重度 | 问题 | 证据与影响 |
|---|---|---|
| P1 | NFO 文件与数据库非原子 | `SaveNFOEditorData` 先替换文件，再执行 DB transaction；后者失败时返回错误但文件已改变，UI/DB/NFO 状态分裂。 |
| P1 | 远程安全修复基本未落地 | 默认全接口监听、无限日志、完整 URI、无限登录、无限 body、无 token 过期和不完整 timeout 同时存在。 |
| P1 | renderer 仍可任意本地读写 | `/local/` 对非缓存路径放行；NFO 保存信任非空前端路径。 |
| P2 | metadata queue 满时 task cancel 无效 | clone 委托 owner enqueue 后丢失 scan context；取消不能打断满 channel，已排队任务也不带 task context。 |
| P2 | RestartApp 绕过 shutdown | 替代进程启动后直接 `os.Exit(0)`，不等待扫描、FFmpeg、cache 或 DB checkpoint。 |
| P2 | 服务端分页仍伴随全库文件 I/O | 默认列表排序每次调用两个 backfill，10k 分页测试未检测这一副作用。 |
| P2 | 桌面 Toggle 未使用已实现的 upsert | 任意查询错误进入 Create，缺少锁冲突和并发 toggle 语义测试。 |
| P3 | 生产 clean build 不干净 | `wails build` 会重写 5 个跟踪文件并重复警告 `Not found: time.Time`；当前提交的生成绑定/依赖元数据与生成器输出漂移。 |

## 5. 修复引入的回归问题

未证明一个可以独立归因于某一修复提交的用户可见行为回归，因此 NAVI 状态中 `REGRESSION=0`。但有两个明显的跨轮次风险：

1. 分页修复与旧 backfill 组合后，UI 虽只取 120 行，后端仍可能对全库做 Stat/UPDATE，性能收益被部分抵消。
2. 取消/原子扫描修复与共享 metadata worker 组合后，主扫描事务可正确回滚，但任务 context 没有进入共享队列项，取消后的后台写入和事件仍可能继续。

生产构建生成文件漂移列为新发现的构建一致性问题，不冒充已经实证的运行时回归。

## 6. 自动测试与构建结果

| 命令 | 结果 | 记录 |
|---|---|---|
| `go test -count=1 ./...` | PASS | 首次工具调用因 1.7 秒外部超时被终止；以 10 分钟限时重跑后通过。root 12.137s、database 6.841s、repository 0.253s、service 5.198s。 |
| `go vet ./...` | PASS | 退出码 0，无输出。 |
| `go test -race -count=1 ./...` | NOT RUN / UNSUPPORTED | 命令实际尝试，失败信息：`-race requires cgo`；`GOOS=windows`、`GOARCH=amd64`、`CGO_ENABLED=0`，且本机无 `gcc`/`clang`。不能计为通过。 |
| `cd frontend; npm ci` | PASS | 安装 74 个包；有 `sourcemap-codec` deprecated 警告。 |
| `npm test` | PASS | 23/23，通过，0 fail/skip。测试集中于 utility/Node，不是浏览器组件测试。 |
| `npm run build` | PASS WITH WARNING | `tsc && vite build`；1770 modules；两次 `use client` ignored 警告。 |
| `wails build` | PASS WITH WARNING | Windows/amd64 production build 成功，产物 `build/bin/Navi.exe`，12.874s；多次 `Not found: time.Time`；构建重写的跟踪文件已恢复。 |

## 7. 未完成的人工测试矩阵

| 场景 | 对应风险 | 所需人工/CI 验证 | 当前是否通过 |
|---|---|---|---|
| Windows race detector | scanner/cache/thumbnail/remote/session map 并发 | 在带 MinGW/LLVM、`CGO_ENABLED=1` 的 Windows CI 运行全量 `go test -race` | 否 |
| 真实网络盘/UNC/长路径/junction | NAVI-01/05/07 | 扫描中断网、拒绝访问、恢复、大小写变化、`\\?\` 和重叠根，核对媒体/收藏/历史/系列 | 否 |
| 真实 Everything | 不完整结果和取消 | stale index、分页截断、HTTP 故障、总数错误、取消，无误删且能 fallback/报 incomplete | 否 |
| 真实 FFprobe/FFmpeg | cancel/shutdown 子进程 | 正常、损坏视频、挂起进程、退出/重启，确认无遗留进程和 DB 后写 | 否 |
| NFO 编码/边界 | NAVI-03/20 | UTF-16、注释、声明、前缀 namespace、占用文件、路径越界、DB 更新失败后的恢复策略 | 否 |
| LAN 安全压力 | NAVI-04/14 | 高频匿名/错误密码、用户名/path/header/IP 变化、超大 body/header、slowloris、token/query 日志检查 | 否 |
| Jellyfin/Infuse 客户端 | NAVI-13/14 | 实机分页、筛选、排序、TotalRecordCount、Range/HEAD、图片、播放进度、多页浏览 | 否 |
| 50k Wails/React | NAVI-10/11/15/18 | 首屏/搜索/切库/删最后页/快速滚动/扫描事件风暴，记录 P95、内存、请求数、文件 I/O | 否 |
| Artwork Windows 压力 | NAVI-12 | ServeFile 长读、容量清理、同时生成/删除、AV 占用、shutdown 总超时、index 损坏恢复 | 否 |
| RestartApp 活跃任务 | NAVI-09 | 扫描/thumbnail/cache/remote 活跃时重启，确认旧进程退出、DB checkpoint、无双写 | 否 |

## 8. 合并阻断项

1. **NAVI-04 + NAVI-14**：远程访问仍默认 LAN 暴露，缺少限速、日志上限/脱敏、body/连接完整限制和 token 到期。
2. **NAVI-03 + NAVI-20**：NFO 路径仍可越界写，文件与 DB 更新非原子，UTF-16/前缀根 namespace 验收不完整；`/local/` 仍可任意读取。
3. **NAVI-07**：移动/改名不保留 Media ID、收藏和观看历史。
4. **NAVI-08**：桌面 Toggle 仍把任意查询错误当作不存在，未使用已实现的 upsert 入口。
5. **NAVI-09**：metadata channel 取消丢失 task context，取消后任务仍可能写入；RestartApp 绕过 shutdown。
6. **NAVI-10 + NAVI-11**：分页路径仍同步做全库 Stat/UPDATE，未达到大型库首屏/P95 验收。
7. **NAVI-17**：播放启动即已看，完整本地路径写入无界日志。

## 9. 非阻断延期项

- **NAVI-19**：App 职责拆分，P3 维护性，延期合理。
- **NAVI-21**：失败历史持久化和有界保留，P3；迁移基础已经具备，应进入下一批而非继续无限延期。
- NAVI-05 的单集/整季人工矩阵、NAVI-12 的真实 10k/Windows 压力、NAVI-13 的客户端/P95、NAVI-15/18 的浏览器组件竞态与可访问性验证可以作为非阻断补充，但不能抵消上述阻断项。

## 10. 最终结论

**NOT READY**

状态计数：

- VERIFIED：4
- PARTIAL：9
- UNVERIFIED：0
- OPEN：6
- REGRESSION：0
- DEFERRED：2

自动化测试和构建全部通过只能证明当前测试集合可运行；它们没有覆盖、也没有推翻仍然存在的远程安全、任意本地读写、播放状态/日志、列表全库 I/O、移动身份、Toggle 错误处理和取消/重启生命周期问题。以上阻断项关闭前，不应合并。
