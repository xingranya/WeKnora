package docparser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	appLogger "github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

func TestNewMinerUReaderResolvesParseMethod(t *testing.T) {
	tests := []struct {
		name      string
		overrides map[string]string
		want      string
	}{
		{name: "default is auto", overrides: map[string]string{}, want: "auto"},
		{name: "legacy enabled becomes auto", overrides: map[string]string{"mineru_enable_ocr": "true"}, want: "auto"},
		{name: "legacy disabled becomes text", overrides: map[string]string{"mineru_enable_ocr": "false"}, want: "txt"},
		{name: "explicit method wins", overrides: map[string]string{"mineru_parse_method": "ocr", "mineru_enable_ocr": "false"}, want: "ocr"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewMinerUReader(tt.overrides)
			if reader.parseMethod != tt.want {
				t.Fatalf("parseMethod = %q, want %q", reader.parseMethod, tt.want)
			}
		})
	}
}

func TestMinerUUploadFileName(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		fileType string
		want     string
	}{
		{name: "original basename", fileName: "reports/Illustrator 研报.pdf", fileType: "pdf", want: "Illustrator 研报.pdf"},
		{
			name:     "strip multipart controls",
			fileName: "reports/\r\nIllustrator\x00 export.pdf",
			fileType: "pdf",
			want:     "Illustrator export.pdf",
		},
		{name: "append extension from type", fileName: "report", fileType: "pdf", want: "report.pdf"},
		{name: "fallback from bare type", fileType: "pdf", want: "document.pdf"},
		{name: "fallback from dotted uppercase type", fileType: ".PDF", want: "document.pdf"},
		{name: "fallback from path-only name", fileName: "/", fileType: "pdf", want: "document.pdf"},
		{name: "legacy fallback without metadata", want: "document"},
		{name: "reject unsafe fallback type", fileType: "../pdf", want: "document"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := minerUUploadFileName(tt.fileName, tt.fileType); got != tt.want {
				t.Fatalf("minerUUploadFileName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseMinerUFileParseResponse(t *testing.T) {
	tests := []struct {
		name           string
		uploadFileName string
		response       map[string]any
		wantMarkdown   string
		wantResultKey  string
	}{
		{
			name:           "match upload stem",
			uploadFileName: "Illustrator 研报.pdf",
			response: map[string]any{
				"results": map[string]any{
					"Illustrator 研报": map[string]any{"md_content": "from-stem", "images": map[string]any{}},
				},
			},
			wantMarkdown:  "from-stem",
			wantResultKey: "Illustrator 研报",
		},
		{
			name:           "legacy document key",
			uploadFileName: "document",
			response: map[string]any{
				"results": map[string]any{
					"document": map[string]any{"md_content": "legacy", "images": map[string]any{}},
				},
			},
			wantMarkdown:  "legacy",
			wantResultKey: "document",
		},
		{
			name:           "files variant",
			uploadFileName: "document.pdf",
			response: map[string]any{
				"results": map[string]any{
					"files": map[string]any{"md_content": "from-files", "images": map[string]any{}},
				},
			},
			wantMarkdown:  "from-files",
			wantResultKey: "files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			respBody, err := json.Marshal(tt.response)
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}
			gotMarkdown, _, gotKey, err := parseMinerUFileParseResponse(respBody, tt.uploadFileName)
			if err != nil {
				t.Fatalf("parseMinerUFileParseResponse() error: %v", err)
			}
			if gotMarkdown != tt.wantMarkdown {
				t.Fatalf("markdown = %q, want %q", gotMarkdown, tt.wantMarkdown)
			}
			if gotKey != tt.wantResultKey {
				t.Fatalf("result key = %q, want %q", gotKey, tt.wantResultKey)
			}
		})
	}
}

func TestMinerUReaderPreservesMultipartFilename(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() {
		utils.SetSSRFWhitelistFromRaw("")
	})

	const fileContent = "%PDF-1.7 synthetic test content"
	tests := []struct {
		name     string
		fileName string
		fileType string
		want     string
	}{
		{name: "original basename", fileName: "reports/Illustrator 研报.pdf", fileType: "pdf", want: "Illustrator 研报.pdf"},
		{
			name:     "strip multipart controls",
			fileName: "reports/\r\nIllustrator\x00 export.pdf",
			fileType: "pdf",
			want:     "Illustrator export.pdf",
		},
		{name: "fallback from bare type", fileType: "pdf", want: "document.pdf"},
		{name: "fallback from dotted uppercase type", fileType: ".PDF", want: "document.pdf"},
		{name: "fallback from path-only name", fileName: "/", fileType: "pdf", want: "document.pdf"},
		{name: "legacy fallback without metadata", want: "document"},
		{name: "reject unsafe fallback type", fileType: "../pdf", want: "document"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			type capturedRequest struct {
				method   string
				path     string
				fileName string
				content  string
				err      error
			}

			captured := make(chan capturedRequest, 1)
			uploadName := minerUUploadFileName(tt.fileName, tt.fileType)
			resultStem := minerUResultStem(uploadName)
			responseBody, err := json.Marshal(map[string]any{
				"results": map[string]any{
					resultStem: map[string]any{
						"md_content": "ok",
						"images":     map[string]any{},
					},
				},
			})
			if err != nil {
				t.Fatalf("marshal response: %v", err)
			}

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got := capturedRequest{method: r.Method, path: r.URL.Path}
				file, header, err := r.FormFile("files")
				if err != nil {
					got.err = err
				} else {
					got.fileName = header.Filename
					content, readErr := io.ReadAll(file)
					got.content = string(content)
					got.err = readErr
					_ = file.Close()
				}
				captured <- got
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(responseBody)
			}))
			defer server.Close()

			reader := NewMinerUReader(map[string]string{"mineru_endpoint": server.URL})
			result, err := reader.Read(context.Background(), &types.ReadRequest{
				FileContent: []byte(fileContent),
				FileName:    tt.fileName,
				FileType:    tt.fileType,
			})
			if err != nil {
				t.Fatalf("Read returned error: %v", err)
			}
			if result.MarkdownContent != "ok" {
				t.Fatalf("MarkdownContent = %q, want %q", result.MarkdownContent, "ok")
			}

			request := <-captured
			if request.err != nil {
				t.Fatalf("capture multipart request: %v", request.err)
			}
			if request.method != http.MethodPost {
				t.Errorf("method = %q, want %q", request.method, http.MethodPost)
			}
			if request.path != "/file_parse" {
				t.Errorf("path = %q, want %q", request.path, "/file_parse")
			}
			if request.fileName != tt.want {
				t.Errorf("multipart filename = %q, want %q", request.fileName, tt.want)
			}
			if request.content != fileContent {
				t.Errorf("multipart content = %q, want %q", request.content, fileContent)
			}
		})
	}
}

func TestNormalizeMinerUMarkdownPreservesMarkdownAndHTML(t *testing.T) {
	input := strings.Join([]string{
		"# Heading",
		"",
		"![](images/cover.jpg)",
		"",
		`<details><summary>text_image</summary>caption</details>`,
		"",
		`<table><tr><td><img src="images/profile.jpg"/></td></tr></table>`,
	}, "\n")

	got := normalizeMinerUMarkdown(input)

	if !strings.Contains(got, "# Heading") {
		t.Fatalf("expected heading to stay intact, got: %q", got)
	}
	if strings.Contains(got, `\# Heading`) {
		t.Fatalf("expected heading to avoid escaped form, got: %q", got)
	}
	if !strings.Contains(got, "![](images/cover.jpg)") {
		t.Fatalf("expected markdown image syntax to stay intact, got: %q", got)
	}
	if strings.Contains(got, `!\[](images/cover.jpg)`) {
		t.Fatalf("expected markdown image syntax to avoid escaped form, got: %q", got)
	}
	if !strings.Contains(got, `<details><summary>text_image</summary>caption</details>`) {
		t.Fatalf("expected details/summary block to be preserved, got: %q", got)
	}
	if !strings.Contains(got, `<img src="images/profile.jpg"/>`) {
		t.Fatalf("expected html img tag to be preserved, got: %q", got)
	}
}

func TestProcessImagesKeepsReferencedVariants(t *testing.T) {
	reader := &MinerUReader{}
	mdContent := strings.Join([]string{
		"![](images/cover.jpg)",
		`<img src="./images/profile.jpg"/>`,
		`![](plain.jpg)`,
	}, "\n")

	png := createTestPNG(200, 150)
	b64 := base64.StdEncoding.EncodeToString(png)
	images := map[string]string{
		"cover.jpg":   "data:image/png;base64," + b64,
		"profile.jpg": "data:image/png;base64," + b64,
		"plain.jpg":   "data:image/png;base64," + b64,
	}

	refs, gotMarkdown, err := reader.processImages(mdContent, images)
	if err != nil {
		t.Fatalf("processImages() error: %v", err)
	}

	if gotMarkdown != mdContent {
		t.Fatalf("processImages should not rewrite markdown content")
	}
	if len(refs) != 3 {
		t.Fatalf("expected 3 image refs, got %d", len(refs))
	}
}

// TestProcessImagesMatchesPathsWithSpaces guards against a regression where
// MinerU image filenames containing spaces (common on Chinese documents,
// e.g. "images/第 1 页.jpg") would be silently dropped because the markdown
// regex used to extract refs disallowed whitespace inside the URL group.
func TestProcessImagesMatchesPathsWithSpaces(t *testing.T) {
	reader := &MinerUReader{}
	mdContent := "![](images/第 1 页.jpg)"

	png := createTestPNG(200, 150)
	b64 := base64.StdEncoding.EncodeToString(png)
	images := map[string]string{
		"第 1 页.jpg": "data:image/png;base64," + b64,
	}

	refs, _, err := reader.processImages(mdContent, images)
	if err != nil {
		t.Fatalf("processImages() error: %v", err)
	}
	if len(refs) != 1 {
		t.Fatalf("expected 1 image ref for path with spaces, got %d", len(refs))
	}
	if refs[0].OriginalRef != "images/第 1 页.jpg" {
		t.Fatalf("unexpected OriginalRef: %q", refs[0].OriginalRef)
	}
}

func TestParseMinerUFileParseResponseRejectsOversizedMarkdownAndImageCount(t *testing.T) {
	oversizedMarkdown, err := json.Marshal(map[string]any{
		"results": map[string]any{
			"document": map[string]any{"md_content": strings.Repeat("x", mineruMaxMarkdownBytes+1)},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := parseMinerUFileParseResponse(oversizedMarkdown, "document.pdf"); err == nil {
		t.Fatal("expected oversized markdown error")
	}

	images := make(map[string]string, mineruMaxImageCount+1)
	for index := 0; index <= mineruMaxImageCount; index++ {
		images[fmt.Sprintf("%d.png", index)] = "eA=="
	}
	tooManyImages, err := json.Marshal(map[string]any{
		"results": map[string]any{"document": map[string]any{"images": images}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := parseMinerUFileParseResponse(tooManyImages, "document.pdf"); err == nil {
		t.Fatal("expected image count error")
	}
}

func TestProcessImagesRejectsDecodedImageLimit(t *testing.T) {
	reader := &MinerUReader{}
	encodedLen := base64.StdEncoding.EncodedLen(mineruMaxImageBytes + 1)
	images := map[string]string{"too-large.png": strings.Repeat("A", encodedLen)}
	_, _, err := reader.processImages("![](too-large.png)", images)
	if err == nil {
		t.Fatal("expected decoded image limit error")
	}
}

func TestValidateMinerUImageLimitsChecksTotalBytes(t *testing.T) {
	images := map[string]string{
		"a.png": base64.StdEncoding.EncodeToString([]byte("abc")),
		"b.png": base64.StdEncoding.EncodeToString([]byte("def")),
	}
	err := validateMinerUImageLimits(context.Background(), images, 3, 5)
	if err == nil {
		t.Fatal("expected total decoded image limit error")
	}
}

func TestProcessImagesChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := &MinerUReader{}
	_, _, err := reader.processImagesWithContext(
		ctx,
		"![](cover.png)",
		map[string]string{"cover.png": base64.StdEncoding.EncodeToString([]byte("abc"))},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("processImagesWithContext() error = %v, want context.Canceled", err)
	}
}

func captureMinerUAuditLogs(t *testing.T, output *bytes.Buffer) {
	t.Helper()
	originalFormat, hadFormat := os.LookupEnv("LOG_FORMAT")
	if err := os.Setenv("LOG_FORMAT", ""); err != nil {
		t.Fatal(err)
	}
	appLogger.ConfigureFromEnv()
	appLogger.SetOutput(output)
	t.Cleanup(func() {
		if hadFormat {
			_ = os.Setenv("LOG_FORMAT", originalFormat)
		} else {
			_ = os.Unsetenv("LOG_FORMAT")
		}
		appLogger.ConfigureFromEnv()
	})
}

func TestMinerUReaderLogsAuditContentWithoutCredentialsOrBase64(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })

	const markdownContent = "员工文档正文"
	const credential = "sk-abcdefghijk"
	const imageSecret = "SENSITIVE_IMAGE_BYTES"
	responseBody, err := json.Marshal(map[string]any{
		"results": map[string]any{
			"document": map[string]any{
				"md_content": "![](cover.png)\n" + markdownContent + " api_key=" + credential,
				"images": map[string]string{
					"cover.png": "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte(imageSecret)),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	var logs bytes.Buffer
	captureMinerUAuditLogs(t, &logs)
	reader := NewMinerUReader(map[string]string{"mineru_endpoint": server.URL})
	result, err := reader.Read(context.Background(), &types.ReadRequest{
		FileContent: []byte("test"),
		FileName:    "document.pdf",
		FileType:    "pdf",
	})
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	if len(result.ImageRefs) != 1 {
		t.Fatalf("image refs = %d, want 1", len(result.ImageRefs))
	}
	logged := logs.String()
	for _, expected := range []string{markdownContent, "cover.png", "response_bytes=", "images=1", "encoded_image_chars="} {
		if !strings.Contains(logged, expected) {
			t.Fatalf("MinerU logs missing audit field %q: %s", expected, logged)
		}
	}
	for _, forbidden := range []string{credential, imageSecret, "base64,"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("MinerU logs expose forbidden payload %q: %s", forbidden, logged)
		}
	}
}

func TestMinerUReaderLogsSanitizedErrorBodyWithoutReturningIt(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	const responseDetail = "解析服务内部错误"
	const credential = "sk-abcdefghijk"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(responseDetail + " api_key=" + credential))
	}))
	defer server.Close()

	var logs bytes.Buffer
	captureMinerUAuditLogs(t, &logs)
	reader := NewMinerUReader(map[string]string{"mineru_endpoint": server.URL})
	_, err := reader.Read(context.Background(), &types.ReadRequest{
		FileContent: []byte("test"),
		FileName:    "document.pdf",
		FileType:    "pdf",
	})
	if err == nil {
		t.Fatal("expected MinerU status error")
	}
	if strings.Contains(err.Error(), responseDetail) || strings.Contains(err.Error(), credential) {
		t.Fatalf("error exposes MinerU response body: %v", err)
	}
	if !strings.Contains(logs.String(), responseDetail) || strings.Contains(logs.String(), credential) {
		t.Fatalf("MinerU error audit log boundary is wrong: %s", logs.String())
	}
}
