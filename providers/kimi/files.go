package kimi

import (
	"bytes"
	"context"
	"fmt"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/odysseythink/pantheon/core"
)

// UploadFile uploads a file to the Kimi /files API and returns an ms:// URL.
func UploadFile(ctx context.Context, client *Client, data []byte, mimeType string, purpose string) (string, error) {
	var b bytes.Buffer
	writer := multipart.NewWriter(&b)

	_ = writer.WriteField("purpose", purpose)

	exts, _ := mime.ExtensionsByType(mimeType)
	filename := "upload"
	if len(exts) > 0 {
		filename += exts[0]
	} else {
		filename += ".bin"
	}
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("kimi: create form file: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return "", fmt.Errorf("kimi: write file data: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("kimi: close multipart writer: %w", err)
	}

	var resp FileUploadResponse
	if err := client.uploadFile(ctx, "/files", &b, writer.FormDataContentType(), &resp); err != nil {
		return "", err
	}
	return fmt.Sprintf("ms://%s", resp.ID), nil
}

// UploadVideo uploads a video file and returns an ms:// URL.
func UploadVideo(ctx context.Context, client *Client, data []byte, mimeType string) (string, error) {
	if !strings.HasPrefix(mimeType, "video/") {
		return "", &core.ProviderError{Message: fmt.Sprintf("expected a video mime type, got %s", mimeType)}
	}
	return UploadFile(ctx, client, data, mimeType, "video")
}
