package provider

import "testing"

func TestIsSiliconFlowDeepSeekV4Model(t *testing.T) {
	tests := []struct {
		name  string
		model string
		want  bool
	}{
		{name: "V4", model: "deepseek-ai/DeepSeek-V4", want: true},
		{name: "V4 Flash", model: "deepseek-ai/DeepSeek-V4-Flash", want: true},
		{name: "V4 Pro 当前模型 ID", model: "deepseek-ai/DeepSeek-V4-Pro", want: true},
		{name: "Pro V4", model: "Pro/deepseek-ai/DeepSeek-V4", want: true},
		{name: "忽略首尾空格和大小写", model: "  PRO/DeepSeek-AI/deepseek-v4-flash  ", want: true},
		{name: "V3 不匹配", model: "deepseek-ai/DeepSeek-V3.1", want: false},
		{name: "相似名称不匹配", model: "vendor/deepseek-v4-flash-copy", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSiliconFlowDeepSeekV4Model(test.model); got != test.want {
				t.Fatalf("IsSiliconFlowDeepSeekV4Model(%q) = %v, want %v", test.model, got, test.want)
			}
		})
	}
}
