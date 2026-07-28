# Hydra 阶段交接文档

**最后更新**: 2026-07-28 21:30
**HEAD**: `db75385` P2-A.1: CLI contract closure
**建议 tag**: `v0.5.0` (未打)

---

## 1. 本地 VERIFIED

以下项在 MacBook 本地源码 `db75385` 上验证通过：

| 维度 | 结果 | 命令 |
|------|------|------|
| Git clean | clean | `git status -s` |
| gofmt | 无 diff | `gofmt -l .` |
| go vet | 通过 | `go vet ./...` |
| go build | 通过 | `go build -o /tmp/hydra-verify ./cmd/hydra` |
| go test -race | 全 7 包通过 | `go test ./... -race -timeout 120s -count=1` |
| 测试函数数 | 224 | `grep -rn "func Test" --include="*_test.go" \| wc -l` |
| shutdown race (3x) | 稳定 | `go test ./internal/proxy/ -race -run "Shutdown\|Drain\|Loop" -count=3` |

### 关键测试覆盖

| 测试组 | 测试数 | 覆盖内容 |
|--------|--------|----------|
| Migration | 2 | v9 一次性迁移（open→modify→close→reopen）、首次 backfill |
| Auth | 8 | /metrics 鉴权、/v1/models 鉴权、Bearer/X-API-Key/X-Goog-API-Key、DB error fail-closed、open access when no keys |
| Shutdown | 2 | 正常 SIGINT/SIGTERM 返回 nil (exit 0)、background loops join before DB close |
| CLI 契约 | 18 | version/--version、invalid output (exit 2)、unknown command、status/accounts list/key list/add/remove/update、doctor、not-found (table+JSON)、secret non-leakage |
| Adapter (real DB) | 9 | not-found → (nil,nil)、write paths、status、list-with-filter、sql.ErrNoRows 不泄漏 |
| Service (fake+real) | 10 | not-found、remove key、disable account、add key、status、list accounts |

---

## 2. 部署 GAP

omarchy 生产部署版本 = `v0.4.16` = commit `dfce59b` (Jul 27 19:42)。
源码 HEAD = `db75385` (Jul 28 18:39)。**落后 8 个 commit**。

### GAP 清单

| Commit | 内容 | 部署影响 | 严重性 |
|--------|------|----------|--------|
| `789a8bf` | /metrics + /v1/models 要求 API key | 当前任何人可访问 /metrics（含 key label、用量） | **高** |
| `789a8bf` | DB error fail-closed | 旧版本 DB 异常时可能返回空结果而非 401 | 中 |
| `978c6e3` | Graceful shutdown, health auto-recovery | 旧版本 SIGTERM 直接断连，health check 无自动恢复 | 中 |
| `069410b` | operator_disabled/health_disabled 分离 | 旧版本只有单一 disabled 列 | 中 |
| `069410b` | DB chmod 0600 | 旧版本 DB 权限 644（token 明文可读） | **高** |
| `069410b` | serve exit 0 on signal | 旧版本 SIGINT 返回 exit 1 | 低 |
| `a3fc74b` | v9 migration idempotency | 旧版本无 v9 migration，不存在此 bug | 无 |
| `a3fc74b` | context-aware shutdown | 旧版本 in-flight 外部调用不接收 ctx | 中 |
| `618b2ac` | P2-A: application service, JSON output | CLI 无 --output/--version/status/doctor | 低 |
| `db75385` | P2-A.1: CLI contract closure | 错误渲染不统一，无 exit code 映射 | 低 |

### 生产环境实测结果 (2026-07-28 21:00)

| 端点 | 部署版本响应 | HEAD 源码预期 |
|------|-------------|--------------|
| `/healthz` | 200 "ok" | 200 "ok" (未变) |
| `/readyz` | 404 | 404 (P2-C 未实现) |
| `/livez` | 404 | 404 (P2-C 未实现) |
| `/metrics` (无 key) | **200** | **401** |
| `/v1/models` (无 key) | **200** | **401** |
| `/v1/chat/completions` (有 key) | "no available accounts" | 同 (账号全 disabled) |

| DB 维度 | 部署版本值 | HEAD 源码预期 |
|---------|-----------|--------------|
| `PRAGMA user_version` | 0 | 9 (迁移后) |
| accounts 列 | 无 health_disabled/operator_disabled | 有 (v8/v9) |
| DB 权限 | 644 | 600 |
| WAL 权限 | 644 | 600 |
| SHM 权限 | 644 | 600 |

| 账号状态 | 值 |
|----------|-----|
| account #2 | disabled=1, last_error=health check EOF, token 过期 21h, quota=100 |
| account #3 | disabled=1, last_error=health check EOF, token 过期 21h, quota=100 |

---

## 3. 旧 DB 首次迁移风险

新二进制首次 `db.Open()` 打开旧 DB (user_version=0) 时，自动执行：

1. `ensureColumn("accounts", "health_disabled", "INTEGER NOT NULL DEFAULT 0")` — 添加列
2. `ensureColumn("accounts", "operator_disabled", "INTEGER NOT NULL DEFAULT 0")` — 添加列
3. `user_version=0 < 9` → 执行一次性 backfill：
   ```sql
   UPDATE accounts SET operator_disabled = 1 WHERE disabled = 1 AND health_disabled = 0
   ```
4. `PRAGMA user_version = 9`
5. `secureDBPermissions()` → chmod 600 (DB + WAL + SHM)

### 风险：backfill 语义错误

两个账号实际是 **health check EOF** 导致的 disabled，但旧 schema 无法区分 health-disabled 和 operator-disabled。backfill 会把它们标为 `operator_disabled=1`（人工禁用）。

**后果**：
- 新二进制的 health check 自动恢复逻辑只看 `health_disabled`，不看 `operator_disabled`
- 迁移后两个账号不会自动恢复，必须手动 `hydra accounts enable 2 && hydra accounts enable 3`
- 这是一次性操作，`user_version=9` 防止 backfill 重复执行

**缓解**：迁移后立即手动 enable 两个账号，让 health check 重新探测。

### 风险：WAL checkpoint

旧 DB 有 4MB WAL 文件。迁移的 ALTER TABLE 会触发 WAL 写入。如果迁移过程中断（如 OOM），WAL 可能包含部分迁移结果。SQLite 的 WAL 恢复机制可以处理这种情况，但建议：
- 迁移前备份 DB
- 确保迁移过程中不强制 kill 进程

---

## 4. 账号恢复步骤

**前提**：新二进制已部署且 v8/v9 迁移完成。

```bash
# 1. 确认迁移完成
~/.local/bin/hydra status
# 应显示 2 accounts, 0 active, 2 disabled

# 2. 手动启用两个账号（backfill 错误地标记为 operator_disabled）
~/.local/bin/hydra accounts enable 2
~/.local/bin/hydra accounts enable 3

# 3. 等待 health check 周期（120s）或手动刷新 token
~/.local/bin/hydra accounts refresh 2
~/.local/bin/hydra accounts refresh 3

# 4. 验证账号恢复
~/.local/bin/hydra status
# 应显示 2 accounts, 2 active, 0 disabled

# 5. 验证真实请求
KEY=$(sqlite3 ~/.config/hydra/hydra.db "SELECT key FROM api_keys WHERE id=4;")
curl -s -H "Authorization: Bearer $KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}]}' \
  http://127.0.0.1:18045/v1/chat/completions
```

---

## 5. Rollback 方案

### 方案 A：回退二进制（推荐）

```bash
# 1. 保留旧二进制
cp ~/.local/bin/hydra ~/.local/bin/hydra.v0.5.0
cp ~/.local/bin/hydra.bak ~/.local/bin/hydra  # 恢复旧二进制

# 2. 重启
systemctl --user restart hydra.service

# 3. 验证
~/.local/bin/hydra --help  # 应无 version/status/doctor 命令
curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18045/metrics  # 应返回 200 (旧版本无鉴权)
```

**注意**：v8/v9 migration 不可逆。回退到旧二进制后：
- `health_disabled`/`operator_disabled` 列仍存在（旧代码忽略它们）
- `user_version=9` 不会被旧代码重置
- 旧代码继续使用 `disabled` 列，新列不影响行为
- **安全**：可以安全回退

### 方案 B：回退 DB（仅紧急情况）

```bash
# 1. 停止 hydra
systemctl --user stop hydra.service

# 2. 恢复 DB 备份
cp ~/.config/hydra/hydra.db.bak ~/.config/hydra/hydra.db
rm -f ~/.config/hydra/hydra.db-wal ~/.config/hydra/hydra.db-shm

# 3. 恢复旧二进制
cp ~/.local/bin/hydra.bak ~/.local/bin/hydra

# 4. 重启
systemctl --user start hydra.service
```

---

## 6. 部署前检查清单

部署新二进制前，需要 master 明确批准以下操作：

- [ ] **构建新二进制**：`GOOS=linux GOARCH=amd64 go build -o /tmp/hydra-linux ./cmd/hydra`
- [ ] **备份旧二进制**：`ssh omarchy 'cp ~/.local/bin/hydra ~/.local/bin/hydra.bak'`
- [ ] **备份 DB**：`ssh omarchy 'cp ~/.config/hydra/hydra.db ~/.config/hydra/hydra.db.bak'`
- [ ] **传输新二进制**：`scp /tmp/hydra-linux omarchy:~/.local/bin/hydra`
- [ ] **重启服务**：`ssh omarchy 'systemctl --user restart hydra.service'`
- [ ] **验证迁移**：`ssh omarchy 'sqlite3 ~/.config/hydra/hydra.db "PRAGMA user_version;"'`（应为 9）
- [ ] **验证权限**：`ssh omarchy 'stat -c %a ~/.config/hydra/hydra.db'`（应为 600）
- [ ] **验证鉴权**：`ssh omarchy 'curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18045/metrics'`（应为 401）
- [ ] **手动启用账号**：`ssh omarchy '~/.local/bin/hydra accounts enable 2 && ~/.local/bin/hydra accounts enable 3'`
- [ ] **验证账号恢复**：等待 120s 后 `ssh omarchy '~/.local/bin/hydra status'`

---

## 7. 未完成项

### P2-B: TUI 重构（未开始）
- TUI 仪表盘仍使用旧代码
- `--no-color` flag 已声明但 TUI 未消费
- `--quiet` flag 已声明但未实现

### P2-C: 健康端点（未开始）
- `/readyz` 未实现（返回 404）
- `/livez` 未实现（返回 404）
- `/healthz` 仅返回 "ok"，不检查 DB/账号状态

### P2-D: 运维辅助命令经过 service（部分完成）
- 已完成：accounts remove/enable/disable, key add/remove/enable/disable/rotate/update
- 未完成：accounts add (OAuth), accounts refresh, key show, quota, config show/init
- 这些命令仍直接访问 DB 且返回 raw error（不经过 AsAppError）
- 影响：not-found 返回 INTERNAL 而非 NOT_FOUND，错误信息可能泄漏内部细节
- 不阻塞封版，属于 P2 后续

### 已知脆弱点（不阻塞封版）
- `isUsageError()` 用字符串匹配判断 Cobra usage error，理论上可能误判
- `AsAppError` 在 `errors.As` 之前执行，AppError 不会被误判
- 只有未经过 `AsAppError` 的 raw error 才可能误判，且这些 error 的 message 不包含匹配 pattern

---

## 8. 建议 tag

```
v0.5.0
```

**理由**：
- v0.4.x 系列是 pre-P2-A 的版本
- P2-A 引入了 application service 层、typed error/DTO、JSON output、CLI 契约统一
- 这是架构层面的 breaking change（CLI 接口、错误码、输出格式），符合 minor 版本升级
- commit: `db75385401b5f8fb3871dcc9765e259c21d3393c`

**未打 tag，等待 master 批准。**
