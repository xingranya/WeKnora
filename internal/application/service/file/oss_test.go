package file

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/testutil/fakedns"
	"github.com/aliyun/alibabacloud-oss-go-sdk-v2/oss"
)

type ossBucketClientStub struct {
	exists   bool
	checkErr error
	putErr   error
	putCalls int
}

func (s *ossBucketClientStub) IsBucketExist(context.Context, string, ...func(*oss.Options)) (bool, error) {
	return s.exists, s.checkErr
}

func (s *ossBucketClientStub) PutBucket(
	context.Context,
	*oss.PutBucketRequest,
	...func(*oss.Options),
) (*oss.PutBucketResult, error) {
	s.putCalls++
	return &oss.PutBucketResult{}, s.putErr
}

func TestParseOssFilePath(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantBucket  string
		wantKey     string
		wantErr     bool
		errContains string
	}{
		{
			name:       "valid path with nested key",
			input:      "oss://my-bucket/123/exports/abc123.csv",
			wantBucket: "my-bucket",
			wantKey:    "123/exports/abc123.csv",
		},
		{
			name:       "valid path with simple key",
			input:      "oss://test-bucket/key",
			wantBucket: "test-bucket",
			wantKey:    "key",
		},
		{
			name:       "valid path with deep nesting",
			input:      "oss://bucket/prefix/tenant/exports/uuid.png",
			wantBucket: "bucket",
			wantKey:    "prefix/tenant/exports/uuid.png",
		},
		{
			name:        "invalid scheme",
			input:       "s3://bucket/key",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
		{
			name:        "empty path",
			input:       "",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
		{
			name:        "bucket only no key",
			input:       "oss://bucket/",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
		{
			name:        "scheme only",
			input:       "oss://",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
		{
			name:        "no slash after bucket",
			input:       "oss://bucket",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
		{
			name:        "empty bucket name",
			input:       "oss:///some-key",
			wantErr:     true,
			errContains: "invalid OSS file path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bucket, key, err := parseOssFilePath(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("parseOssFilePath(%q) expected error, got bucket=%q key=%q", tt.input, bucket, key)
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("parseOssFilePath(%q) error = %v, want containing %q", tt.input, err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Errorf("parseOssFilePath(%q) unexpected error: %v", tt.input, err)
				return
			}
			if bucket != tt.wantBucket {
				t.Errorf("parseOssFilePath(%q) bucket = %q, want %q", tt.input, bucket, tt.wantBucket)
			}
			if key != tt.wantKey {
				t.Errorf("parseOssFilePath(%q) key = %q, want %q", tt.input, key, tt.wantKey)
			}
		})
	}
}

func TestNewOSSClient(t *testing.T) {
	fakedns.InstallDefault(t, map[string][]string{
		"oss-cn-hangzhou.aliyuncs.com": {"8.8.8.8"},
		"example.com":                  {"8.8.8.8"},
	})
	tests := []struct {
		name      string
		endpoint  string
		region    string
		accessKey string
		secretKey string
		wantErr   bool
	}{
		{
			name:      "valid parameters create client",
			endpoint:  "https://oss-cn-hangzhou.aliyuncs.com",
			region:    "cn-hangzhou",
			accessKey: "test-access-key",
			secretKey: "test-secret-key",
			wantErr:   false,
		},
		{
			name:      "custom endpoint",
			endpoint:  "https://example.com",
			region:    "cn-shanghai",
			accessKey: "ak",
			secretKey: "sk",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := newOSSClient(tt.endpoint, tt.region, tt.accessKey, tt.secretKey)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("newOSSClient() unexpected error: %v", err)
				return
			}
			if client == nil {
				t.Error("expected non-nil client")
			}
		})
	}
}

func TestNewOSSClientRejectsUnsafeEndpoint(t *testing.T) {
	if _, err := newOSSClient("http://127.0.0.1:9000", "cn-hangzhou", "ak", "sk"); err == nil {
		t.Fatal("expected loopback OSS endpoint to be rejected")
	}
}

func TestCheckOssConnectivity_InvalidEndpoint(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Should fail with an invalid/unreachable endpoint
	err := CheckOssConnectivity(ctx,
		"https://invalid-oss-endpoint-that-does-not-exist.local",
		"cn-hangzhou",
		"invalid-access-key",
		"invalid-secret-key",
		"nonexistent-bucket",
	)

	if err == nil {
		t.Error("CheckOssConnectivity with invalid endpoint should return an error")
	}
}

func TestOssEnsureBucket_NonExistent(t *testing.T) {
	client := &ossBucketClientStub{checkErr: errors.New("连接失败")}
	err := ossEnsureBucket(client, "unreachable-bucket")
	if err == nil || !strings.Contains(err.Error(), "failed to check OSS bucket") {
		t.Fatalf("ossEnsureBucket() error = %v", err)
	}
	if client.putCalls != 0 {
		t.Fatalf("PutBucket calls = %d, want 0", client.putCalls)
	}
}

func TestOssEnsureBucket_CreateFails(t *testing.T) {
	client := &ossBucketClientStub{putErr: errors.New("拒绝创建")}
	err := ossEnsureBucket(client, "missing-bucket")
	if err == nil || !strings.Contains(err.Error(), "failed to create OSS bucket") {
		t.Fatalf("ossEnsureBucket() error = %v", err)
	}
	if client.putCalls != 1 {
		t.Fatalf("PutBucket calls = %d, want 1", client.putCalls)
	}
}

func TestOssEnsureBucketAcceptsConcurrentCreateConflict(t *testing.T) {
	client := &ossBucketClientStub{putErr: &oss.ServiceError{StatusCode: http.StatusConflict}}
	if err := ossEnsureBucket(client, "concurrently-created"); err != nil {
		t.Fatalf("ossEnsureBucket() error = %v", err)
	}
}
