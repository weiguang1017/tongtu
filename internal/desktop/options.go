// Package desktop 实现桌面模式:系统托盘常驻 + 原生 WebView 窗口。
//
// 进程模型:主进程跑托盘、面板 HTTP 服务与连接器(runner.Manager);
// 窗口是独立子进程(tongtu window),关窗只是子进程退出,主进程照常
// 后台运行——托盘菜单「退出」才真正退出。窗口子进程经 stdin 管道与
// 主进程相连:读到 EOF(主进程任何死法)即自行退出,读到 raise 则把
// 窗口带到前台(防多开:重复「打开面板」只置前不再 spawn)。
//
// 全部实现代码带 //go:build desktop 标签;无标签构建(headless)只含
// 本文件与 stub,保持纯 Go、可交叉编译。
package desktop

// Options 是桌面模式启动参数。
type Options struct {
	Hidden bool   // 静默启动到托盘,不自动打开窗口(开机自启用)
	Addr   string // 面板监听地址,默认 127.0.0.1:7080
}
