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
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const cdnBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"

const mediaTimeout = 30 * time.Second

// uploadImage 上传一张图片，返回可直接放进 item_list 的 image item。
// 整个流程与 615 的 uploadMediaBuffer + buildMediaItem 逐字段对齐。
func (c *Client) uploadImage(ctx context.Context, data []byte, toUserID, fileName string) (outItem, error) {
	item := outItem{Type: 2}
	if len(data) == 0 {
		return item, fmt.Errorf("待发送图片内容为空")
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
		"media_type":    1, // image
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
	item.ImageItem = &outImageItem{
		Media: outMedia{
			EncryptQueryParam: downloadParam,
			AesKey:            base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(aesKey))),
			EncryptType:       1,
		},
		MidSize: len(ciphertext),
		HdSize:  len(ciphertext),
	}
	_ = fileName
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
