// Package config 负责通途本地配置(~/.tongtu/config.json)的读写与校验。
//
// 配置含 Cloudflare API Token 等敏感信息,文件权限 0600,写入采用
// 临时文件 + rename 的原子方式,避免中途崩溃留下半截文件。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Version 是当前配置文件格式版本。
const Version = 1

var (
	// NameRe 限制凭证别名/应用名:小写字母数字与中划线。
	NameRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// HostnameRe 粗校验完整域名(至少两段)。
	HostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)
)

// Cred 是一个 Cloudflare API 凭证。
type Cred struct {
	Token string `json:"token"`
}

// Domain 是一个已登记的、托管在 Cloudflare 上的根域名。
type Domain struct {
	Cred      string `json:"cred"`       // 引用的凭证别名
	ZoneID    string `json:"zone_id"`    // add 时验证后缓存
	AccountID string `json:"account_id"` // add 时验证后缓存
}

// App 是一个对外暴露的应用。
type App struct {
	Hostname         string `json:"hostname"`                     // 对外完整域名,如 blog.example.com
	Local            string `json:"local"`                        // 本地服务地址 host:port
	Proto            string `json:"proto"`                        // http / https / tcp
	NoTLSVerify      bool   `json:"no_tls_verify,omitempty"`      // 源站自签证书时跳过校验
	OriginServerName string `json:"origin_server_name,omitempty"` // 源站 TLS SNI
	Enabled          bool   `json:"enabled"`
}

// Config 是通途的全部本地配置。
type Config struct {
	Version int               `json:"version"`
	Creds   map[string]*Cred  `json:"creds"`
	Domains map[string]*Domain `json:"domains"`
	Apps    map[string]*App   `json:"apps"`

	path string // 加载来源,Save 时写回
}

// DefaultPath 返回默认配置文件路径 ~/.tongtu/config.json。
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户主目录: %w", err)
	}
	return filepath.Join(home, ".tongtu", "config.json"), nil
}

// Load 读取配置;文件不存在时返回空配置(首次使用)。
func Load() (*Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return LoadFrom(path)
}

// LoadFrom 从指定路径读取配置。
func LoadFrom(path string) (*Config, error) {
	c := &Config{
		Version: Version,
		Creds:   map[string]*Cred{},
		Domains: map[string]*Domain{},
		Apps:    map[string]*App{},
		path:    path,
	}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	if err := json.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	if c.Version > Version {
		return nil, fmt.Errorf("配置 %s 版本 %d 高于本程序支持的 %d,请升级通途", path, c.Version, Version)
	}
	// 兼容手工编辑导致的 nil map
	if c.Creds == nil {
		c.Creds = map[string]*Cred{}
	}
	if c.Domains == nil {
		c.Domains = map[string]*Domain{}
	}
	if c.Apps == nil {
		c.Apps = map[string]*App{}
	}
	c.path = path
	return c, nil
}

// Save 原子写回配置文件(0600,临时文件 + rename)。
func (c *Config) Save() error {
	c.Version = Version
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(c.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后此调用无害
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, c.path); err != nil {
		return fmt.Errorf("写入配置 %s: %w", c.path, err)
	}
	return nil
}

// ---- 引用完整性与查询 ----

// DomainsUsingCred 返回引用某凭证的域名列表(有序)。
func (c *Config) DomainsUsingCred(cred string) []string {
	var out []string
	for name, d := range c.Domains {
		if d.Cred == cred {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// AppsUsingDomain 返回 hostname 属于某根域名的应用列表(有序)。
func (c *Config) AppsUsingDomain(domain string) []string {
	var out []string
	for name, a := range c.Apps {
		if a.Hostname == domain || strings.HasSuffix(a.Hostname, "."+domain) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// DomainForHostname 找出 hostname 所属的已登记根域名;未登记返回错误。
func (c *Config) DomainForHostname(hostname string) (string, *Domain, error) {
	// 取最长后缀匹配,支持 example.com 与 t.example.com 同时登记的情况
	best := ""
	for name := range c.Domains {
		if (hostname == name || strings.HasSuffix(hostname, "."+name)) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		var known []string
		for name := range c.Domains {
			known = append(known, name)
		}
		sort.Strings(known)
		if len(known) == 0 {
			return "", nil, fmt.Errorf("尚未登记任何域名,请先执行: tongtu domain add <根域名>")
		}
		return "", nil, fmt.Errorf("域名 %s 不属于任何已登记的根域名(已登记: %s)", hostname, strings.Join(known, ", "))
	}
	return best, c.Domains[best], nil
}

// CredForApp 返回应用所用的凭证别名与凭证。
func (c *Config) CredForApp(app *App) (string, *Cred, *Domain, error) {
	_, d, err := c.DomainForHostname(app.Hostname)
	if err != nil {
		return "", nil, nil, err
	}
	cred, ok := c.Creds[d.Cred]
	if !ok {
		return "", nil, nil, fmt.Errorf("域名引用的凭证 %q 不存在,配置可能被手工改坏", d.Cred)
	}
	return d.Cred, cred, d, nil
}

// SortedNames 返回 map 的有序键,供 list 类命令稳定输出。
func SortedNames[M ~map[string]V, V any](m M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
