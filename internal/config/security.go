package config

import (
	"fmt"
	"strings"
)

const (
	systemAESKeyBytes    = 32
	minJWTSecretBytes    = 32
	minSecretUniqueBytes = 8

	// MinimumProductionMigrationVersion 是本版本标准版生产服务可安全运行的最低迁移版本。
	MinimumProductionMigrationVersion uint = 87

	legacyDefaultSystemAESKey = "weknora-system-aes-key-32bytes!!"
	legacyDefaultJWTSecret    = "weknora-jwt-secret"
)

// NormalizeGinModeForStartup 统一校验运行模式，避免拼写错误退化到 debug 并绕过生产门禁。
func NormalizeGinModeForStartup(ginMode string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(ginMode))
	if mode == "" {
		return "debug", nil
	}
	switch mode {
	case "debug", "test", "release":
		return mode, nil
	default:
		return "", fmt.Errorf("GIN_MODE must be one of debug, test, or release")
	}
}

// IsStandardProduction 判断当前进程是否属于标准版生产服务。
func IsStandardProduction(edition, ginMode string) bool {
	mode, err := NormalizeGinModeForStartup(ginMode)
	return err == nil && strings.EqualFold(strings.TrimSpace(edition), "standard") && mode == "release"
}

// ValidateSystemAESKeyForStartup 校验标准版生产服务的数据库秘密主密钥。
// 本地 debug、单元测试和 Lite 桌面版保留未配置密钥时的历史兼容行为。
func ValidateSystemAESKeyForStartup(edition, ginMode, key string) error {
	if _, err := NormalizeGinModeForStartup(ginMode); err != nil {
		return err
	}
	if !IsStandardProduction(edition, ginMode) {
		return nil
	}
	if key == "" {
		return fmt.Errorf("SYSTEM_AES_KEY is required for standard production startup")
	}
	if len([]byte(key)) != systemAESKeyBytes {
		return fmt.Errorf("SYSTEM_AES_KEY must be exactly %d bytes for standard production startup (got %d bytes)",
			systemAESKeyBytes, len([]byte(key)))
	}
	if key == legacyDefaultSystemAESKey {
		return fmt.Errorf("SYSTEM_AES_KEY must not use the legacy public default in standard production")
	}
	if hasObviouslyLowEntropy(key) {
		return fmt.Errorf("SYSTEM_AES_KEY must not use an obvious low-entropy or placeholder value in standard production")
	}
	return nil
}

// ValidateProductionStartupConfig 对标准版生产环境执行完整的启动前门禁。
// 返回规范化后的 Gin 模式，调用方应直接使用该值设置 Gin，避免校验和运行语义漂移。
func ValidateProductionStartupConfig(
	edition, ginMode, systemAESKey, jwtSecret, autoMigrate, autoRecoverDirty string,
) (string, error) {
	mode, err := NormalizeGinModeForStartup(ginMode)
	if err != nil {
		return "", err
	}
	if !IsStandardProduction(edition, mode) {
		return mode, nil
	}
	if err := ValidateSystemAESKeyForStartup(edition, mode, systemAESKey); err != nil {
		return "", err
	}

	jwtSecret = strings.TrimSpace(jwtSecret)
	if len([]byte(jwtSecret)) < minJWTSecretBytes {
		return "", fmt.Errorf("JWT_SECRET must be at least %d bytes for standard production startup", minJWTSecretBytes)
	}
	if jwtSecret == legacyDefaultJWTSecret {
		return "", fmt.Errorf("JWT_SECRET must not use the public example value in standard production")
	}
	if hasObviouslyLowEntropy(jwtSecret) {
		return "", fmt.Errorf("JWT_SECRET must not use an obvious low-entropy or placeholder value in standard production")
	}
	if autoMigrate != "false" {
		return "", fmt.Errorf("AUTO_MIGRATE must be explicitly set to false in standard production; run a bounded migration job before startup")
	}
	if autoRecoverDirty != "false" {
		return "", fmt.Errorf("AUTO_RECOVER_DIRTY must be explicitly set to false in standard production")
	}
	return mode, nil
}

// hasObviouslyLowEntropy 只拦截可明确判定的占位值或短模式重复值。
// 这不是密码强度估算器，目的是防止示例值、单字符填充和可见重复模式进入生产。
func hasObviouslyLowEntropy(secret string) bool {
	lower := strings.ToLower(secret)
	for _, marker := range []string{
		"changeme", "change-me", "password", "example", "placeholder", "your-secret", "default-secret",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}

	unique := make(map[byte]struct{}, len(secret))
	for i := 0; i < len(secret); i++ {
		unique[secret[i]] = struct{}{}
	}
	if len(unique) < minSecretUniqueBytes {
		return true
	}

	for patternLength := 1; patternLength <= 8 && patternLength*2 <= len(secret); patternLength++ {
		if len(secret)%patternLength != 0 {
			continue
		}
		pattern := secret[:patternLength]
		if strings.Repeat(pattern, len(secret)/patternLength) == secret {
			return true
		}
	}
	return false
}

// ValidateProductionMigrationState 阻止标准版生产服务在迁移缺失或 dirty 时继续启动。
func ValidateProductionMigrationState(
	edition, ginMode, dbDriver string,
	version uint,
	dirty, known bool,
) error {
	if !IsStandardProduction(edition, ginMode) || !strings.EqualFold(strings.TrimSpace(dbDriver), "postgres") {
		return nil
	}
	if !known {
		return fmt.Errorf("production database migration state is unavailable")
	}
	if dirty {
		return fmt.Errorf("production database migration version %d is dirty", version)
	}
	if version != MinimumProductionMigrationVersion {
		return fmt.Errorf(
			"production database migration version %d does not match required version %d",
			version,
			MinimumProductionMigrationVersion,
		)
	}
	return nil
}
