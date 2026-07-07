# 通途 TongTu

> **天堑变通途 —— 家宽即服务器。**
> *Your home broadband, served to the world.*

「通途」是一个极简的内网穿透工具:一条命令,把家里任何一个本地端口,
通过你自己的域名(HTTP + HTTPS)暴露到公网 —— 就像
`https://yilutongxing.restartx.top` 那样,子域名即开即用,客户端零证书配置。

```
tongtu -server tunnel.example.com:7000 -token xxx -name blog -local 127.0.0.1:8080
✓ 通途已就绪: https://blog.example.com -> 127.0.0.1:8080
```

## 命名与 SLOGAN

- **名称**:通途(TongTu)。取自"一桥飞架南北,天堑变通途"——家庭内网与公网之间
  隔着 NAT、动态 IP、封禁的 80/443 端口,这就是"天堑";通途负责架桥。
- **SLOGAN**:**天堑变通途,家宽即服务器。**
- 二进制命名:客户端 `tongtu`(日常敲的命令,短),服务端 `tongtud`(daemon 惯例)。

## 工作原理

```
 访客浏览器                     公网服务器 (tongtud)                家里 (tongtu)
──────────────                ──────────────────────              ────────────────
https://blog.example.com ──▶  :443 泛域名证书卸载 TLS
                              按子域名 blog 找到隧道 ──accept──▶  控制连接(客户端外连,
                                                                 无需公网 IP / 端口映射)
                              ◀───────── 数据连接(回拨)──────────
                              字节流双向透传          ──────────▶  127.0.0.1:8080 本地服务
```

- 客户端**主动外连**服务端,家里不需要公网 IP、不需要路由器端口映射;
- HTTPS 由服务端用 `*.example.com` 泛域名证书统一卸载,客户端和本地服务零证书配置;
- 每个公网请求按需回拨一条数据连接,支持 WebSocket / SSE / 长连接;
- 纯 Go 标准库实现,零第三方依赖,单二进制跨平台。

## 构建

```bash
go build -o bin/tongtu  ./cmd/tongtu    # 客户端(家里)
go build -o bin/tongtud ./cmd/tongtud   # 服务端(公网服务器)

# 交叉编译示例:给 Linux 服务器编服务端
GOOS=linux GOARCH=amd64 go build -o bin/tongtud-linux ./cmd/tongtud
```

## 服务端部署(一次性,约 10 分钟)

前提:一台有公网 IP 的服务器 + 一个域名(下文以 `example.com` 为例)。

1. **DNS**:添加两条 A 记录指向服务器公网 IP:
   - `tunnel.example.com`(客户端接入用)
   - `*.example.com`(访客访问用;若不想占整个根域,可用 `*.t.example.com`,
     则服务端 `-domain t.example.com`)

2. **泛域名证书**(Let's Encrypt,DNS 验证,自动续期)——以 acme.sh + Cloudflare 为例:

   ```bash
   curl https://get.acme.sh | sh
   export CF_Token="你的CloudflareAPI令牌"
   acme.sh --issue --dns dns_cf -d example.com -d '*.example.com'
   acme.sh --install-cert -d example.com \
     --fullchain-file /etc/tongtu/fullchain.pem \
     --key-file       /etc/tongtu/privkey.pem \
     --reloadcmd      "systemctl restart tongtud"
   ```

3. **启动服务端**:

   ```bash
   tongtud -domain example.com -token 你的令牌 \
     -tls-cert /etc/tongtu/fullchain.pem -tls-key /etc/tongtu/privkey.pem
   ```

   systemd 常驻见 [`deploy/tongtud.service`](deploy/tongtud.service);
   防火墙放行 TCP 80 / 443 / 7000。

## 客户端使用(家里,每次一条命令)

```bash
# 把本地 8080 暴露为 blog.example.com
tongtu -server tunnel.example.com:7000 -token 你的令牌 -name blog -local 127.0.0.1:8080

# 令牌也可放环境变量,命令更短
export TONGTU_TOKEN=你的令牌
tongtu -server tunnel.example.com:7000 -name nas -local 192.168.1.10:5000
```

- 断线自动重连(指数退避),放心挂机;
- macOS 开机自启见 [`deploy/com.tongtu.client.plist`](deploy/com.tongtu.client.plist)。

## 本机快速自测(不用域名)

```bash
# 终端 1:起一个本地服务端,假装 lvh.me 是你的域名(lvh.me 全部解析到 127.0.0.1)
go run ./cmd/tongtud -domain lvh.me -http :8080 -control :7000 -token dev

# 终端 2:随便起个本地服务
python3 -m http.server 3000

# 终端 3:客户端
go run ./cmd/tongtu -server 127.0.0.1:7000 -token dev -name demo -local 127.0.0.1:3000

# 验证
curl http://demo.lvh.me:8080/
```

## 安全须知

- 一定要设置 `-token`,否则任何人都能在你的域名下注册子域名;
- 客户端与服务端之间的控制/数据通道目前是明文 TCP(令牌可被链路上的人截获),
  建议仅在可信链路使用,或在前面套一层 TLS(见路线图);
- 暴露到公网的本地服务自身要有认证 —— 通途只负责"能访问",不负责"该不该访问"。

## 路线图

- [ ] 控制/数据通道 TLS 加密(复用泛域名证书)
- [ ] 单控制连接上多路复用(yamux),减少回拨延迟
- [ ] TCP/UDP 任意端口转发(不限于 HTTP)
- [ ] 客户端 Web 面板与流量统计
