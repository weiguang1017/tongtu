//go:build !desktop

package desktop

// Available 报告本构建是否包含桌面组件。
// headless 构建中 cli 层直接拒绝 desktop/window 子命令,这里只需给
// main.go 的无参数分流一个判定。
func Available() bool { return false }
