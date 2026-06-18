package service

import (
	"net"
	"net/url"
	"strconv"
	"time"
)

// defaultProxyURL 全局默认代理URL，当账号未配置代理时回退使用
var defaultProxyURL string

// SetDefaultProxyURL 设置全局默认代理URL
func SetDefaultProxyURL(url string) {
	defaultProxyURL = url
}

// GetDefaultProxyURL 获取当前全局默认代理URL
func GetDefaultProxyURL() string {
	return defaultProxyURL
}

type Proxy struct {
	ID        int64
	Name      string
	Protocol  string
	Host      string
	Port      int
	Username  string
	Password  string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p *Proxy) IsActive() bool {
	return p.Status == StatusActive
}

func (p *Proxy) URL() string {
	if p == nil || p.Protocol == "" {
		return defaultProxyURL
	}
	u := &url.URL{
		Scheme: p.Protocol,
		Host:   net.JoinHostPort(p.Host, strconv.Itoa(p.Port)),
	}
	if p.Username != "" && p.Password != "" {
		u.User = url.UserPassword(p.Username, p.Password)
	}
	return u.String()
}

type ProxyWithAccountCount struct {
	Proxy
	AccountCount   int64
	LatencyMs      *int64
	LatencyStatus  string
	LatencyMessage string
	IPAddress      string
	Country        string
	CountryCode    string
	Region         string
	City           string
	QualityStatus  string
	QualityScore   *int
	QualityGrade   string
	QualitySummary string
	QualityChecked *int64
}

type ProxyAccountSummary struct {
	ID       int64
	Name     string
	Platform string
	Type     string
	Notes    *string
}
