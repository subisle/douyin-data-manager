// Package qq 是 QQ 官方机器人（开放平台）的 Go 实现。
//
// 协议照搬 615 的 shared/qqbot-adapter.js：
//   - token：POST https://bots.qq.com/app/getAppAccessToken {appId, clientSecret}
//   - 鉴权头：Authorization: QQBot {access_token} + X-Union-Appid: {appId}
//   - 事件走 WebSocket gateway；群 @ = GROUP_AT_MESSAGE_CREATE，私聊 = C2C_MESSAGE_CREATE
//   - 被动回复必须带 msg_id，且要在有效时间窗口内
//
// 注意：官方 token 接口经常返回 HTTP 200 + 业务错误码，所以不能只看状态码。
package qq

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// DefaultAPIBase 开放平台 API 地址
	DefaultAPIBase = "https://api.sgroup.qq.com"
	// DefaultTokenURL 取 access_token 的地址
	DefaultTokenURL = "https://bots.qq.com/app/getAppAccessToken"
	// IntentGroupAndC2C 群聊与私聊事件所需的 intent 位
	IntentGroupAndC2C = 1 << 25
	// MsgTypeText 文本消息类型；7 是富媒体
	MsgTypeText = 0
	// MsgTypeMedia 富媒体消息（图/文件），media 里带 files 接口返回的 file_info
	MsgTypeMedia = 7
	// FileTypeImage 上传文件时的图片类型
	FileTypeImage = 1
)

// Client 是 QQ 开放平台的 HTTP 客户端。
type Client struct {
	appID        string
	clientSecret string
	apiBase      string
	tokenURL     string
	http         *http.Client

	mu          sync.RWMutex
	accessToken string
	expiresAt   time.Time
}

// NewClient 构造客户端。
func NewClient(appID, clientSecret, apiBase string) *Client {
	if apiBase == "" {
		apiBase = DefaultAPIBase
	}
	return &Client{
		appID:        appID,
		clientSecret: clientSecret,
		apiBase:      trimSlash(apiBase),
		tokenURL:     DefaultTokenURL,
		http:         &http.Client{Timeout: 20 * time.Second},
	}
}

// FetchAccessToken 取 access_token。有效期 7200 秒，到期前需要重取。
func (c *Client) FetchAccessToken(ctx context.Context) (string, time.Duration, error) {
	if c.appID == "" {
		return "", 0, fmt.Errorf("请填写 QQ 机器人 AppID")
	}
	if c.clientSecret == "" {
		return "", 0, fmt.Errorf("请填写 QQ 机器人 ClientSecret")
	}

	body, err := json.Marshal(map[string]string{
		"appId":        c.appID,
		"clientSecret": c.clientSecret,
	})
	if err != nil {
		return "", 0, fmt.Errorf("序列化 token 请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("构造 token 请求失败: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("请求 QQ token 接口失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var data struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Code        any    `json:"code"`
		Message     string `json:"message"`
		Msg         string `json:"msg"`
		Error       string `json:"error"`
	}
	_ = json.Unmarshal(raw, &data)

	if data.AccessToken == "" {
		msg := firstNonEmpty(data.Message, data.Msg, data.Error, string(raw))
		if data.Code != nil {
			return "", 0, fmt.Errorf("获取 QQ Bot token 失败：%s（code %v）", msg, data.Code)
		}
		return "", 0, fmt.Errorf("获取 QQ Bot token 失败：%s", msg)
	}

	ttl := time.Duration(data.ExpiresIn) * time.Second
	if ttl < time.Minute {
		ttl = 2 * time.Hour
	}
	c.mu.Lock()
	c.accessToken = data.AccessToken
	c.expiresAt = time.Now().Add(ttl)
	c.mu.Unlock()

	return data.AccessToken, ttl, nil
}

// EnsureToken 返回可用 token，快过期或没有时自动重取。
func (c *Client) EnsureToken(ctx context.Context) (string, error) {
	c.mu.RLock()
	token := c.accessToken
	expiresAt := c.expiresAt
	c.mu.RUnlock()

	// 留 5 分钟余量，避免刚好在请求途中过期
	if token != "" && time.Now().Add(5*time.Minute).Before(expiresAt) {
		return token, nil
	}
	token, _, err := c.FetchAccessToken(ctx)
	return token, err
}

// SetToken 手工注入 token（供持久化恢复）。
func (c *Client) SetToken(token string, ttl time.Duration) {
	c.mu.Lock()
	c.accessToken = token
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	c.expiresAt = time.Now().Add(ttl)
	c.mu.Unlock()
}

func (c *Client) authHeaders(token string) map[string]string {
	return map[string]string{
		"Authorization": "QQBot " + token,
		"X-Union-Appid": c.appID,
		"Content-Type":  "application/json",
	}
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any) (map[string]any, error) {
	token, err := c.EnsureToken(ctx)
	if err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}
	for k, v := range c.authHeaders(token) {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求 QQ 接口失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("QQ 接口 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	return out, nil
}

// Gateway 取 WebSocket 接入地址。
func (c *Client) Gateway(ctx context.Context) (string, error) {
	out, err := c.doJSON(ctx, http.MethodGet, "/gateway", nil)
	if err != nil {
		return "", err
	}
	url, _ := out["url"].(string)
	if url == "" {
		return "", fmt.Errorf("QQ Bot gateway 响应缺少 url")
	}
	return url, nil
}

// SendToGroup 发群消息。被动回复要带 msgID（触发消息的 id）与唯一 msgSeq。
func (c *Client) SendToGroup(ctx context.Context, groupOpenID, content, msgID string, msgSeq int) error {
	if groupOpenID == "" {
		return fmt.Errorf("缺少 group_openid")
	}
	body := map[string]any{
		"content":  content,
		"msg_type": MsgTypeText,
		"msg_seq":  nextMsgSeq(msgSeq),
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	_, err := c.doJSON(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/messages", body)
	return err
}

// SendToUser 发私聊消息。
func (c *Client) SendToUser(ctx context.Context, openID, content, msgID string, msgSeq int) error {
	if openID == "" {
		return fmt.Errorf("缺少 user openid")
	}
	body := map[string]any{
		"content":  content,
		"msg_type": MsgTypeText,
		"msg_seq":  nextMsgSeq(msgSeq),
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	_, err := c.doJSON(ctx, http.MethodPost,
		"/v2/users/"+url.PathEscape(openID)+"/messages", body)
	return err
}

// nextMsgSeq 官方要求同一 msg_id 下 msg_seq 唯一。
func nextMsgSeq(provided int) int {
	if provided > 0 {
		return provided
	}
	return int(time.Now().UnixNano()%900000) + 1000
}

/* ------------------------------- 富媒体发图 ------------------------------- */
//
// 照搬 615 shared/qqbot-adapter.js 的 uploadGroupFile/uploadC2cFile：
// /files 接口支持 file_data（base64）直传，不需要公网 URL；
// 拿到 file_info 后用 msg_type=7 发 media 消息。

// uploadFile 上传媒体拿 file_info。path 是 /v2/groups/{id}/files 或 /v2/users/{id}/files。
func (c *Client) uploadFile(ctx context.Context, path string, fileType int, fileData []byte) (string, error) {
	if len(fileData) == 0 {
		return "", fmt.Errorf("上传媒体内容为空")
	}
	body := map[string]any{
		"file_type":    fileType,
		"srv_send_msg": false,
		"file_data":    base64.StdEncoding.EncodeToString(fileData),
	}
	out, err := c.doJSON(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	fi, _ := out["file_info"].(string)
	if fi == "" {
		if d, ok := out["data"].(map[string]any); ok {
			fi, _ = d["file_info"].(string)
		}
	}
	if fi == "" {
		return "", fmt.Errorf("QQ 媒体上传成功但缺少 file_info")
	}
	return fi, nil
}

// UploadGroupImage 群图片上传，返回 file_info。
func (c *Client) UploadGroupImage(ctx context.Context, groupOpenID string, data []byte) (string, error) {
	if groupOpenID == "" {
		return "", fmt.Errorf("缺少 group_openid")
	}
	return c.uploadFile(ctx, "/v2/groups/"+url.PathEscape(groupOpenID)+"/files", FileTypeImage, data)
}

// UploadC2cImage 私聊图片上传，返回 file_info。
func (c *Client) UploadC2cImage(ctx context.Context, openID string, data []byte) (string, error) {
	if openID == "" {
		return "", fmt.Errorf("缺少 user openid")
	}
	return c.uploadFile(ctx, "/v2/users/"+url.PathEscape(openID)+"/files", FileTypeImage, data)
}

// SendGroupMedia 发群富媒体消息（msg_type=7）。
func (c *Client) SendGroupMedia(ctx context.Context, groupOpenID, fileInfo, msgID string, msgSeq int) error {
	if groupOpenID == "" {
		return fmt.Errorf("缺少 group_openid")
	}
	body := map[string]any{
		"msg_type": MsgTypeMedia,
		"media":    map[string]any{"file_info": fileInfo},
		"msg_seq":  nextMsgSeq(msgSeq),
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	_, err := c.doJSON(ctx, http.MethodPost,
		"/v2/groups/"+url.PathEscape(groupOpenID)+"/messages", body)
	return err
}

// SendUserMedia 发私聊富媒体消息（msg_type=7）。
func (c *Client) SendUserMedia(ctx context.Context, openID, fileInfo, msgID string, msgSeq int) error {
	if openID == "" {
		return fmt.Errorf("缺少 user openid")
	}
	body := map[string]any{
		"msg_type": MsgTypeMedia,
		"media":    map[string]any{"file_info": fileInfo},
		"msg_seq":  nextMsgSeq(msgSeq),
	}
	if msgID != "" {
		body["msg_id"] = msgID
	}
	_, err := c.doJSON(ctx, http.MethodPost,
		"/v2/users/"+url.PathEscape(openID)+"/messages", body)
	return err
}

func trimSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
}
