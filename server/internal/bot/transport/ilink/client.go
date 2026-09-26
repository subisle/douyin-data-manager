// Package ilink 是微信 iLink 协议的 Go 实现。
//
// 端点、请求体、认证头全部照搬 615 的 shared/ilink-adapter.js，
// 这是私有协议，字段名和 header 一个都不能改。
//
// 协议要点：
//   - 固定路径 {base}/ilink/bot/{route}
//   - POST 必须带 AuthorizationType: ilink_bot_token、X-WECHAT-UIN、
//     Authorization: Bearer {token}，请求体自动包一层 base_info.channel_version
//   - 长轮询 getupdates，返回 msgs[] 与游标 get_updates_buf
//   - errcode == -14 表示登录过期，必须重新扫码
package ilink

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultBaseURL iLink 生产地址
	DefaultBaseURL = "https://ilinkai.weixin.qq.com"
	// ChannelVersion 客户端版本号，服务端会校验
	ChannelVersion = "1.0.2"
	// SessionExpiredCode 登录过期的错误码
	SessionExpiredCode = -14
	// maxResponseBytes 响应体上限，防止被异常端点撑爆内存
	maxResponseBytes = 2 << 20
)

// trustedHosts 只允许这些主机，避免配置被人改成任意地址导致 token 外泄
var trustedHosts = map[string]bool{
	"ilinkai.weixin.qq.com": true,
	"edge.weixin.qq.com":    true,
}

// SessionExpiredError 登录已过期，调用方应当清理凭证并提示重新扫码。
type SessionExpiredError struct{ ErrMsg string }

func (e *SessionExpiredError) Error() string {
	return "微信登录已过期，请重新扫码连接：" + e.ErrMsg
}

// APIError 业务错误码非 0。
type APIError struct {
	Code   int
	ErrMsg string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("微信接口错误 (%d): %s", e.Code, e.ErrMsg)
}

// Client 是 iLink 的 HTTP 客户端。零值不可用，请用 NewClient。
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient 构造客户端。token 留空表示尚未登录（只能调扫码接口）。
func NewClient(baseURL, token string) (*Client, error) {
	normalized, err := normalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &Client{
		baseURL: normalized,
		token:   strings.TrimSpace(token),
		http:    &http.Client{Timeout: 20 * time.Second},
	}, nil
}

// SetToken 登录成功后写入 token。
func (c *Client) SetToken(token string) { c.token = strings.TrimSpace(token) }

// BaseURL 返回规范化后的基地址。
func (c *Client) BaseURL() string { return c.baseURL }

func normalizeBaseURL(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = DefaultBaseURL
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("微信接口地址无效: %w", err)
	}
	if u.Scheme != "https" {
		return "", fmt.Errorf("微信接口地址必须使用 HTTPS")
	}
	if !trustedHosts[strings.ToLower(u.Hostname())] {
		return "", fmt.Errorf("微信接口地址是非受信任的 iLink 主机: %s", u.Hostname())
	}
	if u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("微信接口地址包含不支持的端口、凭据或参数")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

func (c *Client) routeURL(route string, query map[string]string) string {
	full := c.baseURL + "/ilink/bot/" + route
	if len(query) == 0 {
		return full
	}
	q := url.Values{}
	for k, v := range query {
		q.Set(k, v)
	}
	return full + "?" + q.Encode()
}

// randomUIN 生成 X-WECHAT-UIN：4 字节随机数的十进制字符串再做 base64。
func randomUIN() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		binary.BigEndian.PutUint32(buf[:], uint32(time.Now().UnixNano()))
	}
	value := binary.BigEndian.Uint32(buf[:])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(value), 10)))
}

// doJSON 发请求并解析 JSON。POST 会自动补 base_info 与认证头。
func (c *Client) doJSON(ctx context.Context, method, route string,
	query map[string]string, body any) (map[string]any, error) {

	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体失败: %w", err)
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.routeURL(route, query), reader)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败: %w", err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("X-WECHAT-UIN", randomUIN())
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求微信接口失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("读取微信接口响应失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("微信接口 HTTP %d: %s", resp.StatusCode, truncate(string(raw), 200))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]any{}, nil
	}

	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("微信接口返回了无效 JSON: %w", err)
	}
	return out, nil
}

// postJSON 包一层 base_info，与 615 的 postJson 行为一致。
func (c *Client) postJSON(ctx context.Context, route string, body map[string]any) (map[string]any, error) {
	payload := map[string]any{}
	for k, v := range body {
		payload[k] = v
	}
	baseInfo, _ := payload["base_info"].(map[string]any)
	merged := map[string]any{"channel_version": ChannelVersion}
	for k, v := range baseInfo {
		merged[k] = v
	}
	payload["base_info"] = merged
	return c.doJSON(ctx, http.MethodPost, route, nil, payload)
}

// checkCode 把 errcode/ret 非 0 转成错误，-14 单独处理。
func checkCode(payload map[string]any) error {
	code := intOf(payload["errcode"])
	if code == 0 {
		code = intOf(payload["ret"])
	}
	if code == 0 {
		return nil
	}
	msg := stringOf(payload["errmsg"])
	if code == SessionExpiredCode {
		return &SessionExpiredError{ErrMsg: msg}
	}
	return &APIError{Code: code, ErrMsg: msg}
}

/* ------------------------------- 扫码登录 ------------------------------- */

// GetBotQRCode 取登录二维码。
func (c *Client) GetBotQRCode(ctx context.Context, botType string) (map[string]any, error) {
	if botType == "" {
		botType = "3"
	}
	out, err := c.doJSON(ctx, http.MethodGet, "get_bot_qrcode",
		map[string]string{"bot_type": botType}, nil)
	if err != nil {
		return nil, err
	}
	if err := checkCode(out); err != nil {
		return nil, err
	}
	return out, nil
}

// QRCodeStatus 扫码状态与登录凭证。
type QRCodeStatus struct {
	Status    string
	QRCode    string
	QRCodeURL string
	BaseURL   string
	Token     string
	Nickname  string
	Raw       map[string]any
}

// GetQRCodeStatus 查询扫码状态。扫完会带回 token 与 baseUrl。
func (c *Client) GetQRCodeStatus(ctx context.Context, qrcode string) (*QRCodeStatus, error) {
	if strings.TrimSpace(qrcode) == "" {
		return nil, fmt.Errorf("微信二维码标识不能为空")
	}
	out, err := c.doJSON(ctx, http.MethodGet, "get_qrcode_status",
		map[string]string{"qrcode": qrcode}, nil)
	if err != nil {
		return nil, err
	}
	if err := checkCode(out); err != nil {
		return nil, err
	}

	st := &QRCodeStatus{Raw: out}
	st.Status = stringOf(out["status"])
	st.QRCode = stringOf(out["qrcode"])
	st.QRCodeURL = firstOf(out, "qrcode_url", "qr_code_url", "url")
	st.BaseURL = firstOf(out, "baseurl", "base_url")
	st.Token = firstOf(out, "token", "bot_token", "access_token")
	st.Nickname = firstOf(out, "nickname", "nick_name", "name")
	return st, nil
}

/* -------------------------------- 收消息 -------------------------------- */

// Item 消息内容项。type 编号与 615 的 extractMessagePreview 一致：
// 1 文本 / 2 图片 / 3 语音 / 4 文件 / 5 视频。
type Item struct {
	Type      int        `json:"type"`
	TextItem  *TextItem  `json:"text_item,omitempty"`
	FileItem  *FileItem  `json:"file_item,omitempty"`
	ImageItem *ImageItem `json:"image_item,omitempty"`
}

// TextItem 文本内容。
type TextItem struct {
	Text string `json:"text"`
}

// FileItem type=4：群/私聊里发过来的文件（CSV 导入就靠它）。
type FileItem struct {
	Media    MediaRef `json:"media"`
	FileName string   `json:"file_name"`
}

// ImageItem type=2：图片。
type ImageItem struct {
	Media MediaRef `json:"media"`
}

// MediaRef 媒体引用。入站媒体是密文，必须拿 CDN 的参数下载后再解 AES。
type MediaRef struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AesKey            string `json:"aes_key"`
	EncryptType       int    `json:"encrypt_type"`
}

// InboundMessage 一条入站消息。
type InboundMessage struct {
	MessageType  int    `json:"message_type"`
	FromUserID   string `json:"from_user_id"`
	GroupID      string `json:"group_id"`
	ContextToken string `json:"context_token"`
	MessageID    string `json:"message_id"`
	ClientID     string `json:"client_id"`
	CreateTimeMS int64  `json:"create_time_ms"`
	ItemList     []Item `json:"item_list"`
}

// ConversationID 群聊取群 ID，私聊取发送者 ID（与 615 一致）。
func (m InboundMessage) ConversationID() string {
	if strings.TrimSpace(m.GroupID) != "" {
		return m.GroupID
	}
	return m.FromUserID
}

// Text 取第一条文本内容。
func (m InboundMessage) Text() string {
	for _, it := range m.ItemList {
		if it.Type == 1 && it.TextItem != nil {
			return it.TextItem.Text
		}
	}
	return ""
}

// Updates 长轮询结果。
type Updates struct {
	Msgs                 []InboundMessage
	GetUpdatesBuf        string
	LongPollingTimeoutMS int
}

// GetUpdates 长轮询收消息。cursor 为空表示从头开始。
//
// 注意超时时间由服务端下发（longpolling_timeout_ms），客户端要跟着调，
// 写死一个短超时会导致空轮询把服务端打爆。
func (c *Client) GetUpdates(ctx context.Context, cursor string, timeout time.Duration) (*Updates, error) {
	if timeout > 0 {
		c.http.Timeout = timeout
	}
	out, err := c.postJSON(ctx, "getupdates", map[string]any{"get_updates_buf": cursor})
	if err != nil {
		return nil, err
	}
	if err := checkCode(out); err != nil {
		return nil, err
	}

	res := &Updates{
		GetUpdatesBuf:        stringOf(out["get_updates_buf"]),
		LongPollingTimeoutMS: intOf(out["longpolling_timeout_ms"]),
	}

	raw, _ := json.Marshal(out["msgs"])
	_ = json.Unmarshal(raw, &res.Msgs)
	return res, nil
}

/* -------------------------------- 发消息 -------------------------------- */

type outMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param"`
	AesKey            string `json:"aes_key"`
	EncryptType       int    `json:"encrypt_type"`
}

type outImageItem struct {
	Media   outMedia `json:"media"`
	MidSize int      `json:"mid_size"`
	HdSize  int      `json:"hd_size"`
}

// outFileItem type=4：主动给会话发文件（导出结果回传等）。
// md5/len 与 615 的 buildMediaItem 逐字段对齐。
type outFileItem struct {
	Media    outMedia `json:"media"`
	FileName string   `json:"file_name"`
	Md5      string   `json:"md5"`
	Len      string   `json:"len"`
}

type outItem struct {
	Type     int `json:"type"`
	TextItem struct {
		Text string `json:"text"`
	} `json:"text_item,omitempty"`
	ImageItem *outImageItem `json:"image_item,omitempty"`
	FileItem  *outFileItem  `json:"file_item,omitempty"`
}

type outboundMsg struct {
	FromUserID   string    `json:"from_user_id"`
	ToUserID     string    `json:"to_user_id"`
	ClientID     string    `json:"client_id"`
	MessageType  int       `json:"message_type"`
	MessageState int       `json:"message_state"`
	ContextToken string    `json:"context_token"`
	ItemList     []outItem `json:"item_list"`
	GroupID      string    `json:"group_id,omitempty"`
}

// SendText 发一条文本消息。
//
// toUserID 是「对方的 user id」：群聊时是群里那个人的 id，私聊时就是对方。
// groupID 非空表示发到群里。contextToken 由收到的消息带回，用于关联会话。
func (c *Client) SendText(ctx context.Context, toUserID, groupID, contextToken, text string) error {
	item := outItem{Type: 1}
	item.TextItem.Text = text
	return c.SendItems(ctx, toUserID, groupID, contextToken, []outItem{item})
}

// SendItems 发一条带任意 item 列表的消息（文本 / 图片混发，与 615 的
// sendmessage 一致）。图片 item 由 uploadImage 生成。
func (c *Client) SendItems(ctx context.Context, toUserID, groupID, contextToken string, items []outItem) error {
	msg := outboundMsg{
		FromUserID:   "",
		ToUserID:     toUserID,
		ClientID:     clientID(),
		MessageType:  2,
		MessageState: 2,
		ContextToken: contextToken,
		ItemList:     items,
		GroupID:      groupID,
	}

	out, err := c.postJSON(ctx, "sendmessage", map[string]any{"msg": msg})
	if err != nil {
		return err
	}
	return checkCode(out)
}

// GetUploadURL 换取媒体上传参数（对应 615 的 ilink/bot/getuploadurl）。
func (c *Client) GetUploadURL(ctx context.Context, payload map[string]any) (map[string]any, error) {
	out, err := c.postJSON(ctx, "getuploadurl", payload)
	if err != nil {
		return nil, err
	}
	if err := checkCode(out); err != nil {
		return nil, err
	}
	return out, nil
}

// clientID 幂等标识，服务端用它去重。
func clientID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		binary.BigEndian.PutUint64(buf[:], uint64(time.Now().UnixNano()))
	}
	return fmt.Sprintf("c-%d-%s", time.Now().UnixMilli(),
		base64.RawURLEncoding.EncodeToString(buf[:]))
}

func stringOf(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func firstOf(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s := stringOf(m[k]); s != "" {
			return s
		}
	}
	return ""
}

func truncate(s string, n int) string {
	runes := []rune(strings.TrimSpace(s))
	if len(runes) <= n {
		return string(runes)
	}
	return string(runes[:n]) + "…"
}
