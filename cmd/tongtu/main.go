// tongtu 是「通途」的客户端,跑在家里的机器上。
//
// 一条命令把本地端口暴露到公网域名:
//
//	tongtu -server tunnel.example.com:7000 -token xxx -name blog -local 127.0.0.1:8080
//
// 成功后 http(s)://blog.example.com 即指向本地 8080。
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"tongtu/internal/proto"
)

// errRejected 表示服务端明确拒绝了注册(令牌错/子域名冲突),重试无意义。
var errRejected = errors.New("注册被拒绝")

type client struct {
	server string // 服务端控制地址 host:port
	token  string
	name   string // 期望的子域名
	local  string // 本地服务地址 host:port
}

func main() {
	var c client
	flag.StringVar(&c.server, "server", "", "服务端地址,如 tunnel.example.com:7000(必填)")
	flag.StringVar(&c.token, "token", "", "接入令牌(也可用环境变量 TONGTU_TOKEN)")
	flag.StringVar(&c.name, "name", "", "子域名,如 blog(必填)")
	flag.StringVar(&c.local, "local", "127.0.0.1:8080", "要暴露的本地服务地址")
	flag.Parse()

	if c.token == "" {
		c.token = os.Getenv("TONGTU_TOKEN")
	}
	if c.server == "" || c.name == "" {
		flag.Usage()
		os.Exit(2)
	}

	// 断线自动重连,指数退避,上限 30s。
	backoff := time.Second
	for {
		err := c.run()
		if errors.Is(err, errRejected) {
			log.Fatalf("%v(令牌错误或子域名被占用,不再重试)", err)
		}
		log.Printf("连接断开: %v,%s 后重连", err, backoff)
		time.Sleep(backoff)
		if backoff *= 2; backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

// run 建立控制连接并服务到出错为止。
func (c *client) run() error {
	ctrl, err := net.DialTimeout("tcp", c.server, 10*time.Second)
	if err != nil {
		return fmt.Errorf("连接服务端: %w", err)
	}
	defer ctrl.Close()

	if err := proto.WriteMsg(ctrl, &proto.Msg{
		Type: proto.TypeRegister, Subdomain: c.name, Token: c.token,
	}); err != nil {
		return fmt.Errorf("发送注册: %w", err)
	}

	r := bufio.NewReader(ctrl)
	msg, err := proto.ReadMsgTimeout(ctrl, r, 10*time.Second)
	if err != nil {
		return fmt.Errorf("等待注册结果: %w", err)
	}
	switch msg.Type {
	case proto.TypeRegistered:
		log.Printf("✓ 通途已就绪: https://%s -> %s", msg.Domain, c.local)
	case proto.TypeError:
		return fmt.Errorf("%w: %s", errRejected, msg.Message)
	default:
		return fmt.Errorf("意外的服务端消息: %s", msg.Type)
	}

	// 心跳:既保活 NAT 映射,也让服务端及时发现客户端下线。
	var wmu sync.Mutex // 心跳与(未来的)其他控制写共用一把锁
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				wmu.Lock()
				err := proto.WriteMsg(ctrl, &proto.Msg{Type: proto.TypePing})
				wmu.Unlock()
				if err != nil {
					ctrl.Close() // 让读循环退出
					return
				}
			}
		}
	}()

	for {
		ctrl.SetReadDeadline(time.Now().Add(90 * time.Second)) //nolint:errcheck
		m, err := proto.ReadMsg(r)
		if err != nil {
			return fmt.Errorf("控制连接读取: %w", err)
		}
		switch m.Type {
		case proto.TypeAccept:
			go c.handleAccept(m.ConnID)
		case proto.TypePong:
			// 心跳应答,忽略
		}
	}
}

// handleAccept 对服务端的每个 accept 回拨一条数据连接,
// 并与本地服务之间双向透传。
func (c *client) handleAccept(connID string) {
	remote, err := net.DialTimeout("tcp", c.server, 10*time.Second)
	if err != nil {
		log.Printf("回拨数据连接失败: %v", err)
		return
	}
	defer remote.Close()

	if err := proto.WriteMsg(remote, &proto.Msg{
		Type: proto.TypeData, Subdomain: c.name, Token: c.token, ConnID: connID,
	}); err != nil {
		log.Printf("数据连接握手失败: %v", err)
		return
	}

	local, err := net.DialTimeout("tcp", c.local, 10*time.Second)
	if err != nil {
		log.Printf("连接本地服务 %s 失败: %v", c.local, err)
		return
	}
	defer local.Close()

	// 双向拷贝,借助半关闭让两个方向各自自然结束。
	done := make(chan struct{}, 2)
	go pipe(local, remote, done)
	go pipe(remote, local, done)
	<-done
	<-done
}

func pipe(dst, src net.Conn, done chan<- struct{}) {
	io.Copy(dst, src) //nolint:errcheck
	if tc, ok := dst.(*net.TCPConn); ok {
		tc.CloseWrite() //nolint:errcheck // 半关闭,让对端读到 EOF
	}
	done <- struct{}{}
}
