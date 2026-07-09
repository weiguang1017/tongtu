# 通途 TongTu

> **天堑变通途 —— 家宽即服务器。**
> *Your home broadband, served to the world.*

「通途」是一个极简的内网穿透工具:把家里任何一个本地端口,
通过你自己的域名(自动 HTTPS)暴露到公网。**纯客户端,无需自建服务器** ——
公网接入、TLS 证书、DNS 全部由 Cloudflare 免费承担。

**图形界面,开箱即用**:直接运行 `tongtu`(不带任何参数)即启动图形管理界面并自动打开浏览器,
首次使用有三步向导引导完成全部配置,cloudflared 连接器可一键安装。

```
tongtu          # 启动图形界面:向导式配置 → 一键启动隧道 → 实时日志
```

偏好命令行的用户,所有能力也都有对应子命令:

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
2. **API Token**:创建一个自定义 Token,授予两个权限:
   - **Cloudflare Tunnel → Edit**(Account 作用域;新版面板里显示为 *Argo Tunnel (Legacy)*,是同一权限)
   - **DNS → Edit**(Zone 作用域;注意别选成 *DNS Settings* 或 *DNS Firewall*,要选 **DNS**)

   两类令牌通途都支持:
   - **用户令牌**(`cfut_` 前缀):在 <https://dash.cloudflare.com/profile/api-tokens> 创建;
   - **账户令牌**(`cfat_` 前缀):在 `dash.cloudflare.com/<账户ID>/api-tokens` 创建,
     Cloudflare 推荐用于长期运行的服务集成(正是通途的场景)。
3. **cloudflared**:
   ```bash
   brew install cloudflared        # macOS
   # Linux/Windows 见 https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/
   ```
   或者不装也行:图形界面里点「一键安装」,或 `tongtu run --auto-install`,
   都会自动下载官方二进制到 `~/.tongtu/bin/`。国内直连 GitHub 受限时,可在图形界面
   「概览 → ⚙ 下载设置」填本机代理(如 `http://127.0.0.1:7890`),下载即走该代理;
   或直接 `brew install cloudflared`。

## 安装

### 一键安装(Linux / macOS,推荐)

```bash
curl -fsSL https://raw.githubusercontent.com/weiguang1017/tongtu/main/install.sh | sh
```

脚本会自动识别系统与架构,下载最新 Release、校验 SHA256 并安装到 `/usr/local/bin`
(需要时会请求 sudo)。可用环境变量定制:

```bash
TONGTU_VERSION=v0.0.2 sh install.sh                 # 指定版本
TONGTU_INSTALL_DIR=~/.local/bin sh install.sh      # 自定义安装目录
https_proxy=http://127.0.0.1:7890 sh install.sh    # 国内网络走代理下载
```

### 手动下载

到 [Releases](https://github.com/weiguang1017/tongtu/releases) 页面下载对应系统与架构的压缩包,解压后:

```bash
tar -xzf tongtu_v0.0.2_darwin_arm64.tar.gz
cd tongtu_v0.0.2_darwin_arm64
./tongtu        # 注意要加 ./ —— 当前目录默认不在 PATH 里,直接敲 tongtu 会报 command not found
```

想在任意目录直接使用,把二进制移到 PATH 里的目录即可:

```bash
sudo mv tongtu /usr/local/bin/
```

> **macOS 提示**:用浏览器下载的二进制带有 quarantine 隔离标记,首次运行可能提示
> "无法验证开发者"。执行 `xattr -d com.apple.quarantine ./tongtu` 清除即可;
> 用 `curl` 下载或一键安装脚本则不会遇到。

Windows 用户下载 zip 包解压后,在解压目录运行 `.\tongtu.exe`,或将其所在目录加入 `Path` 环境变量。

## 构建

```bash
make build     # 产出 bin/tongtu
make linux     # 交叉编译 bin/tongtu-linux-amd64 / arm64
make vet
```

## 自动构建与发布

GitHub Actions 会在推送代码或提交 PR 时自动执行测试、`go vet` 和编译。
推送 `v*` 标签会自动生成 Linux / macOS / Windows 的 amd64、arm64 安装包,
并发布到 GitHub Release:

```bash
git tag v0.1.0
git push origin v0.1.0
```

## 快速上手(图形界面,推荐)

```bash
tongtu
```

直接运行即启动本地图形界面并自动打开浏览器(默认 `http://127.0.0.1:7080`,仅本机可访问):

- **新手向导**:首次使用自动弹出,三步走完 凭证 → 域名 → 应用,域名列表直接从你的
  Cloudflare 账号读取,点选即可;
- **概览页**:隧道运行状态、cloudflared 连接器状态(未安装可一键安装)、配置概况一目了然;
- **应用 / 域名 / 凭证页**:全部增删改在弹窗里完成,应用支持启停切换与高级选项
  (协议、源站 TLS);
- **运行日志页**:通途与 cloudflared 的实时输出,排查问题不用回终端。

## 快速上手(命令行)

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
| `tongtu`(无参数) | 启动图形管理界面并自动打开浏览器 |
| `tongtu web [--addr 127.0.0.1:7080] [--open]` | 启动图形界面(`--open` 自动打开浏览器) |

`app add / update` 参数:

| 参数 | 说明 | 默认 |
|------|------|------|
| `--domain` | 对外完整域名,如 blog.example.com,须属于已登记根域名 | — |
| `--local` | 本地服务地址 | — |
| `--proto` | 本地服务协议 http / https / tcp | `http` |
| `--no-tls-verify` | 本地服务为自签 HTTPS 证书时跳过校验 | 关 |
| `--origin-server-name` | 源站 TLS 握手 SNI(证书域名与访问域名不一致时) | — |
| `--disable` | 添加后暂不启用 | 关 |

## 图形界面

```bash
tongtu                  # 等价于 tongtu web --open
tongtu web              # 只启动服务,不自动开浏览器
```

浏览器里完成凭证 / 域名 / 应用的增删改、一键启停隧道、cloudflared 一键安装与实时日志查看。
默认只监听本机(`127.0.0.1:7080`);监听非本机地址时必须同时设置访问令牌:

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
