package file

import (
	"context"
	"errors"
	"io"
	"mime/multipart"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

type catalogStub struct {
	resource      *types.StoredResource
	ref           string
	registerCalls int
	resolveErr    error
}

func (c *catalogStub) Register(
	_ context.Context,
	tenantID uint64,
	physicalPath string,
	meta interfaces.ResourceRegistration,
) (string, error) {
	c.registerCalls++
	c.resource = &types.StoredResource{
		ID:           "resource-1",
		Handle:       "AbCdEfGhIjKlMnOpQrStUv",
		TenantID:     tenantID,
		PhysicalPath: physicalPath,
		OriginalName: meta.OriginalName,
	}
	c.ref = types.BuildResourcePath(c.resource.Handle)
	return c.ref, nil
}

func (c *catalogStub) Resolve(_ context.Context, _ string) (*types.StoredResource, error) {
	return c.resource, nil
}

func (c *catalogStub) ResolvePath(_ context.Context, value string) (string, *types.StoredResource, error) {
	if c.resolveErr != nil {
		return "", nil, c.resolveErr
	}
	if value == c.ref {
		return c.resource.PhysicalPath, c.resource, nil
	}
	return value, nil, nil
}
func (c *catalogStub) Bind(context.Context, string, string, string, string) error { return nil }
func (c *catalogStub) MarkDeleted(context.Context, string) error                  { return nil }
func (c *catalogStub) CreateAccessGrant(context.Context, string, time.Duration) (string, error) {
	return "GrantTokenAbCdEfGhIjKl", nil
}

func (c *catalogStub) ResolveAccessGrant(context.Context, string) (*types.StoredResource, error) {
	return c.resource, nil
}

type physicalFileStub struct {
	savedPath   string
	readPath    string
	writePath   string
	deleteCalls int
}

func (s *physicalFileStub) CheckConnectivity(context.Context) error { return nil }
func (s *physicalFileStub) SaveFile(context.Context, *multipart.FileHeader, uint64, string) (string, error) {
	return "", nil
}

func (s *physicalFileStub) SaveBytes(context.Context, []byte, uint64, string, bool) (string, error) {
	return s.savedPath, nil
}

func (s *physicalFileStub) GetFile(_ context.Context, path string) (io.ReadCloser, error) {
	s.readPath = path
	return io.NopCloser(strings.NewReader("body")), nil
}
func (s *physicalFileStub) GetFileURL(context.Context, string) (string, error) { return "", nil }
func (s *physicalFileStub) DeleteFile(context.Context, string) error {
	s.deleteCalls++
	return nil
}
func (s *physicalFileStub) CopyFile(context.Context, string, uint64, string) (string, error) {
	return "", nil
}

func (s *physicalFileStub) SaveReader(context.Context, io.Reader, int64, string, string, uint64, string) (string, error) {
	return s.savedPath, nil
}

func (s *physicalFileStub) PrepareReaderPath(context.Context, int64, string, string, uint64, string) (string, error) {
	return s.savedPath, nil
}

func (s *physicalFileStub) SaveReaderTo(_ context.Context, _ io.Reader, _ int64, _ string, _ string, _ uint64, _ string, path string) error {
	s.writePath = path
	return nil
}

func (s *physicalFileStub) FinalizeReaderPath(_ context.Context, _ int64, _ string, _ string, _ uint64, _ string, path string) (string, error) {
	return path, nil
}

func TestResourceCatalogFileServiceReturnsReferenceAndResolvesReads(t *testing.T) {
	inner := &physicalFileStub{savedPath: "local://7/exports/a.png"}
	catalog := &catalogStub{}
	svc := NewResourceCatalogFileService(inner, catalog)

	ref, err := svc.SaveBytes(context.Background(), []byte("image"), 7, "a.png", false)
	require.NoError(t, err)
	require.Equal(t, "resource://AbCdEfGhIjKlMnOpQrStUv", ref)
	reader, err := svc.GetFile(context.Background(), ref)
	require.NoError(t, err)
	require.NoError(t, reader.Close())
	require.Equal(t, inner.savedPath, inner.readPath)
}

func TestResourceCatalogFileServiceReturnsShortExternalGrantURL(t *testing.T) {
	t.Setenv("APP_EXTERNAL_URL", "https://weknora.example.com/")
	inner := &physicalFileStub{savedPath: "local://7/exports/a.png"}
	catalog := &catalogStub{}
	svc := NewResourceCatalogFileService(inner, catalog)

	ref, err := svc.SaveBytes(context.Background(), []byte("image"), 7, "a.png", false)
	require.NoError(t, err)
	externalURL, err := svc.GetFileURL(context.Background(), ref)
	require.NoError(t, err)
	require.Equal(t, "https://weknora.example.com/r/GrantTokenAbCdEfGhIjKl", externalURL)
}

func TestPreparedResourceRegistersOnlyAfterPhysicalWrite(t *testing.T) {
	inner := &physicalFileStub{savedPath: "local://7/knowledge/source.pdf"}
	catalog := &catalogStub{}
	svc := NewResourceCatalogFileService(inner, catalog)
	prepared, ok := svc.(interfaces.PreparedStreamingFileService)
	require.True(t, ok)

	path, err := prepared.PrepareReaderPath(context.Background(), 4, "source.pdf", "application/pdf", 7, "knowledge")
	require.NoError(t, err)
	require.Equal(t, inner.savedPath, path)
	require.Zero(t, catalog.registerCalls)

	require.NoError(t, prepared.SaveReaderTo(
		context.Background(), strings.NewReader("data"), 4, "source.pdf", "application/pdf", 7, "knowledge", path,
	))
	require.Equal(t, inner.savedPath, inner.writePath)
	require.Zero(t, catalog.registerCalls)

	ref, err := prepared.FinalizeReaderPath(context.Background(), 4, "source.pdf", "application/pdf", 7, "knowledge", path)
	require.NoError(t, err)
	require.Equal(t, "resource://AbCdEfGhIjKlMnOpQrStUv", ref)
	require.Equal(t, 1, catalog.registerCalls)
}

func TestResourceCatalogDeleteIsIdempotentAfterResourceWasDeleted(t *testing.T) {
	inner := &physicalFileStub{}
	catalog := &catalogStub{resolveErr: interfaces.ErrResourceNotFound}
	svc := NewResourceCatalogFileService(inner, catalog)

	err := svc.DeleteFile(context.Background(), "resource://AbCdEfGhIjKlMnOpQrStUv")

	require.NoError(t, err)
	require.Zero(t, inner.deleteCalls)
	require.True(t, errors.Is(catalog.resolveErr, interfaces.ErrResourceNotFound))
}
