package hhapi

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
)

var androidDevices = []string{"ETAIY1TDQC", "C5PKOM6TX3", "HWYQGOTTZ8", "GNTQ4N4HKC", "XK7Q1YQ2ZP"}
var androidVersions = []string{"11", "12", "13", "14", "15"}

func nativeUserAgent() string {
	randInt := func(max int64) int64 {
		n, err := rand.Int(rand.Reader, big.NewInt(max))
		if err != nil {
			return 0
		}
		return n.Int64()
	}
	uuid := make([]byte, 16)
	_, _ = rand.Read(uuid)
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80
	formatted := fmt.Sprintf("%s-%s-%s-%s-%s",
		hex.EncodeToString(uuid[0:4]), hex.EncodeToString(uuid[4:6]),
		hex.EncodeToString(uuid[6:8]), hex.EncodeToString(uuid[8:10]),
		hex.EncodeToString(uuid[10:16]))
	return fmt.Sprintf("ru.hh.android/7.%d.%d, Device: %s, Android OS: %s (UUID: %s)",
		randInt(200), randInt(20000), androidDevices[randInt(int64(len(androidDevices)))],
		androidVersions[randInt(int64(len(androidVersions)))], formatted)
}
