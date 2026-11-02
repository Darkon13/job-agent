package hhapi

import (
	"os"
	"strings"
)

// loadCookies читает Netscape cookies.txt и возвращает строку для заголовка Cookie.
func loadCookies(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var parts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 7 {
			continue
		}
		domain, name, value := fields[0], fields[5], fields[6]
		if !strings.Contains(domain, "hh.ru") || name == "" {
			continue
		}
		parts = append(parts, name+"="+value)
	}
	return strings.Join(parts, "; ")
}
