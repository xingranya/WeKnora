package docparser

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/docreader/proto"
	"github.com/Tencent/WeKnora/internal/utils"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
)

type healthyDocReaderServer struct {
	proto.UnimplementedDocReaderServer
}

func (healthyDocReaderServer) ListEngines(
	context.Context,
	*proto.ListEnginesRequest,
) (*proto.ListEnginesResponse, error) {
	return &proto.ListEnginesResponse{}, nil
}

func TestDocReaderPayloadMaxBytesReservesProtobufOverhead(t *testing.T) {
	t.Setenv("DOCREADER_GRPC_MAX_FILE_SIZE_MB", "2")
	t.Setenv("MAX_FILE_SIZE_MB", "50")

	if got, want := DocReaderPayloadMaxBytes(), int64(1024*1024); got != want {
		t.Fatalf("DocReaderPayloadMaxBytes() = %d, want %d", got, want)
	}
	request := &proto.ReadRequest{FileContent: make([]byte, 1024*1024+1)}
	err := validateDocReaderRequest(request)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("validateDocReaderRequest() code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
}

func TestValidateDocReaderImageLimits(t *testing.T) {
	limits := docReaderImageLimits{maxCount: 2, maxImageBytes: 4, maxTotalBytes: 5}
	var count int64
	var total int64

	if err := validateDocReaderImage(
		context.Background(),
		&proto.ImageRef{ImageData: []byte("abc")},
		limits,
		&count,
		&total,
	); err != nil {
		t.Fatalf("first image rejected: %v", err)
	}
	if err := validateDocReaderImage(
		context.Background(),
		&proto.ImageRef{ImageData: []byte("def")},
		limits,
		&count,
		&total,
	); status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("total image limit code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}

	count, total = 0, 0
	err := validateDocReaderImage(
		context.Background(),
		&proto.ImageRef{ImageData: []byte("large")},
		limits,
		&count,
		&total,
	)
	if status.Code(err) != codes.ResourceExhausted {
		t.Fatalf("single image limit code = %v, want %v", status.Code(err), codes.ResourceExhausted)
	}
}

func TestValidateDocReaderImageChecksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var count int64
	var total int64
	err := validateDocReaderImage(
		ctx,
		&proto.ImageRef{ImageData: []byte("abc")},
		docReaderImageLimits{maxCount: 1, maxImageBytes: 3, maxTotalBytes: 3},
		&count,
		&total,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("validateDocReaderImage() error = %v, want context.Canceled", err)
	}
}

func TestGRPCDocumentReaderUsesSSRFSafeDialer(t *testing.T) {
	utils.SetSSRFWhitelistFromRaw("")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })

	reader, err := NewGRPCDocumentReader("127.0.0.1:50051")
	if reader != nil || err == nil || !strings.Contains(err.Error(), "connection blocked") {
		t.Fatalf("NewGRPCDocumentReader() = (%v, %v), want SSRF connection block", reader, err)
	}
}

func TestGRPCDocumentReaderReconnectKeepsHealthyConnectionOnFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	proto.RegisterDocReaderServer(server, healthyDocReaderServer{})
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	t.Setenv("DOCREADER_GRPC_CONNECT_TIMEOUT_SEC", "1")
	reader, err := NewGRPCDocumentReader(listener.Addr().String())
	if err != nil {
		t.Fatalf("NewGRPCDocumentReader() error: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if !reader.IsConnected() {
		t.Fatal("healthy reader is not connected")
	}

	if err := reader.Reconnect("169.254.169.254:50051"); err == nil {
		t.Fatal("Reconnect() unexpectedly accepted blocked address")
	}
	if !reader.IsConnected() {
		t.Fatal("failed reconnect destroyed the previous healthy connection")
	}
}

func TestGRPCDocumentReaderRejectsHealthOnlyEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	go server.Serve(listener)
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	utils.SetSSRFWhitelistFromRaw("127.0.0.1")
	t.Cleanup(func() { utils.SetSSRFWhitelistFromRaw("") })
	t.Setenv("DOCREADER_GRPC_CONNECT_TIMEOUT_SEC", "1")
	reader, err := NewGRPCDocumentReader(listener.Addr().String())
	if reader != nil || err == nil || !strings.Contains(err.Error(), "capability check failed") {
		t.Fatalf("NewGRPCDocumentReader() = (%v, %v), want capability rejection", reader, err)
	}
}
