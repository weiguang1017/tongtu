// Package proto 定义通途客户端与服务端之间的控制协议。
//
// 控制连接与数据连接都以一行 JSON(\n 结尾)作为握手消息,
// 数据连接握手完成后退化为纯字节流,双向透传。
package proto

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// 消息类型
const (
	// 客户端 -> 服务端(控制连接第一条消息):注册子域名
	TypeRegister = "register"
	// 服务端 -> 客户端:注册结果
	TypeRegistered = "registered"
	// 服务端 -> 客户端:有新的公网请求,请建立数据连接
	TypeAccept = "accept"
	// 客户端 -> 服务端(数据连接第一条消息):认领某个公网请求
	TypeData = "data"
	// 心跳
	TypePing = "ping"
	TypePong = "pong"
	// 服务端 -> 客户端:错误(注册失败等)
	TypeError = "error"
)

// Msg 是控制协议的统一消息结构,按 Type 取用相应字段。
type Msg struct {
	Type string `json:"type"`

	// register / data
	Subdomain string `json:"subdomain,omitempty"`
	Token     string `json:"token,omitempty"`

	// registered
	Domain string `json:"domain,omitempty"` // 完整对外域名,如 blog.example.com

	// accept / data
	ConnID string `json:"conn_id,omitempty"`

	// error / registered
	Message string `json:"message,omitempty"`
}

// WriteMsg 将消息编码为一行 JSON 写入连接。
func WriteMsg(conn net.Conn, m *Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = conn.Write(b)
	return err
}

// ReadMsg 从 bufio.Reader 读取一行 JSON 消息。
// 单条消息上限 16KB,防止恶意超长行耗尽内存。
func ReadMsg(r *bufio.Reader) (*Msg, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	if len(line) > 16*1024 {
		return nil, fmt.Errorf("proto: message too long (%d bytes)", len(line))
	}
	var m Msg
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, fmt.Errorf("proto: bad message: %w", err)
	}
	return &m, nil
}

// ReadMsgTimeout 带超时地读取一条消息,用于握手阶段防止连接挂死。
func ReadMsgTimeout(conn net.Conn, r *bufio.Reader, d time.Duration) (*Msg, error) {
	if err := conn.SetReadDeadline(time.Now().Add(d)); err != nil {
		return nil, err
	}
	defer conn.SetReadDeadline(time.Time{}) //nolint:errcheck // 清除 deadline
	return ReadMsg(r)
}
