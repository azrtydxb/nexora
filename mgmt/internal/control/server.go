// Package control serves the EngineControl gRPC API and pushes config versions to engines.
package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
	"github.com/piwi3910/nexora/mgmt/internal/snapshot"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

// BlobChunkSize is the GetBlob chunk size.
const BlobChunkSize = 1 << 20

var (
	nodeNameRE = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
	uuidRE     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	sha256RE   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Server implements controlv1.EngineControlServer.
type Server struct {
	controlv1.UnimplementedEngineControlServer

	// OnStats, when set, receives every Stats message.
	OnStats func(ctx context.Context, engineID string, s *controlv1.Stats)

	st         *store.Store
	ca         *pki.CA
	hub        *Hub
	instanceID string
}

// NewServer creates the EngineControl server of one instance.
func NewServer(st *store.Store, ca *pki.CA, hub *Hub, instanceID string) *Server {
	return &Server{st: st, ca: ca, hub: hub, instanceID: instanceID}
}

// Enroll exchanges a join secret and a CSR for an engine identity.
func (s *Server) Enroll(ctx context.Context, req *controlv1.EnrollRequest) (*controlv1.EnrollResponse, error) {
	if !nodeNameRE.MatchString(req.NodeName) {
		return nil, status.Error(codes.InvalidArgument, "node_name must match [a-z0-9-]{1,63}")
	}
	resp := &controlv1.EnrollResponse{CaCertificateDer: s.ca.Cert.Raw}
	err := s.st.InTx(ctx, func(tx pgx.Tx) error {
		tokenID, err := lookupJoinToken(ctx, tx, req.JoinSecret)
		if errors.Is(err, pgx.ErrNoRows) {
			return status.Error(codes.PermissionDenied, "invalid, expired or revoked join token")
		} else if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "select gen_random_uuid()::text").Scan(&resp.EngineId); err != nil {
			return err
		}
		der, serial, err := s.ca.SignEngineCSR(req.CsrDer, resp.EngineId, pki.EngineCertValidity)
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		resp.CertificateDer = der
		if _, err := tx.Exec(ctx, `insert into engines(id, node_name, join_token_id, certificate_serial, engine_version)
			values ($1, $2, $3, $4, $5)`, resp.EngineId, req.NodeName, tokenID, serial, req.EngineVersion); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "update join_tokens set uses = uses + 1 where id = $1", tokenID)
		return err
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return resp, nil
}

// Connect is the engine's long-lived config stream.
func (s *Server) Connect(stream controlv1.EngineControl_ConnectServer) error {
	ctx := stream.Context()
	id, err := s.engine(ctx)
	if err != nil {
		return err
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil {
		return status.Error(codes.InvalidArgument, "first message must be Hello")
	}
	if hello.EngineId != "" && hello.EngineId != id {
		return status.Error(codes.PermissionDenied, "Hello engine_id does not match the client certificate")
	}
	err = s.st.InTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "insert into instances(id) values ($1) on conflict do nothing", s.instanceID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `update engines set
			node_name = case when $2 ~ '^[a-z0-9-]{1,63}$' then $2 else node_name end,
			engine_version = case when $3 <> '' then $3 else engine_version end,
			connected_instance = $4, last_seen_at = now()
			where id = $1`, id, hello.NodeName, hello.EngineVersion, s.instanceID)
		return err
	})
	if err != nil {
		return grpcError(err)
	}
	// Headers now, not with the first snapshot: an engine that is already current would otherwise
	// wait indefinitely for the response to start and never learn that its Hello was accepted.
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}

	sub := newSubscriber(id, hello.AppliedVersion)
	s.hub.register(sub)
	defer func() {
		s.hub.unregister(sub)
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := s.st.Pool.Exec(dctx, "update engines set connected_instance = null where id = $1 and connected_instance = $2", id, s.instanceID); err != nil {
			slog.Warn("clear engine connection", "engine", id, "err", err)
		}
	}()

	// Registered before reading the latest version, so a version published in between is not missed.
	version, snap, err := snapshot.Latest(ctx, s.st.Pool)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return grpcError(err)
	case hello.AppliedVersion < version:
		sub.offer(version, snap)
	case hello.AppliedVersion > version:
		if _, err := s.st.Pool.Exec(ctx, "update engines set version_ahead = true where id = $1", id); err != nil {
			return grpcError(err)
		}
		sub.send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_VersionAhead{VersionAhead: &controlv1.VersionAhead{ServerVersion: version}}})
	}

	// A failed Send breaks the stream, which also ends Recv in the loop below.
	sendCtx, stopSend := context.WithCancel(ctx)
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for {
			select {
			case <-sendCtx.Done():
				return
			case msg := <-sub.out:
				if err := stream.Send(msg); err != nil {
					return
				}
			}
		}
	}()
	err = s.receive(ctx, stream, sub)
	stopSend()
	<-sendDone
	return err
}

func (s *Server) receive(ctx context.Context, stream controlv1.EngineControl_ConnectServer, sub *subscriber) error {
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch m := msg.Msg.(type) {
		case *controlv1.EngineMessage_Applied:
			sub.observe(m.Applied.Version)
			_, err = s.st.Pool.Exec(ctx, `update engines set applied_version = $2, persist_error = $3, version_ahead = false,
				rejected_reason = case when rejected_version <= $2 then '' else rejected_reason end,
				rejected_version = case when rejected_version <= $2 then null else rejected_version end,
				last_seen_at = now()
				where id = $1`, sub.engineID, int64(m.Applied.Version), m.Applied.PersistError)
		case *controlv1.EngineMessage_Rejected:
			sub.observe(m.Rejected.Version)
			_, err = s.st.Pool.Exec(ctx, "update engines set rejected_version = $2, rejected_reason = $3, last_seen_at = now() where id = $1",
				sub.engineID, int64(m.Rejected.Version), m.Rejected.Reason)
		case *controlv1.EngineMessage_Stats:
			_, err = s.st.Pool.Exec(ctx, "update engines set last_seen_at = now() where id = $1", sub.engineID)
			if s.OnStats != nil {
				s.OnStats(ctx, sub.engineID, m.Stats)
			}
		default:
			err = status.Error(codes.InvalidArgument, "unexpected message")
		}
		if err != nil {
			return grpcError(err)
		}
	}
}

// GetBlob streams a blob in BlobChunkSize chunks.
func (s *Server) GetBlob(req *controlv1.GetBlobRequest, stream controlv1.EngineControl_GetBlobServer) error {
	ctx := stream.Context()
	if _, err := s.engine(ctx); err != nil {
		return err
	}
	if !sha256RE.MatchString(req.Sha256) {
		return status.Error(codes.InvalidArgument, "sha256 must be 64 lowercase hex characters")
	}
	// debt: the whole blob is read into memory (<= 256 MiB lists); revisit with large-object
	// streaming if memory pressure on mgmt instances shows up.
	var data []byte
	if err := s.st.Pool.QueryRow(ctx, "select data from blobs where sha256 = $1", req.Sha256).Scan(&data); err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			return status.Error(codes.NotFound, "blob not found")
		}
		return grpcError(err)
	}
	for off := 0; off < len(data); off += BlobChunkSize {
		if err := stream.Send(&controlv1.BlobChunk{Data: data[off:min(off+BlobChunkSize, len(data))]}); err != nil {
			return err
		}
	}
	return nil
}

// engine authenticates the caller's certificate and requires a live engine row.
func (s *Server) engine(ctx context.Context) (string, error) {
	id, err := EngineID(ctx)
	if err != nil {
		return "", err
	}
	if !uuidRE.MatchString(id) {
		return "", status.Error(codes.PermissionDenied, "unknown engine")
	}
	var deleted bool
	err = s.st.Pool.QueryRow(ctx, "select deleted_at is not null from engines where id = $1", id).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && deleted) {
		return "", status.Error(codes.PermissionDenied, "unknown or deleted engine")
	}
	if err != nil {
		return "", grpcError(err)
	}
	return id, nil
}

func grpcError(err error) error {
	if _, ok := status.FromError(err); ok {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, err.Error())
	}
	if errors.Is(store.MapError(err), store.ErrUnavailable) {
		return status.Error(codes.Unavailable, "database unavailable")
	}
	slog.Error("engine control", "err", err)
	return status.Error(codes.Internal, "internal error")
}
