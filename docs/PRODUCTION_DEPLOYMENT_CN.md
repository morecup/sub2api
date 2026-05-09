# 线上部署信息

本文记录当前项目在 SSH MCP 台式机服务器上的实际部署信息和日常运维命令。

> 注意：本文只记录运维入口和非敏感配置。数据库密码、JWT Secret、TOTP/支付加密密钥等敏感信息只保存在服务器配置文件中，不写入仓库文档。

## 部署概览

| 项目 | 当前值 |
| --- | --- |
| SSH MCP 服务器 | `家庭台式机` |
| 服务器地址 | `192.168.124.24` |
| 部署根目录 | `/large/sub2api` |
| 应用访问地址 | `http://192.168.124.24:8080/` |
| 健康检查地址 | `http://192.168.124.24:8080/health` |
| 应用版本 | `Sub2API 0.1.119` |
| 构建标识 | `commit: local-deploy` |
| 源码快照 | `/large/sub2api/source/current` |
| 当前源码提交 | `c056db74` |

当前部署采用本地构建 Linux 二进制、服务器 systemd 托管的方式。服务器未使用 Docker 部署 Sub2API。

## 目录结构

| 路径 | 用途 |
| --- | --- |
| `/large/sub2api/app/sub2api` | Sub2API Linux 二进制 |
| `/large/sub2api/app/resources/model-pricing/model_prices_and_context_window.json` | 模型价格 fallback 数据 |
| `/large/sub2api/data/config.yaml` | 线上应用配置，包含敏感信息，不要复制到仓库 |
| `/large/sub2api/data/logs/sub2api.log` | 应用文件日志 |
| `/large/sub2api/data/model_pricing.json` | 运行时模型价格数据 |
| `/large/sub2api/redis/redis.conf` | Redis 专用配置 |
| `/large/sub2api/redis/data` | Redis 持久化数据目录 |
| `/large/sub2api/source/current` | 当前部署源码快照 |
| `/large/sub2api/backups` | 部署前/运维备份目录 |

## Systemd 服务

| 服务 | 用途 | 开机自启 | 当前状态 |
| --- | --- | --- | --- |
| `sub2api.service` | Sub2API 主服务 | `enabled` | `active` |
| `sub2api-redis.service` | Sub2API 专用 Redis | `enabled` | `active` |
| `postgresql.service` | PostgreSQL 数据库 | `enabled` | `active` |

常用命令：

```bash
systemctl status sub2api.service --no-pager -l
systemctl status sub2api-redis.service --no-pager -l
systemctl status postgresql.service --no-pager -l

systemctl restart sub2api.service
systemctl restart sub2api-redis.service

systemctl is-enabled sub2api.service sub2api-redis.service postgresql.service
systemctl is-active sub2api.service sub2api-redis.service postgresql.service
```

## 运行端口

| 服务 | 监听地址 | 说明 |
| --- | --- | --- |
| Sub2API | `0.0.0.0:8080` | 对局域网开放 Web UI 和 API |
| Redis | `127.0.0.1:6379`、`::1:6379` | 仅本机访问 |
| PostgreSQL | `0.0.0.0:5432`、`:::5432` | 由服务器已有 PostgreSQL 提供 |

检查命令：

```bash
ss -ltnp | grep -E ':(8080|6379|5432)\b'
curl -fsS http://127.0.0.1:8080/health
curl -fsS http://192.168.124.24:8080/health
redis-cli -h 127.0.0.1 -p 6379 ping
```

健康检查正常时，Sub2API 返回：

```json
{"status":"ok"}
```

## 数据库与 Redis

PostgreSQL 使用服务器现有实例：

| 项目 | 当前值 |
| --- | --- |
| Host | `192.168.124.24` |
| Port | `5432` |
| Database | `sub2api` |
| User | `postgres` |
| 迁移记录表 | `schema_migrations` |

密码在 `/large/sub2api/data/config.yaml` 中，仅服务器本地保存。

迁移状态检查：

```bash
sudo -u postgres psql -d sub2api -tAc "SELECT COUNT(*) FROM schema_migrations;"
sudo -u postgres psql -d sub2api -tAc "SELECT filename FROM schema_migrations ORDER BY filename DESC LIMIT 5;"
```

当前部署时记录到的迁移状态：

```text
schema_migrations count: 163
latest migration: 133_affiliate_rebate_freeze.sql
```

Redis 使用 `sub2api-redis.service` 独立托管，配置文件为 `/large/sub2api/redis/redis.conf`，持久化目录为 `/large/sub2api/redis/data`。已设置 `vm.overcommit_memory = 1`，配置文件位于 `/etc/sysctl.d/99-sub2api-redis.conf`。

## 日志与排障

查看应用日志：

```bash
journalctl -u sub2api.service -f
journalctl -u sub2api.service -n 200 --no-pager
tail -f /large/sub2api/data/logs/sub2api.log
```

查看 Redis 日志：

```bash
journalctl -u sub2api-redis.service -f
```

确认服务环境变量：

```bash
pid=$(systemctl show -p MainPID --value sub2api.service)
tr '\0' '\n' < /proc/$pid/environ | grep -E '^(DATA_DIR|GIN_MODE|SERVER_HOST|SERVER_PORT|TZ)='
readlink /proc/$pid/cwd
```

当前主服务关键环境变量：

```text
DATA_DIR=/large/sub2api/data
GIN_MODE=release
SERVER_HOST=0.0.0.0
SERVER_PORT=8080
TZ=Asia/Shanghai
WorkingDirectory=/large/sub2api/app
```

## 备份

首次部署启动前已创建 PostgreSQL 备份：

```text
/large/sub2api/backups/sub2api_pg_20260426T115548Z.sql.gz
```

手动创建 PostgreSQL 备份示例：

```bash
install -d -m 700 /large/sub2api/backups
backup="/large/sub2api/backups/sub2api_pg_$(date -u +%Y%m%dT%H%M%SZ).sql.gz"
PGPASSWORD='<从 /large/sub2api/data/config.yaml 获取>' \
  pg_dump -h 192.168.124.24 -p 5432 -U postgres -d sub2api | gzip -9 > "$backup"
chmod 600 "$backup"
ls -lh "$backup"
```

Redis 数据已落在 `/large/sub2api/redis/data`，如需完整迁移，可在停服务后打包 `/large/sub2api`。

## 更新流程

推荐更新前先备份数据库，然后替换二进制并重启服务。

本地构建前端：

```bash
pnpm --dir frontend install --frozen-lockfile
pnpm --dir frontend run build
```

本地交叉编译 Linux 二进制示例：

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
  -C backend \
  -tags embed \
  -ldflags "-s -w -X main.BuildType=release" \
  -trimpath \
  -o ../sub2api-linux-amd64 \
  ./cmd/server
```

上传并切换二进制：

```bash
systemctl stop sub2api.service
cp /large/sub2api/app/sub2api /large/sub2api/app/sub2api.bak.$(date -u +%Y%m%dT%H%M%SZ)
install -m 755 -o sub2api -g sub2api /path/to/new/sub2api-linux-amd64 /large/sub2api/app/sub2api
systemctl start sub2api.service
curl -fsS http://127.0.0.1:8080/health
```

如需同步源码快照，可将当前提交打包并解压到 `/large/sub2api/source/current`。

## 已知提醒

- 服务器根分区 `/` 使用率较高，部署数据已放到 `/large`，不要把运行数据写到根分区。
- `/large` 当前空间充足，部署时约有 `434G` 可用。
- `ufw` 当前为 inactive，8080 可在局域网访问。
- 日志中提示 `TOTP_ENCRYPTION_KEY` 未固定；如果启用 2FA 或支付恢复 token，建议在配置中固定该密钥。
- 日志中提示 `security.url_allowlist.enabled=false`、CORS 未配置；如对公网开放，建议补齐安全配置。
