package hhapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	AndroidClientID     = "HIOMIAS39CA9DICTA7JIO64LQKQJF5AGIK74G9ITJKLNEDAOH5FHS5G1JI7FOEGD"
	AndroidClientSecret = "V9M870DE342BGHFRUJ5FTCGCUA1482AN0DI8C5TFI9ULMA89H10N60NOP8I4JMVS"
)

func AuthorizeURL(clientID, redirectURI, state string) string {
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {clientID},
		"state":         {state},
	}
	if redirectURI != "" {
		params.Set("redirect_uri", redirectURI)
	}
	return "https://hh.ru/oauth/authorize?" + params.Encode()
}

func (c *Client) ExchangeCode(ctx context.Context, clientID, clientSecret, code string) error {
	form := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     clientID,
		"client_secret": clientSecret,
		"code":          code,
	}
	var token Token
	if err := c.tokenRequest(ctx, form, &token); err != nil {
		return err
	}
	return c.SetToken(token)
}

func (c *Client) tokenRequest(ctx context.Context, form map[string]string, out *Token) error {
	data := url.Values{}
	for k, v := range form {
		data.Set(k, v)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oauthBaseURL+"/token", strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("hh oauth: %d: %s", resp.StatusCode, string(body))
	}
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return err
	}
	out.AccessToken = response.AccessToken
	out.RefreshToken = response.RefreshToken
	out.ExpiresAt = time.Now().Unix() + response.ExpiresIn
	return nil
}
