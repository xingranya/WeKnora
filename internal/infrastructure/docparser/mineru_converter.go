package docparser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

const (
	mineruTimeout          = 1000 * time.Second
	mineruMaxResponseBytes = int64(128 * 1024 * 1024)
	mineruMaxErrorBytes    = int64(1024 * 1024)
	mineruMaxMarkdownBytes = 32 * 1024 * 1024
	mineruMaxImageCount    = 256
	mineruMaxImageBytes    = 16 * 1024 * 1024
	mineruMaxDecodedImages = 128 * 1024 * 1024
)

var (
	b64DataURIPattern     = regexp.MustCompile(`^data:image/(\w+);base64,(.+)$`)
	minerUFileTypePattern = regexp.MustCompile(`^[a-z0-9]+$`)
)

// MinerUReader calls a self-hosted MinerU API to read/convert documents.
type MinerUReader struct {
	endpoint      string
	backend       string // "pipeline", "vlm-*", "hybrid-*"
	vlmServerURL  string // vLLM server URL for vlm-http-client / hybrid-http-client
	formulaEnable bool
	tableEnable   bool
	parseMethod   string
	language      string
}

// NewMinerUReader creates a reader from ParserEngineOverrides.
func NewMinerUReader(overrides map[string]string) *MinerUReader {
	var legacyOCREnabled *bool
	if raw, ok := overrides["mineru_enable_ocr"]; ok {
		value := parseBoolOr(raw, true)
		legacyOCREnabled = &value
	}

	c := &MinerUReader{
		endpoint:      strings.TrimRight(overrides["mineru_endpoint"], "/"),
		backend:       stringOr(overrides["mineru_model"], "pipeline"),
		vlmServerURL:  overrides["mineru_vlm_server_url"],
		formulaEnable: parseBoolOr(overrides["mineru_enable_formula"], true),
		tableEnable:   parseBoolOr(overrides["mineru_enable_table"], true),
		parseMethod:   types.ResolveMinerUParseMethod(overrides["mineru_parse_method"], legacyOCREnabled),
		language:      stringOr(overrides["mineru_language"], "ch"),
	}
	return c
}

func (c *MinerUReader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	if c.endpoint == "" {
		return &types.ReadResult{Error: "MinerU endpoint is not configured"}, nil
	}
	if err := validateMinerUOutboundURL(c.endpoint); err != nil {
		return &types.ReadResult{Error: err.Error()}, nil
	}
	if c.vlmServerURL != "" {
		if err := validateMinerUOutboundURL(c.vlmServerURL); err != nil {
			return &types.ReadResult{Error: err.Error()}, nil
		}
	}

	if len(req.FileContent) == 0 && req.FileReader == nil {
		return &types.ReadResult{Error: "no file content provided"}, nil
	}

	size := req.FileSize
	if size <= 0 {
		size = int64(len(req.FileContent))
	}
	logger.Infof(context.Background(), "[MinerU] Parsing file: size=%d type=%s", size, req.FileType)

	mdContent, imagesB64, err := c.callFileParse(ctx, req, req.FileName, req.FileType)
	if err != nil {
		return nil, fmt.Errorf("MinerU file_parse: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// MinerU already returns markdown with embedded HTML blocks (e.g. <table>, <details>).
	// Re-running the whole document through html-to-markdown corrupts valid markdown
	// by escaping headings and image syntax. Only apply narrow compatibility fixes.
	mdContent = normalizeMinerUMarkdown(mdContent)

	// Process images: decode base64, build ImageRef list, replace refs in markdown
	imageRefs, mdContent, err := c.processImagesWithContext(ctx, mdContent, imagesB64)
	if err != nil {
		return nil, err
	}

	mdContent, imageRefs = ensureOriginalImageRef(req, mdContent, imageRefs)

	logger.Infof(context.Background(), "[MinerU] Parsed successfully, markdown=%d chars, images=%d", len(mdContent), len(imageRefs))

	return &types.ReadResult{
		MarkdownContent: mdContent,
		ImageRefs:       imageRefs,
	}, nil
}

type mineruFileEntry struct {
	MDContent string            `json:"md_content"`
	Images    map[string]string `json:"images"` // path -> "data:image/png;base64,..." or raw base64
}

func minerUCleanFileType(fileType string) string {
	cleanType := strings.ToLower(strings.TrimSpace(fileType))
	cleanType = strings.TrimPrefix(cleanType, ".")
	if minerUFileTypePattern.MatchString(cleanType) {
		return cleanType
	}
	return ""
}

func minerUUploadFileName(fileName, fileType string) string {
	cleanName := strings.TrimSpace(fileName)
	cleanName = strings.ReplaceAll(cleanName, `\`, "/")
	cleanName = path.Base(cleanName)
	cleanName = strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == 0 {
			return -1
		}
		return r
	}, cleanName)
	cleanName = strings.TrimSpace(cleanName)
	if cleanName != "" && cleanName != "." && cleanName != ".." && cleanName != "/" {
		if filepath.Ext(cleanName) == "" {
			if cleanType := minerUCleanFileType(fileType); cleanType != "" {
				return cleanName + "." + cleanType
			}
		}
		return cleanName
	}

	if cleanType := minerUCleanFileType(fileType); cleanType != "" {
		return "document." + cleanType
	}
	return "document"
}

// minerUResultStem mirrors MinerU's upload.stem: the basename without extension.
func minerUResultStem(uploadFileName string) string {
	stem := strings.TrimSuffix(path.Base(uploadFileName), filepath.Ext(uploadFileName))
	if stem == "" || stem == "." {
		return ""
	}
	return stem
}

func minerUResultLookupKeys(uploadFileName string) []string {
	keys := make([]string, 0, 3)
	if stem := minerUResultStem(uploadFileName); stem != "" {
		keys = append(keys, stem)
	}
	return append(keys, "document", "files")
}

func parseMinerUFileParseResponse(respBody []byte, uploadFileName string) (string, map[string]string, string, error) {
	return parseMinerUFileParseResponseContext(context.Background(), respBody, uploadFileName)
}

func parseMinerUFileParseResponseContext(
	ctx context.Context,
	respBody []byte,
	uploadFileName string,
) (string, map[string]string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, "", err
	}
	var envelope struct {
		Results map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(respBody, &envelope); err != nil {
		return "", nil, "", fmt.Errorf("decode response: %w", err)
	}
	if len(envelope.Results) == 0 {
		return "", nil, "", nil
	}

	keys := minerUResultLookupKeys(uploadFileName)
	for key := range envelope.Results {
		if err := ctx.Err(); err != nil {
			return "", nil, "", err
		}
		matched := false
		for _, preferred := range keys {
			if key == preferred {
				matched = true
				break
			}
		}
		if !matched {
			keys = append(keys, key)
		}
	}
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return "", nil, "", err
		}
		raw, ok := envelope.Results[key]
		if !ok {
			continue
		}
		var entry mineruFileEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return "", nil, "", fmt.Errorf("decode results.%s: %w", key, err)
		}
		if len(entry.MDContent) > mineruMaxMarkdownBytes {
			return "", nil, "", fmt.Errorf("MinerU markdown exceeds %dMB limit", mineruMaxMarkdownBytes/(1024*1024))
		}
		if len(entry.Images) > mineruMaxImageCount {
			return "", nil, "", fmt.Errorf("MinerU image count exceeds %d limit", mineruMaxImageCount)
		}
		if err := validateMinerUImageLimits(
			ctx,
			entry.Images,
			mineruMaxImageBytes,
			mineruMaxDecodedImages,
		); err != nil {
			return "", nil, "", err
		}
		if entry.MDContent != "" || len(entry.Images) > 0 {
			return entry.MDContent, entry.Images, key, nil
		}
	}
	return "", nil, "", nil
}

func (c *MinerUReader) callFileParse(
	ctx context.Context,
	request *types.ReadRequest,
	fileName string,
	fileType string,
) (string, map[string]string, error) {
	pipeReader, pipeWriter := io.Pipe()
	writer := multipart.NewWriter(pipeWriter)
	contentType := writer.FormDataContentType()

	// Form fields
	fields := map[string]string{
		"return_md":           "true",
		"return_images":       "true",
		"table_enable":        fmt.Sprintf("%v", c.tableEnable),
		"formula_enable":      fmt.Sprintf("%v", c.formulaEnable),
		"parse_method":        c.parseMethod,
		"start_page_id":       "0",
		"end_page_id":         "99999",
		"backend":             c.backend,
		"response_format_zip": "false",
		"return_middle_json":  "false",
		"return_model_output": "false",
		"return_content_list": "true",
	}
	if c.language != "" {
		fields["lang_list"] = c.language
	}
	if c.vlmServerURL != "" && (strings.HasPrefix(c.backend, "vlm-http-client") || strings.HasPrefix(c.backend, "hybrid-http-client")) {
		fields["server_url"] = c.vlmServerURL
	}
	uploadFileName := minerUUploadFileName(fileName, fileType)
	go func() {
		var writeErr error
		defer func() { _ = pipeWriter.CloseWithError(writeErr) }()
		defer writer.Close()
		for k, v := range fields {
			if writeErr = writer.WriteField(k, v); writeErr != nil {
				return
			}
		}
		part, err := writer.CreateFormFile("files", uploadFileName)
		if err != nil {
			writeErr = err
			return
		}
		source := request.FileReader
		if source == nil {
			source = bytes.NewReader(request.FileContent)
		}
		_, writeErr = io.Copy(part, source)
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/file_parse", pipeReader)
	if err != nil {
		return "", nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", contentType)

	client := utils.NewSSRFSafeHTTPClient(utils.SSRFSafeHTTPClientConfig{
		Timeout:      mineruTimeout,
		MaxRedirects: 5,
	})
	resp, err := client.Do(httpReq)
	if err != nil {
		return "", nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, mineruMaxErrorBytes))
		logger.Errorf(ctx, "[MinerU] API error: status=%d response=%q",
			resp.StatusCode, logger.AuditText(string(respBody), 4096))
		return "", nil, fmt.Errorf(
			"MinerU API status %d (response_bytes=%d)",
			resp.StatusCode,
			len(respBody),
		)
	}

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, mineruMaxResponseBytes+1))
	if err != nil {
		return "", nil, fmt.Errorf("read response body: %w", err)
	}
	if int64(len(respBody)) > mineruMaxResponseBytes {
		return "", nil, fmt.Errorf("MinerU response exceeds %dMB limit", mineruMaxResponseBytes/(1024*1024))
	}

	mdContent, imagesB64, resultKey, err := parseMinerUFileParseResponseContext(ctx, respBody, uploadFileName)
	if err != nil {
		return "", nil, err
	}
	if resultKey != "" {
		var encodedImageChars int64
		imagePaths := make([]string, 0, len(imagesB64))
		for imagePath, encoded := range imagesB64 {
			encodedImageChars += int64(len(encoded))
			imagePaths = append(imagePaths, imagePath)
		}
		logger.Infof(
			ctx,
			"[MinerU] Response parsed: response_bytes=%d markdown=%q image_paths=%q images=%d encoded_image_chars=%d",
			len(respBody),
			logger.AuditText(mdContent, 8192),
			logger.AuditText(strings.Join(imagePaths, ","), 4096),
			len(imagesB64),
			encodedImageChars,
		)
		return mdContent, imagesB64, nil
	}

	logger.Errorf(context.Background(), "[MinerU] Response has no markdown/images under results")
	return "", nil, nil
}

// processImages decodes base64 images from MinerU response and returns ImageRef list.
// It also replaces image references in the markdown content.
func (c *MinerUReader) processImages(mdContent string, imagesB64 map[string]string) ([]types.ImageRef, string, error) {
	return c.processImagesWithContext(context.Background(), mdContent, imagesB64)
}

func (c *MinerUReader) processImagesWithContext(
	ctx context.Context,
	mdContent string,
	imagesB64 map[string]string,
) ([]types.ImageRef, string, error) {
	if len(imagesB64) > mineruMaxImageCount {
		return nil, mdContent, fmt.Errorf("MinerU image count exceeds %d limit", mineruMaxImageCount)
	}
	if err := validateMinerUImageLimits(
		ctx,
		imagesB64,
		mineruMaxImageBytes,
		mineruMaxDecodedImages,
	); err != nil {
		return nil, mdContent, err
	}
	var refs []types.ImageRef
	var decodedTotal int64

	for ipath, b64Str := range imagesB64 {
		if err := ctx.Err(); err != nil {
			return nil, mdContent, err
		}
		matchedRefs := mineruImageOriginalRefs(mdContent, ipath)
		if len(matchedRefs) == 0 {
			continue
		}

		var imgBytes []byte
		var ext string
		encoded := b64Str

		if m := b64DataURIPattern.FindStringSubmatch(b64Str); len(m) == 3 {
			ext = m[1]
			encoded = m[2]
		} else {
			ext = strings.TrimPrefix(filepath.Ext(ipath), ".")
			if ext == "" {
				ext = "png"
			}
		}
		decodedSize := int64(base64.StdEncoding.DecodedLen(len(encoded)))
		if decodedSize > mineruMaxImageBytes {
			return nil, mdContent, fmt.Errorf("MinerU image %q exceeds %dMB decoded limit", ipath, mineruMaxImageBytes/(1024*1024))
		}
		if decodedTotal > mineruMaxDecodedImages-decodedSize {
			return nil, mdContent, fmt.Errorf("MinerU decoded images exceed %dMB total limit", mineruMaxDecodedImages/(1024*1024))
		}
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, mdContent, fmt.Errorf("decode MinerU image %q: %w", ipath, err)
		}
		imgBytes = decoded
		decodedTotal += int64(len(imgBytes))

		mimeType := mime.TypeByExtension("." + ext)
		if mimeType == "" {
			mimeType = "image/png"
		}

		for _, originalRef := range matchedRefs {
			if err := ctx.Err(); err != nil {
				return nil, mdContent, err
			}
			refs = append(refs, types.ImageRef{
				Filename:    ipath,
				OriginalRef: originalRef,
				MimeType:    mimeType,
				ImageData:   imgBytes,
			})
		}
	}

	return refs, mdContent, nil
}

func validateMinerUImageLimits(
	ctx context.Context,
	images map[string]string,
	maxImageBytes int64,
	maxTotalBytes int64,
) error {
	var decodedTotal int64
	for _, value := range images {
		if err := ctx.Err(); err != nil {
			return err
		}
		encoded := value
		if match := b64DataURIPattern.FindStringSubmatch(value); len(match) == 3 {
			encoded = match[2]
		}
		decodedSize := int64(base64.StdEncoding.DecodedLen(len(encoded)))
		if decodedSize > maxImageBytes {
			return fmt.Errorf("MinerU image exceeds %dMB decoded limit", maxImageBytes/(1024*1024))
		}
		if decodedTotal > maxTotalBytes-decodedSize {
			return fmt.Errorf("MinerU decoded images exceed %dMB total limit", maxTotalBytes/(1024*1024))
		}
		decodedTotal += decodedSize
	}
	return nil
}

// validateMinerUOutboundURL rejects MinerU endpoints that would reach private
// or otherwise restricted hosts when parsed or probed from the app server.
func validateMinerUOutboundURL(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil
	}
	if err := utils.ValidateURLForSSRF(rawURL); err != nil {
		return fmt.Errorf("MinerU URL blocked by SSRF check: %v", err)
	}
	return nil
}

// PingMinerU checks if the self-hosted MinerU service is reachable.
func PingMinerU(endpoint string) (bool, string) {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		return false, "未配置 MinerU 端点"
	}
	if err := validateMinerUOutboundURL(endpoint); err != nil {
		return false, err.Error()
	}
	client := utils.NewSSRFSafeHTTPClient(utils.SSRFSafeHTTPClientConfig{
		Timeout:      5 * time.Second,
		MaxRedirects: 5,
	})
	resp, err := client.Get(endpoint + "/docs")
	if err != nil {
		return false, fmt.Sprintf("MinerU 服务不可达: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 400 {
		return false, fmt.Sprintf("MinerU 服务返回状态 %d", resp.StatusCode)
	}
	return true, ""
}

// escapedImageSyntaxPattern matches markdown image references whose `[` was
// over-escaped to `\[` by html-to-markdown. The URL group mirrors the
// downstream image-extraction regex so escapes are only stripped for actual
// image references.
var escapedImageSyntaxPattern = regexp.MustCompile(`!\\\[(.*?)\\?\]\(([^()\n]*(?:\([^)]*\)[^()\n]*)*)\)`)

// escapedHeadingPattern restores markdown headings that were over-escaped to
// \# Heading. We only touch line-leading heading markers to avoid rewriting
// body text that intentionally contains escaped # characters.
var escapedHeadingPattern = regexp.MustCompile(`(?m)^\\(#{1,6})(\s+)`)

// unescapeMarkdownImageSyntax restores `![alt](url)` from html-to-markdown's
// over-escaped `!\[alt\](url)` form. Without this, the downstream image regex
// in ImageResolver fails to match and images are never persisted.
func unescapeMarkdownImageSyntax(content string) string {
	return escapedImageSyntaxPattern.ReplaceAllString(content, "![$1]($2)")
}

func normalizeEscapedMarkdownHeadings(content string) string {
	return escapedHeadingPattern.ReplaceAllString(content, `$1$2`)
}

func normalizeMinerUMarkdown(content string) string {
	content = unescapeMarkdownImageSyntax(content)
	content = normalizeEscapedMarkdownHeadings(content)
	return content
}

func mineruImageOriginalRefs(mdContent, imagePath string) []string {
	normalizedTarget := normalizeMinerUImagePath(imagePath)
	if normalizedTarget == "" {
		return nil
	}

	referenced := extractImageRefsFromContent(mdContent)
	seen := make(map[string]struct{}, len(referenced))
	var matched []string
	for _, refPath := range referenced {
		if normalizeMinerUImagePath(refPath) != normalizedTarget {
			continue
		}
		if _, ok := seen[refPath]; ok {
			continue
		}
		matched = append(matched, refPath)
		seen[refPath] = struct{}{}
	}

	return matched
}

// imgMarkdownPatternAllowSpaces matches markdown image syntax while allowing
// spaces in the URL group, so that paths like "images/第 1 页.jpg" produced by
// MinerU on Chinese documents are still detected as image references.
var imgMarkdownPatternAllowSpaces = regexp.MustCompile(
	`!\[(.*?)\]\(([^()\n]*(?:\([^)]*\)[^()\n]*)*)\)`,
)

func extractImageRefsFromContent(content string) []string {
	var refs []string

	for _, match := range imgMarkdownPatternAllowSpaces.FindAllStringSubmatch(content, -1) {
		if len(match) >= 3 {
			refs = append(refs, strings.TrimSpace(match[2]))
		}
	}
	for _, match := range imgHTMLSrc.FindAllStringSubmatch(content, -1) {
		if len(match) >= 3 {
			refs = append(refs, match[2])
		}
	}

	return refs
}

func normalizeMinerUImagePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if decoded, err := url.PathUnescape(p); err == nil && decoded != "" {
		p = decoded
	}
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	p = strings.TrimPrefix(p, "images/")
	return p
}
