// Package grpcapi implements the agent-facing gRPC services: enrollment
// (token-authenticated, no client cert) and AgentService (mTLS only).
package grpcapi

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/devalexllc/lighthouse/internal/pb/lighthousev1"
	"github.com/devalexllc/lighthouse/internal/server/ca"
	"github.com/devalexllc/lighthouse/internal/server/store"
	"github.com/google/uuid"
)

// Server implements lighthouse.v1.EnrollmentService and AgentService.
type Server struct {
	pb.UnimplementedEnrollmentServiceServer
	pb.UnimplementedAgentServiceServer

	store *store.Store
	ca    *ca.CA
}

func New(st *store.Store, authority *ca.CA) *Server {
	return &Server{store: st, ca: authority}
}

// Register attaches both services to a grpc.Server.
func (s *Server) Register(g *grpc.Server) {
	pb.RegisterEnrollmentServiceServer(g, s)
	pb.RegisterAgentServiceServer(g, s)
}

// Enroll consumes a one-time join token and issues the agent's first
// certificate. This is the only RPC that works without a client cert.
func (s *Server) Enroll(ctx context.Context, req *pb.EnrollRequest) (*pb.EnrollResponse, error) {
	if req.GetJoinToken() == "" || len(req.GetCsrDer()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "join_token and csr_der are required")
	}

	// Validate the CSR before touching the single-use token so a malformed
	// CSR is rejected without consuming anything.
	if err := ca.ValidateCSR(req.GetCsrDer()); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "CSR rejected: %v", err)
	}

	probeAddress := req.GetProbeAddress()
	if probeAddress == "" {
		if p, ok := peer.FromContext(ctx); ok {
			if host, _, err := net.SplitHostPort(p.Addr.String()); err == nil {
				probeAddress = host
			}
		}
		// Behind the SNI-passthrough proxy the observed source is the
		// PROXY, not the agent — mesh probes against it would be wrong.
		slog.Warn("enrollment without --probe-address: falling back to observed source, "+
			"which is the proxy's address in proxied deployments",
			"observed", probeAddress, "hostname", req.GetHostname())
	}

	var certDER []byte
	var notAfter time.Time
	csrHash := sha256.Sum256(req.GetCsrDer())
	agentID, siteID, err := s.store.EnrollAgent(ctx,
		req.GetJoinToken(), req.GetHostname(), probeAddress, req.GetAgentVersion(), csrHash[:],
		func(agentID uuid.UUID) (store.IssuedCert, error) {
			der, serial, na, err := s.ca.SignAgentCSR(req.GetCsrDer(), agentID, req.GetHostname())
			if err != nil {
				return store.IssuedCert{}, err
			}
			certDER, notAfter = der, na
			return store.IssuedCert{
				Serial:    serial,
				NotBefore: time.Now().Add(-5 * time.Minute),
				NotAfter:  na,
			}, nil
		})
	if err == store.ErrTokenInvalid {
		return nil, status.Error(codes.PermissionDenied, err.Error())
	}
	if err != nil {
		slog.Error("enrollment failed", "err", err)
		return nil, status.Error(codes.Internal, "enrollment failed")
	}

	slog.Info("agent enrolled", "agent", agentID, "site", siteID,
		"hostname", req.GetHostname(), "probe_address", probeAddress)
	return &pb.EnrollResponse{
		AgentId:     agentID.String(),
		CertDer:     certDER,
		CaBundleDer: s.ca.BundleDER(),
		NotAfter:    timestamppb.New(notAfter),
	}, nil
}

// StreamConfig registers the agent as connected and pushes config snapshots.
// M1: sends an empty snapshot and keeps the stream open as liveness; probe
// distribution lands in M2.
func (s *Server) StreamConfig(hello *pb.AgentHello, stream grpc.ServerStreamingServer[pb.ConfigSnapshot]) error {
	ctx := stream.Context()
	id, err := s.authenticateAgent(ctx)
	if err != nil {
		return err
	}
	slog.Info("agent connected", "agent", id.AgentID, "version", hello.GetAgentVersion())

	snapshot := &pb.ConfigSnapshot{ConfigHash: "empty"}
	if hello.GetConfigHash() != snapshot.GetConfigHash() {
		if err := stream.Send(snapshot); err != nil {
			return err
		}
	}
	if err := s.store.TouchAgent(ctx, id.AgentID, hello.GetAgentVersion(), snapshot.GetConfigHash()); err != nil {
		slog.Error("touch agent failed", "err", err)
	}

	// Keep the stream open; periodically refresh liveness and re-check the
	// certificate so revocation cuts live streams within a minute.
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			slog.Info("agent disconnected", "agent", id.AgentID)
			return nil
		case <-ticker.C:
			// Fail closed: the DB is the sole revocation authority, so a
			// stream whose certificate cannot be confirmed valid does not
			// stay up. The agent reconnects with backoff once the check
			// passes again.
			valid, err := s.store.CertValid(ctx, id.Cert.SerialNumber, id.AgentID)
			if err != nil {
				slog.Error("dropping stream: certificate validity unconfirmable", "agent", id.AgentID, "err", err)
				return status.Error(codes.Unavailable, "certificate validity check failed")
			}
			if !valid {
				slog.Info("dropping stream for revoked certificate", "agent", id.AgentID)
				return status.Error(codes.PermissionDenied, "certificate revoked")
			}
			if err := s.store.TouchAgent(ctx, id.AgentID, hello.GetAgentVersion(), snapshot.GetConfigHash()); err != nil {
				slog.Error("touch agent failed", "err", err)
			}
		}
	}
}

// PushResults ingests a result batch. M1: authenticates and acknowledges;
// storage lands with the probe_results hypertable in M2.
func (s *Server) PushResults(ctx context.Context, req *pb.PushResultsRequest) (*pb.PushResultsResponse, error) {
	id, err := s.authenticateAgent(ctx)
	if err != nil {
		return nil, err
	}
	if req.GetDroppedSinceLastPush() > 0 {
		slog.Warn("agent reported spooled results dropped",
			"agent", id.AgentID, "dropped", req.GetDroppedSinceLastPush())
	}
	return &pb.PushResultsResponse{Accepted: uint32(len(req.GetResults()))}, nil
}

// RenewCert issues a fresh certificate to an already-authenticated agent.
func (s *Server) RenewCert(ctx context.Context, req *pb.RenewCertRequest) (*pb.RenewCertResponse, error) {
	id, err := s.authenticateAgent(ctx)
	if err != nil {
		return nil, err
	}
	certDER, serial, notAfter, err := s.ca.SignAgentCSR(req.GetCsrDer(), id.AgentID, id.Cert.Subject.CommonName)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "CSR rejected: %v", err)
	}
	if err := s.store.InsertCertificate(ctx, serial, id.AgentID,
		time.Now().Add(-5*time.Minute), notAfter); err != nil {
		slog.Error("record renewed certificate failed", "err", err)
		return nil, status.Error(codes.Internal, "renewal failed")
	}
	slog.Info("certificate renewed", "agent", id.AgentID, "not_after", notAfter)
	return &pb.RenewCertResponse{
		CertDer:     certDER,
		CaBundleDer: s.ca.BundleDER(),
		NotAfter:    timestamppb.New(notAfter),
	}, nil
}
