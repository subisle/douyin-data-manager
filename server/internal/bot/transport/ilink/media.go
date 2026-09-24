// 图片上传：把 615 的 electron/weixin-bot-media.js 移植成 Go。
// 流程：随机 AES-128 密钥 → ECB/PKCS7 加密 → getuploadurl 换参数 →
// POST 密文到 novac2c CDN → 响应头拿下载参数 → 组装 type=2 image_item。
package ilink

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const cdnBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

const mediaTimeout = 30 * time.Second

const maxInboundMediaBytes = 20 << 20 // 入站附件上限，与 bot.downloadClient 一致

// DownloadInboundMedia 下载并解密一条入站媒体（_file/图片通用）。
//
// 入站流程与发图正好相反：CDN 上的是 AES-128-ECB 密文，密钥挂在消息
// item 的 media.aes_key 上。615 的 downloadInboundMedia 逐行照搬。
func (c *Client) DownloadInboundMedia(ctx context.Context, ref MediaRef) ([]byte, error) {
	query := strings.TrimSpace(ref.EncryptQueryParam)
	if query == "" {
		return nil, errors.New("微信消息里没有可下载的媒体参数")
	}

	u := fmt.Sprintf("%s/download?encrypted_query_param=%s", cdnBaseURL, url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("构造媒体下载请求失败: %w", err)
	}

	// 密文比明文最多大一个 block（PKCS7 填充），放宽一点再校验明文长度
	limit := maxInboundMediaBytes + 4096
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("微信媒体下载失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("微信媒体下载失败 (HTTP %d)", resp.StatusCode)
	}
	cipher, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("微信媒体读取失败: %w", err)
	}
	if len(cipher) > limit {
		return nil, errors.New("微信附件超过大小上限（20MB）")
	}

	key, err := parseAesKey(ref.AesKey)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return cipher, nil // 没带密钥，说明本来就是明文
	}
	plain, err := decryptAESECB(cipher, key)
	if err != nil {
		return nil, err
	}
	if len(plain) > maxInboundMediaBytes {
		return nil, errors.New("微信附件超过大小上限（20MB）")
	}
	return plain, nil
}

// parseAesKey 解析 media.aes_key。与 615 同款，三种形态都认：
// base64(16 字节原文) / base64(32 位 hex 字符串) / 裸 32 位 hex。
func parseAesKey(value string) ([]byte, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(text); err == nil {
		if len(decoded) == 16 {
			return decoded, nil
		}
		if len(decoded) == 32 && isHexString(string(decoded)) {
			h, _ := hex.DecodeString(string(decoded))
			return h, nil
		}
	}
	if isHexString(text) {
		h, err := hex.DecodeString(text)
		if err == nil {
			return h, nil
		}
	}
	return nil, errors.New("微信媒体密钥格式无效")
}

func isHexString(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return false
	}
	return true
}

// decryptAESECB AES-128-ECB 解密并剥掉 PKCS7 填充。
func decryptAESECB(cipher, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("媒体密钥无效: %w", err)
	}
	if len(cipher) == 0 || len(cipher)%aes.BlockSize != 0 {
		return nil, errors.New("微信媒体密文长度异常")
	}
	out := make([]byte, len(cipher))
	for i := 0; i < len(cipher); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], cipher[i:i+aes.BlockSize])
	}
	pad := int(out[len(out)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(out) {
		return nil, errors.New("微信媒体解密结果格式异常")
	}
	return out[:len(out)-pad], nil
}

const (
	// mediaTypeImage type=2 图片；mediaTypeFile type=4 普通文件。
	// 编号跟 615 的 MEDIA_TYPE 一致（video 2 / file 3 / voice 4 是另一套命名，别混）。
	mediaTypeImage = 1
	mediaTypeFile  = 3
)

// uploadImage 上传一张图片，返回可直接放进 item_list 的 type=2 item。
func (c *Client) uploadImage(ctx context.Context, data []byte, toUserID, fileName string) (outItem, error) {
	return c.uploadMedia(ctx, mediaTypeImage, "image", data, toUserID, fileName)
}

// uploadFile 上传一个文件，返回可直接放进 item_list 的 type=4 item。
// 与 615 的 buildMediaItem 非图分支一致：media + file_name + md5 + len。
func (c *Client) uploadFile(ctx context.Context, data []byte, toUserID, fileName string) (outItem, error) {
	return c.uploadMedia(ctx, mediaTypeFile, "file", data, toUserID, fileName)
}

// uploadMedia 上传流程与 615 的 uploadMediaBuffer 逐字段对齐：
// 随机 AES-128 密钥 → ECB/PKCS7 加密 → getuploadurl 换参数 →
// POST 密文到 novac2c CDN → 响应头拿下载参数 → 组装 item。
func (c *Client) uploadMedia(ctx context.Context, mediaType int, kind string, data []byte, toUserID, fileName string) (outItem, error) {
	item := outItem{}
	if strings.TrimSpace(kind) == "file" {
		item.Type = 4
	} else {
		item.Type = 2
	}
	if len(data) == 0 {
		return item, fmt.Errorf("待发送内容为空")
	}

	// prepareUpload
	aesKey := make([]byte, 16)
	if _, err := rand.Read(aesKey); err != nil {
		return item, fmt.Errorf("生成媒体密钥失败: %w", err)
	}
	fileKey := make([]byte, 16)
	if _, err := rand.Read(fileKey); err != nil {
		return item, fmt.Errorf("生成 filekey 失败: %w", err)
	}
	fileKeyHex := hex.EncodeToString(fileKey)
	md5sum := fmt.Sprintf("%x", md5.Sum(data))
	ciphertext := encryptAESECB(data, aesKey)

	// getuploadurl（返回值里带 upload_param）
	up, err := c.GetUploadURL(ctx, map[string]any{
		"filekey":       fileKeyHex,
		"media_type":    mediaType,
		"to_user_id":    toUserID,
		"rawsize":       len(data),
		"rawfilemd5":    md5sum,
		"filesize":      len(ciphertext),
		"no_need_thumb": true,
		"aeskey":        hex.EncodeToString(aesKey),
	})
	if err != nil {
		return item, fmt.Errorf("获取上传地址失败: %w", err)
	}
	uploadParam := stringOf(up["upload_param"])
	if uploadParam == "" {
		return item, fmt.Errorf("微信接口未返回媒体上传参数")
	}

	// CDN 上传密文，下载参数在响应头 x-encrypted-param
	downloadParam, err := uploadToCDN(ctx, uploadParam, fileKeyHex, ciphertext)
	if err != nil {
		return item, err
	}

	// buildMediaItem：aes_key 是「hex 字符串」的 base64（不是原始字节的 base64）
	media := outMedia{
		EncryptQueryParam: downloadParam,
		AesKey:            base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(aesKey))),
		EncryptType:       1,
	}
	if item.Type == 4 {
		name := strings.TrimSpace(fileName)
		if name == "" {
			name = "data.csv"
		}
		item.FileItem = &outFileItem{
			Media:    media,
			FileName: name,
			Md5:      md5sum,
			Len:      strconv.Itoa(len(data)),
		}
		return item, nil
	}
	item.ImageItem = &outImageItem{
		Media:   media,
		MidSize: len(ciphertext),
		HdSize:  len(ciphertext),
	}
	return item, nil
}

// uploadToCDN 上传密文到微信 C2C CDN，3 次重试，成功后返回 x-encrypted-param。
func uploadToCDN(ctx context.Context, uploadParam, fileKey string, ciphertext []byte) (string, error) {
	uploadURL := fmt.Sprintf("%s/upload?encrypted_query_param=%s&filekey=%s",
		cdnBaseURL, url.QueryEscape(uploadParam), url.QueryEscape(fileKey))

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		// 每次重试单独给超时；Do 返回后先关 body 再取消 ctx。
		attemptCtx, cancel := context.WithTimeout(ctx, mediaTimeout)
		req, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, uploadURL, bytes.NewReader(ciphertext))
		if err != nil {
			cancel()
			return "", fmt.Errorf("构造媒体上传请求失败: %w", err)
		}
		req.Header.Set("Content-Type", "application/octet-stream")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			lastErr = fmt.Errorf("微信媒体上传失败: %w", err)
		} else {
			downloadParam := resp.Header.Get("X-Encrypted-Param")
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			_ = resp.Body.Close()
			cancel()
			if resp.StatusCode != 200 {
				lastErr = fmt.Errorf("微信媒体上传失败 (HTTP %d)", resp.StatusCode)
			} else if downloadParam == "" {
				lastErr = fmt.Errorf("微信媒体上传缺少下载参数")
			} else {
				return downloadParam, nil
			}
		}
		if attempt >= 3 {
			break
		}
	}
	return "", lastErr
}

// encryptAESECB AES-128-ECB + PKCS7 填充（微信媒体加密固定用 ECB）。
func encryptAESECB(plain, key []byte) []byte {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil // key 长度固定 16，理论上不会走到
	}
	pad := 16 - len(plain)%16
	padded := make([]byte, len(plain)+pad)
	copy(padded, plain)
	for i := len(plain); i < len(padded); i++ {
		padded[i] = byte(pad)
	}
	out := make([]byte, len(padded))
	for i := 0; i < len(padded); i += block.BlockSize() {
		block.Encrypt(out[i:i+block.BlockSize()], padded[i:i+block.BlockSize()])
	}
	return out
}
