package hhapi

import (
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// loadCookieJar загружает Netscape cookies.txt; дальше jar сам обновляет Set-Cookie.
func loadCookieJar(path string) *cookiejar.Jar {
	jar, _ := cookiejar.New(nil)
	data, err := os.ReadFile(path)
	if err != nil {
		return jar
	}
	byDomain := map[string][]*http.Cookie{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 7 || !strings.Contains(f[0], "hh.ru") {
			continue
		}
		expires := time.Time{}
		if sec, err := strconv.ParseInt(f[4], 10, 64); err == nil && sec > 0 {
			expires = time.Unix(sec, 0)
		}
		cookie := &http.Cookie{Name: f[5], Value: f[6], Path: f[2], Domain: f[0], Secure: f[3] == "TRUE", Expires: expires}
		byDomain[f[0]] = append(byDomain[f[0]], cookie)
	}
	for domain, cookies := range byDomain {
		host := strings.TrimPrefix(domain, ".")
		target, err := url.Parse("https://" + host + "/")
		if err != nil {
			continue
		}
		jar.SetCookies(target, cookies)
	}
	return jar
}
