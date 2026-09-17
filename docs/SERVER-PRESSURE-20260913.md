# 2026-09-13 服务器压力事故记录

## 证据时间线

- 约 09:29–10:11：本项目 account-list-filter Docker 构建运行约 42 分钟；期间 Go/Web 构建与 root 的 Go 测试叠加。
- 09:48:35：journald watchdog 超时；10:11:23 被强制终止并重新启动。
- 10:11:23：kernel global OOM 终止 codex PID 1603780；该进程 anon RSS 2,275,768 kB（约 2.17 GiB），记录的 swap snapshot 约 758 MiB。
- 10:11:44：Docker 构建日志完成；10:12:53：项目过滤测试日志完成。后续采用 `GOMAXPROCS=2 -p 1` 串行执行，HTTP 包测试约 23 秒通过（不是整轮测试的总时间）。

## 资源证据

主机容量为 7.48 GiB RAM、2 GiB swap；OOM 时剩余 swap 约 248 kB。该时刻记录的 3 个 codex 进程合计 RSS 约 2,618 MiB、swap 约 900 MiB；13 个 Node 进程合计 RSS 约 1,741 MiB、swap 约 334 MiB，归属并非全部可确认于本项目。icloud-api 当时 RSS 约 14 MiB，业务两个容器 `oom=false`。恢复后 idle 约 93–99%、无 swap in/out、磁盘约 35%、systemd failed=0。

## 因果界限

已证实直接故障是主机级内存与 swap 枯竭并触发 global OOM。Docker Go/Web 构建与主机 Go 测试存在并发叠加，OOM 快照中的最大单进程占用来自另一 codex 进程（当前项目调度进程为 PID 2762428）。缺少整个故障期的逐进程采样，尚不足以认定某个进程发生内存泄漏，或量化各任务对故障的贡献。业务两个容器未被 OOM 直接终止，也不应将全部 Node/codex 资源归因于本项目。

恢复检查：本机 `/healthz` 返回 200，约 1 ms；公网管理页返回 200，约 74 ms；业务 PostgreSQL 阻塞会话数为 0。此次检查未停止其他会话或业务服务，也未再次运行构建或全套测试。

后续按 `AGENTS.md` 的重任务锁、资源阈值和并发上限执行。
