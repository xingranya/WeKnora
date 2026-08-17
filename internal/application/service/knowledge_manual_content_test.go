package service

import (
	"strings"
	"testing"
)

func TestValidateManualContentLength(t *testing.T) {
	t.Run("普通 Markdown 在限制内", func(t *testing.T) {
		if err := validateManualContentLength("# 标题\n\n正文"); err != nil {
			t.Fatalf("validateManualContentLength() error = %v", err)
		}
	})

	t.Run("超长纯文本仍被拒绝", func(t *testing.T) {
		if err := validateManualContentLength(strings.Repeat("文", manualContentMaxLength+1)); err == nil {
			t.Fatal("validateManualContentLength() 应拒绝超长纯文本")
		}
	})

	t.Run("内嵌图片不计入文本字符限制", func(t *testing.T) {
		content := "# 图文文档\n\n![流程图](data:image/png;base64," + strings.Repeat("A", manualContentMaxLength+1) + ")"
		if err := validateManualContentLength(content); err != nil {
			t.Fatalf("validateManualContentLength() error = %v", err)
		}
	})

	t.Run("请求总大小仍有硬上限", func(t *testing.T) {
		content := "![大图](data:image/png;base64," + strings.Repeat("A", manualContentMaxBytes) + ")"
		if err := validateManualContentLength(content); err == nil {
			t.Fatal("validateManualContentLength() 应拒绝超过总大小上限的请求")
		}
	})
}
