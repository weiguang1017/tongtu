package runner

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"tongtu/internal/cloudflared"
	"tongtu/internal/config"
)

// ErrAlreadyRunning 表示连接器总开关已打开。
var ErrAlreadyRunning = errors.New("隧道已在运行,请先停止")

// Manager 托管全部 cloudflared 组进程,是「期望状态(配置)→ 实际状态
// (Cloudflare ingress/DNS + 子进程)」的唯一运行态持有者。应用的新增/
// 编辑/启停/删除通过 Reconcile 即时收敛,按需启停各凭证组的连接器,
// 无需整体重启;隧道为远程配置模式,ingress 变更由 cloudflared 自动热加载。
type Manager struct {
	// opMu 串行化 Start/Stop/Reconcile(Cloudflare API 调用期间持有);
	// mu 只保护下方状态字段,供 Status 低延迟读取,不得跨 API 调用持有。
	opMu sync.Mutex
	mu   sync.Mutex

	running bool
	sel     []string              // Start 时的点名集合(空=全部启用应用),Reconcile 沿用
	bin     string                // cloudflared 路径,Start 时探测缓存
	cancel  context.CancelFunc    // 取消 root,即停掉全部组进程
	root    context.Context       // 所有组进程 ctx 的父
	procs   map[string]*groupProc // credName -> 运行中的组进程
	errs    map[string]string     // credName -> 最近一次同步失败原因(成功后清除)
}

// groupProc 是一个凭证组的运行态(一条隧道、一个 cloudflared 进程)。
type groupProc struct {
	credName  string
	token     string // CF API Token,组下线时清 ingress 用
	accountID string
	tunnelID  string
	published []string // 最近一次成功写入 ingress 的对外域名
	cancel    context.CancelFunc
	done      chan struct{}
}

// GroupStatus 是一个凭证组的运行快照。
type GroupStatus struct {
	Alive     bool     // 连接器进程是否在托管中
	Published []string // 已写入 ingress 的对外域名
	LastError string   // 最近一次同步失败原因
}

// ManagerStatus 是 Manager 的运行快照(纯内存,不打 Cloudflare API)。
type ManagerStatus struct {
	Running bool
	Groups  map[string]GroupStatus
}

func NewManager() *Manager {
	return &Manager{procs: map[string]*groupProc{}, errs: map[string]string{}}
}

// Start 打开总开关:同步 plan 中所有组并启动各自的 cloudflared。
// 任一组同步失败即整体失败,不启动任何进程(要么全就绪要么不跑)。
// ctx 仅约束首次同步的 API 调用;进程生命周期由 Stop 控制。
// sel 是点名集合,供后续 Reconcile 沿用(空=全部启用应用)。
func (m *Manager) Start(ctx context.Context, plan *Plan, sel []string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()
	if running {
		return ErrAlreadyRunning
	}

	bin, err := cloudflared.Find()
	if err != nil {
		return err
	}
	results := make([]syncResult, len(plan.groups))
	for i := range plan.groups {
		g := &plan.groups[i]
		res, err := syncGroup(ctx, g)
		if err != nil {
			return fmt.Errorf("凭证 %s: %w", g.credName, err)
		}
		results[i] = res
	}

	root, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.running, m.sel, m.bin = true, sel, bin
	m.root, m.cancel = root, cancel
	m.errs = map[string]string{}
	m.mu.Unlock()
	for i := range plan.groups {
		m.startProc(root, bin, &plan.groups[i], results[i])
	}
	return nil
}

// Stop 关闭总开关:停止全部组进程并等待退出。幂等。
func (m *Manager) Stop() {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}
	m.running = false
	cancel := m.cancel
	procs := m.procs
	m.procs = map[string]*groupProc{}
	m.errs = map[string]string{}
	m.root, m.cancel, m.sel = nil, nil, nil
	m.mu.Unlock()

	cancel()
	for _, gp := range procs {
		waitProc(gp)
	}
}

// Reconcile 把最新配置调和到运行态:逐组同步 ingress/DNS,新出现的凭证组
// 启动连接器,组内应用全部停用/删除时清空 ingress 并停掉连接器。总开关
// 关闭时为 no-op。某组失败只记入该组、不影响其他组(已存活的进程继续按
// 旧规则服务,比中断更安全),聚合错误返回;配置解析失败不触碰任何运行态。
func (m *Manager) Reconcile(ctx context.Context, cfg *config.Config) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	running, sel, bin, root := m.running, m.sel, m.bin, m.root
	m.mu.Unlock()
	if !running {
		return nil
	}

	desired, err := desiredGroups(cfg, sel)
	if err != nil {
		return err
	}

	var errs []error
	seen := map[string]bool{}
	for i := range desired {
		g := &desired[i]
		seen[g.credName] = true
		res, err := syncGroup(ctx, g)
		m.mu.Lock()
		if err != nil {
			m.errs[g.credName] = err.Error()
			m.mu.Unlock()
			errs = append(errs, fmt.Errorf("凭证 %s: %w", g.credName, err))
			continue
		}
		delete(m.errs, g.credName)
		if gp := m.procs[g.credName]; gp != nil {
			// 进程已在托管中:ingress 变更由 cloudflared 热加载,只更新快照
			gp.published = g.hostnames()
			gp.token, gp.accountID, gp.tunnelID = g.token, res.accountID, res.tunnelID
			m.mu.Unlock()
			continue
		}
		m.mu.Unlock()
		m.startProc(root, bin, g, res)
	}

	// 不再期望的组(应用全部停用/删除):清空远端 ingress 后停掉连接器
	m.mu.Lock()
	var gone []*groupProc
	for name, gp := range m.procs {
		if !seen[name] {
			gone = append(gone, gp)
			delete(m.procs, name)
			delete(m.errs, name)
		}
	}
	m.mu.Unlock()
	for _, gp := range gone {
		if err := clearIngress(ctx, gp.token, gp.accountID, gp.tunnelID); err != nil {
			log.Printf("清空凭证 %s 的 ingress 失败: %v", gp.credName, err)
		}
		gp.cancel()
		waitProc(gp)
		log.Printf("凭证 %s 下已无启用应用,其 cloudflared 已停止", gp.credName)
	}
	return errors.Join(errs...)
}

// Status 返回运行态快照。
func (m *Manager) Status() ManagerStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := ManagerStatus{Running: m.running, Groups: map[string]GroupStatus{}}
	for name, gp := range m.procs {
		st.Groups[name] = GroupStatus{Alive: true, Published: append([]string(nil), gp.published...)}
	}
	for name, msg := range m.errs {
		g := st.Groups[name]
		g.LastError = msg
		st.Groups[name] = g
	}
	return st
}

// startProc 记录组运行态并托管其 cloudflared(须在 opMu 下调用)。
func (m *Manager) startProc(root context.Context, bin string, g *group, res syncResult) {
	gctx, gcancel := context.WithCancel(root)
	gp := &groupProc{
		credName:  g.credName,
		token:     g.token,
		accountID: res.accountID,
		tunnelID:  res.tunnelID,
		published: g.hostnames(),
		cancel:    gcancel,
		done:      make(chan struct{}),
	}
	m.mu.Lock()
	m.procs[g.credName] = gp
	m.mu.Unlock()
	for _, e := range g.apps {
		log.Printf("✓ 通途已就绪: https://%s -> %s://%s", e.app.Hostname, e.app.Proto, e.app.Local)
	}
	go func() {
		defer close(gp.done)
		if err := cloudflared.Run(gctx, bin, res.runToken); err != nil && gctx.Err() == nil {
			log.Printf("凭证 %s 的 cloudflared 异常结束: %v", gp.credName, err)
		}
	}()
}

// waitProc 等待组进程退出;cloudflared 侧 SIGTERM→SIGKILL 兜底约 10s,
// 这里再放宽到 15s,超时只记日志不阻塞调用方后续操作。
func waitProc(gp *groupProc) {
	select {
	case <-gp.done:
	case <-time.After(15 * time.Second):
		log.Printf("等待凭证 %s 的 cloudflared 退出超时", gp.credName)
	}
}
