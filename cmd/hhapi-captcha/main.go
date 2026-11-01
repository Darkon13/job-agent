package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"

	"github.com/Darkon13/job-agent/adapters/hhapi"
)

func main() {
	captchaURL := flag.String("url", "", "captcha_url из ошибки API")
	solve := flag.Bool("solve", false, "решить последнюю сохранённую капчу")
	status := flag.Bool("status", false, "показать ожидающую капчу")
	flag.Parse()
	if *status {
		if data, err := os.ReadFile("data/captcha-pending.txt"); err == nil && len(data) > 0 {
			fmt.Println(string(data))
		} else {
			fmt.Println("капчи нет")
		}
		return
	}
	if *solve {
		if data, err := os.ReadFile("data/captcha-pending.txt"); err == nil && len(data) > 0 {
			*captchaURL = string(data)
		}
	}
	if *captchaURL == "" {
		log.Fatal("usage: hhapi-captcha -solve | -status | -url CAPTCHA_URL")
	}
	err := hhapi.SolveCaptcha(context.Background(), *captchaURL, func(image []byte) (string, error) {
		if err := os.WriteFile("/tmp/hh-captcha.png", image, 0o600); err != nil {
			return "", err
		}
		if _, err := exec.LookPath("chafa"); err == nil {
			cmd := exec.Command("chafa", "--size", "80x24", "/tmp/hh-captcha.png")
			cmd.Stdout = os.Stdout
			_ = cmd.Run()
		} else {
			fmt.Println("IMAGE:/tmp/hh-captcha.png")
		}
		fmt.Print("Введите текст с картинки: ")
		return bufio.NewReader(os.Stdin).ReadString('\n')
	})
	if err != nil {
		log.Fatal(err)
	}
	_ = os.Remove("data/captcha-pending.txt")
	fmt.Println("OK: капча принята")
}
