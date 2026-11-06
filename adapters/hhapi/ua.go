package hhapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
)

var androidDevices = []string{"ETAIY1TDQC", "C5PKOM6TX3", "HWYQGOTTZ8", "GNTQ4N4HKC", "XK7Q1YQ2ZP"}
var androidVersions = []string{"11", "12", "13", "14", "15"}

func randInt(max int64) int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(max))
	if err != nil {
		return 0
	}
	return n.Int64()
}

// nativeUserAgent имитирует новый запуск приложения: утилита генерирует
// личность на каждый запуск, мы — каждые N откликов.
func nativeUserAgent() string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	uuid := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(buf[0:4]), hex.EncodeToString(buf[4:6]),
		hex.EncodeToString(buf[6:8]), hex.EncodeToString(buf[8:10]),
		hex.EncodeToString(buf[10:16]))
	return fmt.Sprintf("ru.hh.android/7.%d.%d, Device: %s, Android OS: %s (UUID: %s)",
		randInt(200), randInt(20000), androidDevices[randInt(int64(len(androidDevices)))],
		androidVersions[randInt(int64(len(androidVersions)))], uuid)
}

var currentDeviceUserAgent = nativeUserAgent()

// deviceUserAgent — текущая личность процесса для страниц капчи.
func deviceUserAgent() string { return currentDeviceUserAgent }

// rotateDeviceUserAgent — новая личность, как новый запуск приложения.
func rotateDeviceUserAgent() string {
	currentDeviceUserAgent = nativeUserAgent()
	return currentDeviceUserAgent
}
