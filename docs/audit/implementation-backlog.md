# Navi-Gui 实施清单

| 问题 ID | 严重度 | 根因 | 涉及文件和函数 | 修复建议 | 验收标准 | 依赖关系 | 推荐实施批次 |
|---|---|---|---|---|---|---|---|
| NAVI-01 | P1 | 遍历错误被吞掉，任意 Stat 错误被当作删除。 | service/scanner.go：listMovieEntriesWithWalk、syncDeletedMovieRecords | 跟踪根目录完整性；仅完整可访问时删除同步；仅 IsNotExist 视为缺失。 | 目录不存在、拒绝访问、部分遍历失败时，媒体、收藏、历史、系列不变。 | 无；NAVI-02/05/09 前置。 | 批次 1：数据安全 |
| NAVI-02 | P1 | 覆盖刷新在扫描成功前删除媒体、系列和缓存。 | app.go：ScanLibraryWithMode | staging scan 后切换；最低限度覆盖前备份和预检。 | 任意阶段失败后旧媒体、ID、收藏、历史、系列可恢复可见。 | NAVI-01；建议 NAVI-16。 | 批次 1：数据安全 |
| NAVI-03 | P1 | NFO 只序列化部分 XML，且直接覆盖源文件。 | service/nfo.go：SaveEditorData | 保留未知 XML；临时文件、Flush/Close、原子替换、可选备份。 | 未知标签、role/thumb、编码保留；写失败时原文件完整。 | 无。 | 批次 1：数据安全 |
| NAVI-04 | P1 | 未认证端点可触达，逐请求追加日志且无上限。 | remote_access.go：Jellyfin 请求日志 | 日志轮转和总量限制；公共端点限速；避免逐请求开文件。 | 高频未认证请求下日志有上限且服务可用。 | 与 NAVI-14 联合。 | 批次 1：远程安全 |
| NAVI-05 | P2 | TV/mixed 走旧扫描路径，删除同步只覆盖电影。 | service/scanner.go：ScanLibraryWithOptions | 统一枚举结果和按根删除同步。 | TV/mixed 删除单集、整季、根目录后无幽灵记录。 | NAVI-01。 | 批次 2：扫描一致性 |
| NAVI-06 | P2 | 删除关联并非同一事务，媒体删除失败仍继续删除库。 | app.go：DeleteLibrary；repository/repo_library.go | 媒体、系列、库在同一事务，任一步失败回滚。 | 注入锁冲突/删除失败后所有记录保持原状。 | NAVI-16。 | 批次 1：数据安全 |
| NAVI-07 | P2 | 无路径规范化键/唯一约束，查找未限库 ID。 | model/model.go、service/scanner.go、repository/repo_media.go | path_key 与组合唯一约束；文件身份/指纹匹配移动。 | 大小写、重叠库、移动/改名不重复或错配，状态保留。 | NAVI-16。 | 批次 3：文件身份 |
| NAVI-08 | P2 | 状态表无组合唯一；任意查询错误会创建。 | model/model.go；app.go toggle | 组合唯一 + upsert；只处理 ErrRecordNotFound。 | 并发桌面/远程写入时每用户媒体最多一条状态。 | NAVI-16。 | 批次 1：数据完整性 |
| NAVI-09 | P2 | worker 无限运行；退出不停止 scanner/thumbnail。 | service/scanner.go、service/thumbnail_worker.go、shutdown | App context、WaitGroup、Stop、等待事务和子进程。 | 取消/退出/重启后无遗留 worker/FFmpeg，且在超时内完成。 | NAVI-01。 | 批次 2：任务生命周期 |
| NAVI-10 | P2 | 整库请求并缓存多份完整列表。 | MediaGrid.tsx、GetMediaList、persistentCache.ts | 后端分页/搜索；前端窗口加载；轻量缓存。 | 10k/50k 媒体首屏、搜索、内存、JSON 达 P95 基线。 | NAVI-11/索引。 | 批次 3：大型库性能 |
| NAVI-11 | P2 | 列表请求逐条执行 Stat + UPDATE。 | app.go：GetMediaList backfill | 独立分批迁移，记录版本/失败重试。 | 列表请求不触发批量文件 IO/写入；离线路径不反复回填。 | NAVI-16。 | 批次 2：列表稳定性 |
| NAVI-12 | P2 | 图片缓存读取/删除反复扫描全目录，无容量治理。 | service/artwork_cache.go | 按媒体分区或缓存索引；批量删除；容量/年龄/孤儿清理。 | 10k 删除耗时不随缓存总文件数线性放大；缓存有上限。 | 无。 | 批次 3：缓存性能 |
| NAVI-13 | P2 | Jellyfin 全量加载后分页，系列/userData N+1。 | remote_access.go：queryJellyfinItems、DTO | SQL 筛选/排序/分页；批量预载。 | 10k 项 Items 查询次数和 P95 达基线，契约不变。 | NAVI-10 的分页/索引可复用。 | 批次 3：远程性能 |
| NAVI-14 | P2 | 全接口监听，认证/请求体/连接限制不足，URI 可泄 token。 | remote_access.go：监听、认证、token、body | 默认 loopback；显式 LAN；限速/body/timeout/token 过期/脱敏；TLS 说明。 | 未授权、错误密码、超大 body、慢连接、query token 均受限且无泄露。 | NAVI-04。 | 批次 1：远程安全 |
| NAVI-15 | P2 | 悬停预取无界，事件刷新无媒体 token。 | MediaCard.tsx、MediaDetail.tsx、app.go | 延迟+并发池；singleflight；刷新检查 media ID token。 | 快速扫卡片时并发受限，旧请求不能覆盖新详情。 | NAVI-12 可协同。 | 批次 2：交互性能 |
| NAVI-16 | P2，待验证 | AutoMigrate 无版本/备份/恢复；连接池和 PRAGMA 一致性未证实。 | app.go startup/PRAGMA；model/model.go | 版本化迁移、升级前备份、恢复、sql.DB 和锁策略。 | 旧库升级、失败恢复、并发锁测试通过。 | NAVI-02/06/07/08/11 的基础。 | 批次 1：数据库基础 |
| NAVI-17 | P2 | 播放启动即已看，日志含完整路径且无轮转。 | app.go：PlayFile | 写最近播放；已看由用户/阈值决定；日志轮转脱敏。 | 立即退出不标已看；日志不含完整路径且有上限。 | NAVI-04 日志设施。 | 批次 2：播放可靠性 |
| NAVI-18 | P3 | 错误伪装为空状态，类型/键盘语义不足。 | MediaGrid.tsx、MediaCard.tsx、App/Settings | error/loading/empty 状态；生成类型；语义卡片和焦点管理。 | API 错误可见可重试；键盘完成操作；TS 契约测试通过。 | 可与 NAVI-10 合并。 | 批次 4：体验与维护 |
| NAVI-19 | P3 | app.go 混合职责，存在未验证 V3 包袱。 | app.go、config/config.go、repository/repository.go | 仅渐进拆分扫描、设置/密钥、播放、远程；先确认 V3 调用。 | 核心行为不变且可独立单测；不删除未验证旧代码。 | P1/P2 稳定后。 | 批次 4：维护性 |
| NAVI-20 | P2，待验证 | /local/ 直接服务前端路径；NFO 保存信任前端路径。 | main.go handler；app.go：SaveNFOEditorData | 由媒体 ID/受控根解析；保存时后端重算 NFO 路径。 | renderer 输入无法越界读写；安全边界集成测试通过。 | 先验证 renderer 威胁模型；与 NAVI-14 协同。 | 批次 2：安全收口 |
| NAVI-21 | P3 | 批次 5 的扫描失败历史仅保留最近一次内存态记录，应用重启后不可查询。 | app.go：lastScanFailures；service/thumbnail_worker.go | 在建立版本化迁移体系后增加轻量任务失败历史表和保留策略；不得恢复已取消 context。 | 重启后可查询有限条失败历史，重试生成新任务 ID，历史表有容量上限。 | NAVI-16。 | 后续：任务历史持久化 |
