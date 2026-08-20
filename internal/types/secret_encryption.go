package types

import (
	"fmt"

	"github.com/Tencent/WeKnora/internal/utils"
)

var encryptAESGCMForStorage = utils.EncryptAESGCM

// encryptSecretForStorage 统一秘密字段的落库加密语义。
// 开发和测试环境未配置密钥时保留历史明文兼容；配置有效密钥后，加密错误必须返回调用方。
func encryptSecretForStorage(plaintext, field string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	key := utils.GetAESKey()
	if key == nil {
		return plaintext, nil
	}
	encrypted, err := encryptAESGCMForStorage(plaintext, key)
	if err != nil {
		return "", fmt.Errorf("encrypt %s: %w", field, err)
	}
	return encrypted, nil
}
