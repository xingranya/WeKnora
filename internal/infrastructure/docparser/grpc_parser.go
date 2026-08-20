package docparser

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"

	docclient "github.com/Tencent/WeKnora/docreader/client"
	"github.com/Tencent/WeKnora/docreader/proto"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	gproto "google.golang.org/protobuf/proto"
)

const (
	defaultDocReaderGRPCMaxMessageBytes = 50 * 1024 * 1024
	docReaderProtobufReserveBytes       = 1 * 1024 * 1024
	defaultDocReaderMaxImageCount       = int64(256)
	defaultDocReaderMaxImageBytes       = int64(16 * 1024 * 1024)
	defaultDocReaderMaxTotalImageBytes  = int64(128 * 1024 * 1024)
	defaultDocReaderConnectTimeout      = 5 * time.Second
)

func getMaxMessageSize() int {
	for _, name := range []string{"DOCREADER_GRPC_MAX_FILE_SIZE_MB", "MAX_FILE_SIZE_MB"} {
		if sizeStr := os.Getenv(name); sizeStr != "" {
			if size, err := strconv.Atoi(sizeStr); err == nil && size > 0 {
				return size * 1024 * 1024
			}
		}
	}
	return defaultDocReaderGRPCMaxMessageBytes
}

// DocReaderPayloadMaxBytes 返回文件正文可占用的最大字节数，并为 protobuf 元数据预留空间。
func DocReaderPayloadMaxBytes() int64 {
	maxMessageBytes := int64(getMaxMessageSize())
	if maxMessageBytes <= docReaderProtobufReserveBytes {
		return 0
	}
	return maxMessageBytes - docReaderProtobufReserveBytes
}

type docReaderImageLimits struct {
	maxCount      int64
	maxImageBytes int64
	maxTotalBytes int64
}

func positiveEnvInt64(name string, fallback int64) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func getDocReaderImageLimits() docReaderImageLimits {
	return docReaderImageLimits{
		maxCount: positiveEnvInt64(
			"DOCREADER_MAX_IMAGE_COUNT",
			defaultDocReaderMaxImageCount,
		),
		maxImageBytes: positiveEnvInt64(
			"DOCREADER_MAX_IMAGE_SIZE_MB",
			defaultDocReaderMaxImageBytes/(1024*1024),
		) * 1024 * 1024,
		maxTotalBytes: positiveEnvInt64(
			"DOCREADER_MAX_TOTAL_IMAGE_SIZE_MB",
			defaultDocReaderMaxTotalImageBytes/(1024*1024),
		) * 1024 * 1024,
	}
}

// GRPCDocumentReader implements DocumentReader over gRPC.
type GRPCDocumentReader struct {
	connectMu sync.Mutex
	mu        sync.RWMutex
	conn      *grpc.ClientConn
	client    proto.DocReaderClient
	addr      string
}

func NewGRPCDocumentReader(addr string) (*GRPCDocumentReader, error) {
	p := &GRPCDocumentReader{}
	if addr != "" {
		if err := p.connect(addr); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (p *GRPCDocumentReader) connect(addr string) error {
	p.connectMu.Lock()
	defer p.connectMu.Unlock()

	authConfig := docclient.LoadAuthConfigFromEnv()
	opts, err := authConfig.BuildDialOptions(getMaxMessageSize())
	if err != nil {
		return fmt.Errorf("failed to build docreader dial options: %w", err)
	}
	if authConfig.TLSEnabled {
		logger.Infof(context.Background(), "TLS enabled for docreader gRPC client")
	}
	if authConfig.AuthToken != "" {
		logger.Infof(context.Background(),
			"Token authentication enabled for docreader gRPC client (TLS=%v)",
			authConfig.TLSEnabled,
		)
	}

	opts = append(
		opts,
		grpc.WithContextDialer(utils.SSRFSafeGRPCDialer),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
	)

	start := time.Now()
	connectTimeout := time.Duration(positiveEnvInt64("DOCREADER_GRPC_CONNECT_TIMEOUT_SEC", int64(defaultDocReaderConnectTimeout/time.Second))) * time.Second
	connectCtx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	conn, err := grpc.DialContext(connectCtx, "passthrough:///"+addr, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to docreader: %w", err)
	}
	healthResponse, err := healthpb.NewHealthClient(conn).Check(connectCtx, &healthpb.HealthCheckRequest{})
	if err != nil || healthResponse.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		_ = conn.Close()
		if err != nil {
			return fmt.Errorf("docreader health check failed: %w", err)
		}
		return fmt.Errorf("docreader health check returned %s", healthResponse.GetStatus())
	}
	client := proto.NewDocReaderClient(conn)
	if _, err := client.ListEngines(connectCtx, &proto.ListEnginesRequest{}); err != nil {
		_ = conn.Close()
		return fmt.Errorf("docreader capability check failed: %w", err)
	}
	logger.Infof(context.Background(), "Connected to healthy docreader in %v", time.Since(start))

	p.mu.Lock()
	oldConn := p.conn
	p.conn = conn
	p.client = client
	p.addr = addr
	p.mu.Unlock()
	if oldConn != nil {
		_ = oldConn.Close()
	}
	return nil
}

func (p *GRPCDocumentReader) Reconnect(addr string) error {
	return p.connect(addr)
}

func (p *GRPCDocumentReader) IsConnected() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.conn == nil {
		return false
	}
	state := p.conn.GetState()
	return state == connectivity.Ready || state == connectivity.Idle
}

func (p *GRPCDocumentReader) Close() error {
	p.connectMu.Lock()
	defer p.connectMu.Unlock()
	p.mu.Lock()
	conn := p.conn
	p.conn = nil
	p.client = nil
	p.addr = ""
	p.mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

var errNotConnected = fmt.Errorf("docreader service not connected")

func (p *GRPCDocumentReader) Read(ctx context.Context, req *types.ReadRequest) (*types.ReadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return nil, errNotConnected
	}

	protoReq := &proto.ReadRequest{
		FileContent: req.FileContent,
		FileName:    req.FileName,
		FileType:    req.FileType,
		Url:         req.URL,
		Title:       req.Title,
		RequestId:   req.RequestID,
		Config: &proto.ReadConfig{
			ParserEngine:          req.ParserEngine,
			ParserEngineOverrides: req.ParserEngineOverrides,
		},
	}
	if err := validateDocReaderRequest(protoReq); err != nil {
		return nil, err
	}

	// Use the streaming RPC so documents with many page images (large scanned
	// PDFs) are not capped by the unary message-size limit. The meta frame
	// arrives first, followed by one frame per image.
	result, err := p.readStream(ctx, client, protoReq)
	if err != nil {
		// An older docreader build may not implement ReadStream. Fall back to
		// the unary Read RPC so a version-skewed deployment still parses
		// documents (small/medium docs only — the unary path remains capped by
		// the gRPC message-size limit, which is exactly what streaming avoids).
		if status.Code(err) == codes.Unimplemented {
			logger.Warnf(ctx, "docreader ReadStream unimplemented, falling back to unary Read: %v", err)
			return p.readUnary(ctx, client, protoReq)
		}
		return nil, err
	}
	return result, nil
}

// readStream consumes the server-streaming ReadStream RPC: one meta frame
// followed by one frame per image. Errors are returned verbatim so the caller
// can inspect the gRPC status code (e.g. Unimplemented) for fallback.
func (p *GRPCDocumentReader) readStream(
	ctx context.Context, client proto.DocReaderClient, protoReq *proto.ReadRequest,
) (*types.ReadResult, error) {
	stream, err := client.ReadStream(ctx, protoReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC ReadStream failed: %w", err)
	}

	result := &types.ReadResult{}
	gotMeta := false
	imageLimits := getDocReaderImageLimits()
	var imageCount int64
	var totalImageBytes int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		frame, recvErr := stream.Recv()
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return nil, fmt.Errorf("gRPC ReadStream recv failed: %w", recvErr)
		}

		if meta := frame.GetMeta(); meta != nil {
			gotMeta = true
			result.MarkdownContent = meta.GetMarkdownContent()
			result.ImageDirPath = meta.GetImageDirPath()
			result.Metadata = meta.GetMetadata()
			result.Error = meta.GetError()
			if n := meta.GetImageCount(); n > 0 {
				if int64(n) > imageLimits.maxCount {
					return nil, status.Errorf(
						codes.ResourceExhausted,
						"docreader image count %d exceeds limit %d",
						n,
						imageLimits.maxCount,
					)
				}
				result.ImageRefs = make([]types.ImageRef, 0, n)
			}
			continue
		}

		if img := frame.GetImage(); img != nil {
			if err := validateDocReaderImage(
				ctx,
				img,
				imageLimits,
				&imageCount,
				&totalImageBytes,
			); err != nil {
				return nil, err
			}
			result.ImageRefs = append(result.ImageRefs, types.ImageRef{
				Filename:    img.GetFilename(),
				OriginalRef: img.GetOriginalRef(),
				MimeType:    img.GetMimeType(),
				StorageKey:  img.GetStorageKey(),
				ImageData:   img.GetImageData(),
			})
		}
	}

	if !gotMeta {
		return nil, fmt.Errorf("gRPC ReadStream returned no metadata frame")
	}
	return result, nil
}

// readUnary calls the legacy unary Read RPC. Used only as a compatibility
// fallback when the connected docreader does not implement ReadStream.
func (p *GRPCDocumentReader) readUnary(
	ctx context.Context, client proto.DocReaderClient, protoReq *proto.ReadRequest,
) (*types.ReadResult, error) {
	resp, err := client.Read(ctx, protoReq)
	if err != nil {
		return nil, fmt.Errorf("gRPC Read failed: %w", err)
	}

	result := &types.ReadResult{
		MarkdownContent: resp.GetMarkdownContent(),
		ImageDirPath:    resp.GetImageDirPath(),
		Metadata:        resp.GetMetadata(),
		Error:           resp.GetError(),
	}
	imageLimits := getDocReaderImageLimits()
	var imageCount int64
	var totalImageBytes int64
	if refs := resp.GetImageRefs(); len(refs) > 0 {
		if int64(len(refs)) > imageLimits.maxCount {
			return nil, status.Errorf(
				codes.ResourceExhausted,
				"docreader image count %d exceeds limit %d",
				len(refs),
				imageLimits.maxCount,
			)
		}
		result.ImageRefs = make([]types.ImageRef, 0, len(refs))
		for _, img := range refs {
			if err := validateDocReaderImage(
				ctx,
				img,
				imageLimits,
				&imageCount,
				&totalImageBytes,
			); err != nil {
				return nil, err
			}
			result.ImageRefs = append(result.ImageRefs, types.ImageRef{
				Filename:    img.GetFilename(),
				OriginalRef: img.GetOriginalRef(),
				MimeType:    img.GetMimeType(),
				StorageKey:  img.GetStorageKey(),
				ImageData:   img.GetImageData(),
			})
		}
	}
	return result, nil
}

func validateDocReaderRequest(req *proto.ReadRequest) error {
	payloadBytes := int64(len(req.GetFileContent()))
	if payloadBytes > DocReaderPayloadMaxBytes() {
		return status.Errorf(
			codes.ResourceExhausted,
			"docreader file payload %d bytes exceeds limit %d bytes",
			payloadBytes,
			DocReaderPayloadMaxBytes(),
		)
	}
	messageBytes := gproto.Size(req)
	if messageBytes > getMaxMessageSize() {
		return status.Errorf(
			codes.ResourceExhausted,
			"docreader protobuf request %d bytes exceeds gRPC limit %d bytes",
			messageBytes,
			getMaxMessageSize(),
		)
	}
	return nil
}

func validateDocReaderImage(
	ctx context.Context,
	image *proto.ImageRef,
	limits docReaderImageLimits,
	imageCount *int64,
	totalImageBytes *int64,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	(*imageCount)++
	if *imageCount > limits.maxCount {
		return status.Errorf(
			codes.ResourceExhausted,
			"docreader image count exceeds limit %d",
			limits.maxCount,
		)
	}
	imageBytes := int64(len(image.GetImageData()))
	if imageBytes > limits.maxImageBytes {
		return status.Errorf(
			codes.ResourceExhausted,
			"docreader image size %d bytes exceeds limit %d bytes",
			imageBytes,
			limits.maxImageBytes,
		)
	}
	if *totalImageBytes > limits.maxTotalBytes-imageBytes {
		return status.Errorf(
			codes.ResourceExhausted,
			"docreader total image bytes exceed limit %d bytes",
			limits.maxTotalBytes,
		)
	}
	*totalImageBytes += imageBytes
	return nil
}

func (p *GRPCDocumentReader) ListEngines(ctx context.Context, overrides map[string]string) ([]types.ParserEngineInfo, error) {
	p.mu.RLock()
	client := p.client
	p.mu.RUnlock()
	if client == nil {
		return nil, errNotConnected
	}

	resp, err := client.ListEngines(ctx, &proto.ListEnginesRequest{ConfigOverrides: overrides})
	if err != nil {
		return nil, fmt.Errorf("gRPC ListEngines failed: %w", err)
	}

	result := make([]types.ParserEngineInfo, 0, len(resp.GetEngines()))
	for _, e := range resp.GetEngines() {
		result = append(result, types.ParserEngineInfo{
			Name:              e.GetName(),
			Description:       e.GetDescription(),
			FileTypes:         e.GetFileTypes(),
			Available:         e.GetAvailable(),
			UnavailableReason: e.GetUnavailableReason(),
		})
	}
	return result, nil
}
