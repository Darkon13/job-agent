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
	flag.Parse()
	if *captchaURL == "" {
		log.Fatal("usage: hhapi-captcha -url CAPTCHA_URL")
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
	fmt.Println("OK: капча принята")
}
