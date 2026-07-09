package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"reasonix/internal/bot"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

const maxOutboundMediaBytes = 25 * 1024 * 1024

// sendMediaURLs uploads each OutboundMessage.MediaURLs entry and sends it as an
// image/file message. Refs are resolved under a strict, off-by-default policy:
// only absolute local paths contained in a configured root are accepted (see
// readOutboundFile). Anything rejected is skipped with a warning. URL media is
// intentionally not fetched here — pulling a caller-supplied URL from inside the
// gateway is an SSRF sink with no safe static-analysis story; the /send caller
// should stage remote media to an allow-listed root instead.
func (a *adapter) sendMediaURLs(ctx context.Context, msg bot.OutboundMessage) (bot.SendResult, error) {
	var result bot.SendResult
	var firstErr error
	for _, ref := range msg.MediaURLs {
		res, err := a.sendOneMedia(ctx, msg, ref)
		if err != nil {
			a.logger.Warn("feishu media send rejected or failed", "err", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		result = res
	}
	return result, firstErr
}

func (a *adapter) sendOneMedia(ctx context.Context, msg bot.OutboundMessage, ref string) (bot.SendResult, error) {
	data, name, err := a.readOutboundFile(ref)
	if err != nil {
		return bot.SendResult{}, err
	}
	mimeType := http.DetectContentType(data[:min(len(data), 512)])
	if strings.HasPrefix(mimeType, "image/") {
		imageKey, err := a.uploadImage(ctx, data)
		if err == nil {
			content, _ := json.Marshal(map[string]string{"image_key": imageKey})
			return a.sendSDKContent(ctx, msg, larkim.MsgTypeImage, string(content))
		}
		a.logger.Warn("feishu image upload failed; falling back to file", "err", err)
	}
	fileKey, err := a.uploadFile(ctx, name, data)
	if err != nil {
		return bot.SendResult{}, err
	}
	content, _ := json.Marshal(map[string]string{"file_key": fileKey})
	return a.sendSDKContent(ctx, msg, larkim.MsgTypeFile, string(content))
}

// readOutboundFile reads a media file for outbound sending under a strict
// policy. The ref must be an absolute path whose cleaned form is contained in
// one of the configured OutboundMediaRoots (empty by default → disabled, so an
// authenticated /send caller cannot read arbitrary files). The containment
// check is inline on the cleaned path that is then read; a separate
// symlink-resolved recheck rejects a symlink inside a root that points out of
// it before any read happens.
func (a *adapter) readOutboundFile(ref string) ([]byte, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, "", fmt.Errorf("feishu outbound media: empty ref")
	}
	if len(a.cfg.OutboundMediaRoots) == 0 {
		return nil, "", fmt.Errorf("feishu outbound media: local file sending is disabled (set outbound_media_roots)")
	}
	clean := filepath.Clean(ref)
	if !filepath.IsAbs(clean) {
		return nil, "", fmt.Errorf("feishu outbound media: path must be absolute")
	}
	// Inline containment barrier: `clean` must sit under a configured root before
	// it is stat'd/read. Keeping the HasPrefix check in this function (not a
	// helper) is what lets static analysis treat it as a path-injection sanitizer
	// guarding the reads below.
	matchedRoot := ""
	for _, root := range a.cfg.OutboundMediaRoots {
		root = filepath.Clean(strings.TrimSpace(root))
		if root == "" || root == "." {
			continue
		}
		if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
			matchedRoot = root
			break
		}
	}
	if matchedRoot == "" {
		return nil, "", fmt.Errorf("feishu outbound media: path is outside the allow-listed roots")
	}
	// Defense in depth: reject a symlink under the root that resolves outside it,
	// before any read. We still read `clean` (the value guarded above).
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		if rootResolved, err2 := filepath.EvalSymlinks(matchedRoot); err2 == nil {
			if resolved != rootResolved && !strings.HasPrefix(resolved, rootResolved+string(filepath.Separator)) {
				return nil, "", fmt.Errorf("feishu outbound media: path escapes its root via a symlink")
			}
		}
	}
	info, err := os.Stat(clean)
	if err != nil {
		return nil, "", err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxOutboundMediaBytes {
		return nil, "", fmt.Errorf("feishu outbound media: must be a regular file between 1 byte and 25 MB")
	}
	raw, err := os.ReadFile(clean)
	if err != nil {
		return nil, "", err
	}
	return raw, filepath.Base(clean), nil
}

func (a *adapter) uploadImage(ctx context.Context, data []byte) (string, error) {
	client, err := a.sdkClient()
	if err != nil {
		return "", err
	}
	var key string
	err = withTransientRetry(ctx, a.logger, "upload image", func(ctx context.Context) error {
		req := larkim.NewCreateImageReqBuilder().
			Body(larkim.NewCreateImageReqBodyBuilder().
				ImageType(larkim.CreateImageImageTypeMessage).
				Image(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.Image.Create(ctx, req)
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("feishu image upload error: empty response")
		}
		if !resp.Success() {
			return fmt.Errorf("feishu image upload error: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		if resp.Data == nil || resp.Data.ImageKey == nil {
			return fmt.Errorf("feishu image upload error: missing image key")
		}
		key = *resp.Data.ImageKey
		return nil
	})
	return key, err
}

func (a *adapter) uploadFile(ctx context.Context, name string, data []byte) (string, error) {
	client, err := a.sdkClient()
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(name) == "" {
		name = "media.bin"
	}
	var key string
	err = withTransientRetry(ctx, a.logger, "upload file", func(ctx context.Context) error {
		req := larkim.NewCreateFileReqBuilder().
			Body(larkim.NewCreateFileReqBodyBuilder().
				FileType(feishuFileType(name)).
				FileName(name).
				File(bytes.NewReader(data)).
				Build()).
			Build()
		resp, err := client.Im.File.Create(ctx, req)
		if err != nil {
			return err
		}
		if resp == nil {
			return fmt.Errorf("feishu file upload error: empty response")
		}
		if !resp.Success() {
			return fmt.Errorf("feishu file upload error: %s", feishuCodeError(resp.Code, resp.Msg))
		}
		if resp.Data == nil || resp.Data.FileKey == nil {
			return fmt.Errorf("feishu file upload error: missing file key")
		}
		key = *resp.Data.FileKey
		return nil
	})
	return key, err
}

func feishuFileType(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf":
		return "pdf"
	case ".doc", ".docx":
		return "doc"
	case ".xls", ".xlsx":
		return "xls"
	case ".ppt", ".pptx":
		return "ppt"
	case ".mp4":
		return "mp4"
	case ".opus":
		return "opus"
	default:
		return "stream"
	}
}
