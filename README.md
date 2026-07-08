# 通途 TongTu

> **天堑变通途 —— 家宽即服务器。**
> *Your home broadband, served to the world.*

「通途」是一个极简的内网穿透工具:把家里任何一个本地端口,
通过你自己的域名(自动 HTTPS)暴露到公网。**纯客户端,无需自建服务器** ——
公网接入、TLS 证书、DNS 全部由 Cloudflare 免费承担。

```
tongtu app add blog --domain blog.example.com --local 127.0.0.1:8080
tongtu run
✓ 通途已就绪: https://blog.example.com -> http://127.0.0.1:8080
```

## 命名与 SLOGAN

- **名称**:通途(TongTu)。取自"一桥飞架南北,天堑变通途"——家庭内网与公网之间
  隔着 NAT、动态 IP、封禁的 80/443 端口,这就是"天堑";通途负责架桥。
- **SLOGAN**:**天堑变通途,家宽即服务器。**

## 工作原理

```
 访客浏览器                Cloudflare 边缘                     家里 (tongtu)
──────────────           ──────────────────                 ─────────────────
https://blog.example.com ──▶ 全球边缘节点,自动 TLS
                             CNAME blog → <id>.cfargotunnel.com
                                    │
                                    ▼
                             Cloudflare Tunnel  ◀──外连──  cloudflared 子进程
                                                            (tongtu 自动托管)
                                                                 │
                                                                 ▼
                                                            127.0.0.1:8080 本地服务
```

tongtu 本身只做**编排**(纯 Go 标准库,零第三方依赖):

1. 调 Cloudflare API 创建(或复用)一条 Cloudflare Tunnel(每个凭证一条,所有应用共享);
2. 写入 ingress 规则:每个应用一条,如 `blog.example.com → http://127.0.0.1:8080`;
3. 创建 DNS CNAME:`blog → <tunnel_id>.cfargotunnel.com`(代理开启,TLS 自动签发);
4. 启动并托管 `cloudflared` 子进程维持隧道连接(意外退出自动重启,指数退避)。

- cloudflared **主动外连** Cloudflare,家里不需要公网 IP、不需要路由器端口映射;
- HTTPS 由 Cloudflare 边缘统一卸载,本地服务零证书配置;
- 原生支持 WebSocket / SSE / 长连接(HTTP/2 + QUIC);
- Cloudflare Tunnel 免费、不限流量。

## 前提条件(一次性,约 5 分钟)

1. **域名托管在 Cloudflare**:有一个自己的域名,NS 已指向 Cloudflare(免费套餐即可)。
2. **API Token**:在 <https://dash.cloudflare.com/profile/api-tokens> 创建自定义 Token,
   授予两个权限:
   - **Account → Cloudflare Tunnel → Edit**
   - **Zone → DNS → Edit**(选择你的域名对应的 Zone)
3. **cloudflared**:
   ```bash
   brew install cloudflared        # macOS
   # Linux/Windows 见 https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
   ```
   或者不装也行,`tongtu run --auto-install` 会自动下载官方二进制到 `~/.tongtu/bin/`。

## 构建

```bash
make build     # 产出 bin/tongtu
make linux     # 交叉编译 bin/tongtu-linux-amd64 / arm64
make vet
```

## 快速上手

```bash
# 1. 保存凭证(在线验证后存入 ~/.tongtu/config.json,权限 0600)
tongtu cred add mycf --token <你的 API Token>

# 2. 登记域名(验证该域名确实托管在你的 Cloudflare 账号下)
tongtu domain add example.com

# 3. 添加应用(可多个,共享一条隧道)
tongtu app add blog --domain blog.example.com --local 127.0.0.1:8080
tongtu app add nas  --domain nas.example.com  --local 192.168.1.10:5000

# 4. 运行全部已启用应用(前台阻塞,Ctrl-C 退出)
tongtu run
```

## 命令一览

| 命令 | 说明 |
|------|------|
| `tongtu cred add <别名> --token <T>` | 添加凭证(在线验证 Token) |
| `tongtu cred list` | 列出凭证(Token 打码显示) |
| `tongtu cred update <别名> --token <T>` | 更换 Token |
| `tongtu cred rm <别名>` | 删除凭证(被域名引用时拒绝) |
| `tongtu domain add <根域名> [--cred X]` | 登记域名(验证 Zone 与权限) |
| `tongtu domain list` | 列出已登记域名 |
| `tongtu domain rm <根域名>` | 移除登记(不动 Cloudflare 上的 Zone;被应用引用时拒绝) |
| `tongtu zones [--cred X]` | 列出凭证可见的全部可用域名 |
| `tongtu app add <名> --domain <FQDN> --local <地址>` | 添加应用(见下方参数) |
| `tongtu app list` | 列出应用 |
| `tongtu app update <名> [参数...]` | 修改应用 |
| `tongtu app enable/disable <名>` | 启用 / 停用 |
| `tongtu app rm <名> [--keep-dns]` | 删除应用(默认连 DNS 记录一起删) |
| `tongtu run [应用名...]` | 同步 Cloudflare 配置并运行(默认全部已启用应用) |
| `tongtu status` | 查看各应用 DNS / 隧道就绪状态 |
| `tongtu web [--addr 127.0.0.1:7080]` | 本地 Web 管理面板 |

`app add / update` 参数:

| 参数 | 说明 | 默认 |
|------|------|------|
| `--domain` | 对外完整域名,如 blog.example.com,须属于已登记根域名 | — |
| `--local` | 本地服务地址 | — |
| `--proto` | 本地服务协议 http / https / tcp | `http` |
| `--no-tls-verify` | 本地服务为自签 HTTPS 证书时跳过校验 | 关 |
| `--origin-server-name` | 源站 TLS 握手 SNI(证书域名与访问域名不一致时) | — |
| `--disable` | 添加后暂不启用 | 关 |

## Web 管理面板

```bash
tongtu web     # 打开 http://127.0.0.1:7080
```

浏览器里完成凭证 / 域名 / 应用的增删改与一键启停。默认只监听本机;
监听非本机地址时必须同时设置访问令牌:

```bash
tongtu web --addr 0.0.0.0:7080 --web-token <随机字符串>
# API 请求需带请求头 Authorization: Bearer <令牌>
```

## 快捷模式(不落配置)

临时暴露单个端口,兼容旧用法:

```bash
export TONGTU_CF_TOKEN=<Token> TONGTU_DOMAIN=example.com
tongtu -name demo -local 127.0.0.1:3000 -cleanup   # -cleanup: 退出时删除隧道与 DNS
```

## SSL / 证书说明

- **访客侧 HTTPS**:证书由 Cloudflare 边缘自动签发、自动续期,零配置;
- **源站 TLS**(本地服务本身是 HTTPS 时):`--proto https` 连接本地服务;
  自签证书加 `--no-tls-verify`;证书域名与访问域名不一致时用 `--origin-server-name` 指定 SNI。

## 开机自启(macOS)

先用 CLI 完成配置,再安装 LaunchAgent(开机执行 `tongtu run`):
见 [`deploy/com.tongtu.client.plist`](deploy/com.tongtu.client.plist)。

## 安全须知

- **API Token 就是钥匙**:它能改你的 DNS 和隧道,请按最小权限创建并限定到具体 Zone;
  `~/.tongtu/config.json` 保存着 Token(0600 权限),不要提交进仓库或同步到不可信设备;
- 通往 Cloudflare 的隧道链路全程加密(cloudflared ↔ CF 边缘为 TLS);
- 暴露到公网的本地服务自身要有认证 —— 通途只负责"能访问",不负责"该不该访问";
  需要访问控制可在 Cloudflare Zero Trust 控制台给对应 hostname 加 Access 策略;
- Web 面板监听非本机地址时强制要求 `--web-token`;
- 注意:流量会经过 Cloudflare(其边缘终止 TLS),对 Cloudflare 不可见的端到端加密
  不在本工具范围内。

## 路线图

- [ ] `tongtu status` 显示 cloudflared 实时连接状态与流量统计
- [ ] TCP 任意端口转发的访客侧引导(`cloudflared access tcp`)
- [ ] TryCloudflare 免域名快速模式(随机 `*.trycloudflare.com` 地址)
- [ ] 配置导入 / 导出与多机同步
