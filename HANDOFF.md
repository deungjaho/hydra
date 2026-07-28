# Hydra 阶段交接文档

**最后更新**: 2026-07-28 21:45
**HEAD**: `db75385` P2-A.1: CLI contract closure
**建议 source-stage tag**: `v0.5.0-alpha.1` (未打)

---

## 1. 本地 VERIFIED

以下项在 MacBook 本地源码 `db75385` 上验证通过：

| 维度 | 结果 | 命令 |
|------|------|------|
| Git clean | clean | `git status -s` |
| gofmt | 无 diff | `gofmt -l .` |
| go vet | 通过 | `go vet ./...` |
| go build (macOS) | 通过 | `go build -o /tmp/hydra-verify ./cmd/hydra` |
| cross build (Linux amd64) | 通过，静态链接 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/hydra-linux ./cmd/hydra` |
| go test -race | 全 7 包通过 | `go test ./... -race -timeout 120s -count=1` |
| 测试函数数 | 224 | `grep -rn "func Test" --include="*_test.go" \| wc -l` |
| shutdown race (3x) | 稳定 | `go test ./internal/proxy/ -race -run "Shutdown\|Drain\|Loop" -count=3` |

### 二进制构建兼容性

- SQLite 驱动: `modernc.org/sqlite v1.54.0` — 纯 Go 实现，**无 CGO 依赖**
- 交叉编译: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build` 生成静态链接 ELF，目标架构 x86-64
- omarchy: Arch Linux, kernel 7.1.3, x86-64 — 兼容
- 无动态库依赖，不需要目标机器安装任何 native 库

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

**部署未验证**：以下 GAP 基于源码审查和 omarchy 只读探测，新二进制从未在 omarchy 上运行过。

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

### 生产环境实测结果 (2026-07-28 21:00，只读探测)

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
- 迁移前备份 DB（必须包含 WAL checkpoint 后的一致性快照）
- 确保迁移过程中不强制 kill 进程

### 风险：新代码 enable 不写 legacy disabled 列

新代码 `SetAccountDisabled` 只写 `operator_disabled` 列：
```sql
UPDATE accounts SET operator_disabled = ? WHERE id = ?
```
旧代码 `SetAccountDisabled` 只写 `disabled` 列：
```sql
UPDATE accounts SET disabled = ? WHERE id = ?
```

迁移后用新代码 `hydra accounts enable 2` 会设置 `operator_disabled=0`，但 `disabled` 列仍为 1。如果回退到旧二进制，旧代码读 `disabled=1` → 账号仍 disabled。详见下方 Rollback 方案 A 的状态语义退化说明。

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

**注意**：步骤 2 只设置 `operator_disabled=0`，不修改 legacy `disabled` 列。如果后续回退到旧二进制，需用旧二进制的 `hydra accounts enable 2/3` 重新设置 `disabled=0`。

---

## 5. Rollback 方案

### 方案 A：回退二进制（进程兼容，状态语义可能退化）

旧二进制可以打开迁移后的 DB（新列被忽略，`user_version=9` 不影响旧代码），但**状态语义可能退化**：

```bash
# 1. 停止新二进制
systemctl --user stop hydra.service

# 2. 恢复旧二进制
cp ~/.local/bin/hydra.bak ~/.local/bin/hydra

# 3. 重启
systemctl --user start hydra.service

# 4. 验证进程启动
curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18045/healthz  # 应返回 200
```

**状态语义退化**：

| 场景 | 迁移后状态 | 回退后旧代码看到 | 影响 |
|------|-----------|-----------------|------|
| 账号被新代码 enable | `operator_disabled=0, disabled=1` | `disabled=1` → 仍 disabled | 账号不参与 pool |
| 账号被新代码 disable | `operator_disabled=1, disabled=1` | `disabled=1` → disabled | 一致 |
| 账号被新代码 health-disable | `health_disabled=1, disabled=1` | `disabled=1` → disabled | 一致（但旧代码无 health 恢复） |
| 账号被新代码 health-recover | `health_disabled=0, operator_disabled=0, disabled=1` | `disabled=1` → 仍 disabled | **账号不恢复** |

**回退后必须用旧二进制重新启用账号**：
```bash
~/.local/bin/hydra accounts enable 2   # 旧代码写 disabled=0
~/.local/bin/hydra accounts enable 3
```

**不能描述为"安全回退"**：进程可以启动，但用新代码做过的 enable/health-recover 操作在旧代码下不可见，账号可能保持 disabled 状态。

### 方案 B：回退 DB + 二进制（仅紧急情况）

**前提**：`hydra.db.bak` 必须是部署前的一致性备份（迁移前、WAL checkpoint 后）。

```bash
# 1. 停止服务（必须先停，否则 WAL 会写回主 DB）
systemctl --user stop hydra.service

# 2. 确认进程已退出
pgrep -af hydra  # 应无输出

# 3. 删除迁移后的 WAL/SHM（它们包含迁移写入，不能混用旧备份）
rm -f ~/.config/hydra/hydra.db-wal ~/.config/hydra/hydra.db-shm

# 4. 恢复部署前的一致性备份
cp ~/.config/hydra/hydra.db.bak ~/.config/hydra/hydra.db

# 5. 恢复旧二进制
cp ~/.local/bin/hydra.bak ~/.local/bin/hydra

# 6. 重启
systemctl --user start hydra.service

# 7. 验证
sqlite3 ~/.config/hydra/hydra.db "PRAGMA user_version;"  # 应为 0
curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:18045/healthz  # 应返回 200
```

**注意**：
- 步骤 3 必须在步骤 4 之前执行 — 迁移后的 WAL/SHM 包含 v8/v9 schema 变更，与旧 DB 主文件不兼容
- 不能用迁移后的 DB 备份回退 — `user_version=9` 和新列已经写入，旧代码虽然能运行但状态语义已变
- 备份必须是部署前创建的，不能用迁移后的快照

---

## 6. 部署前检查清单

部署新二进制前，需要 master 明确批准以下操作：

- [ ] **构建 Linux 二进制**：
      `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /tmp/hydra-linux ./cmd/hydra`
      （纯 Go，无 CGO，静态链接，已在 MacBook 验证交叉编译成功）
- [ ] **备份旧二进制**：`ssh omarchy 'cp ~/.local/bin/hydra ~/.local/bin/hydra.bak'`
- [ ] **一致性备份 DB**（必须停服务后备份，确保 WAL 已 checkpoint）：
      `ssh omarchy 'systemctl --user stop hydra.service && sqlite3 ~/.config/hydra/hydra.db ".backup ~/.config/hydra/hydra.db.bak" && systemctl --user start hydra.service'`
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

## 8. 建议 source-stage tag

```
v0.5.0-alpha.1
```

**理由**：
- 本地源码验证通过（format/vet/race/build/224 测试），可作为 source-stage 里程碑
- **不是稳定版本**：部署未验证，旧 DB 迁移有 backfill 语义风险，在线账号全 disabled 未恢复
- v0.4.x 系列是 pre-P2-A 的已部署版本；P2-A 引入架构变更（application service、typed error/DTO、JSON output、CLI 契约统一）
- alpha.1 表示源码阶段完成但部署验证未完成，后续部署验证通过后再考虑 v0.5.0
- commit: `db75385401b5f8fb3871dcc9765e259c21d3393c`

**未打 tag，等待 master 批准。**
