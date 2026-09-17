// Package control serves the EngineControl gRPC API and pushes config versions to engines.
package control

import (
	"context"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/miekg/dns"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/fleet"
	"github.com/piwi3910/nexora/mgmt/internal/pki"
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

	// OnStats receives Stats from the owning stream under an engine row lock.
	// It must use tx for all persistence, finish synchronously, and return errors.
	// The callback may be retried with the transaction; it must not commit tx.
	OnStats func(ctx context.Context, tx pgx.Tx, engineID string, s *controlv1.Stats) error
	// OnNotify schedules a refresh in the ownership transaction. Use tx for all
	// persistence; no nested pool acquisition, network IO, or application state in memory.
	OnNotify func(ctx context.Context, tx pgx.Tx, engineID string, ev *controlv1.NotifyReceived) error
	// OnUpdate, when set, applies a dynamic update an engine forwarded. It runs outside the receive
	// loop with a 4 s context; without it every update is answered NOTIMP. The callback
	// MUST call fence in its mutation transaction before writing, on every retry.
	// Preparation may use the pool before that transaction; no network IO under the fence.
	OnUpdate func(ctx context.Context, engineID string, req *controlv1.UpdateRequest, fence func(pgx.Tx) error) *controlv1.UpdateResult
	// EngineCertTTL is the lifetime of issued engine certificates (0: pki.EngineCertValidity).
	EngineCertTTL time.Duration

	st         *store.Store
	ca         *pki.CA
	hub        *Hub
	instanceID string
	dnsTLS     *DNSTLSFanout
	logs       *LogBroker // set before serving (SetLogBroker)
}

// NewServer creates the EngineControl server of one instance.
func NewServer(st *store.Store, ca *pki.CA, hub *Hub, instanceID string, dnsTLS *DNSTLSFanout) *Server {
	return &Server{st: st, ca: ca, hub: hub, instanceID: instanceID, dnsTLS: dnsTLS}
}

// Enroll exchanges a join secret and a CSR for an engine identity.
func (s *Server) Enroll(ctx context.Context, req *controlv1.EnrollRequest) (*controlv1.EnrollResponse, error) {
	if !nodeNameRE.MatchString(req.NodeName) {
		return nil, status.Error(codes.InvalidArgument, "node_name must match [a-z0-9-]{1,63}")
	}
	resp := &controlv1.EnrollResponse{CaCertificateDer: s.ca.Cert.Raw}
	err := s.st.InTx(ctx, func(tx pgx.Tx) error {
		grant, err := fleet.ConsumeJoinToken(ctx, tx, req.JoinSecret)
		if errors.Is(err, fleet.ErrJoinTokenUnknown) || errors.Is(err, fleet.ErrJoinTokenExpired) ||
			errors.Is(err, fleet.ErrJoinTokenExhausted) || errors.Is(err, fleet.ErrJoinTokenRevoked) {
			return status.Error(codes.PermissionDenied, err.Error())
		} else if err != nil {
			return err
		}
		engineID := uuid.New()
		resp.EngineId = engineID.String()
		der, serial, err := s.ca.SignEngineCSR(req.CsrDer, resp.EngineId, s.certTTL())
		if err != nil {
			return status.Error(codes.InvalidArgument, err.Error())
		}
		resp.CertificateDer = der
		if _, err := tx.Exec(ctx, `insert into engines(id, node_name, join_token_id, certificate_serial, engine_version, engine_group_id, labels)
			values ($1, $2, $3, $4, $5, $6, $7)`, engineID, req.NodeName, grant.ID, serial, req.EngineVersion, grant.EngineGroupID, grant.Labels); err != nil {
			return err
		}
		return fleet.RecordCertificate(ctx, tx, engineID, der)
	})
	if err != nil {
		return nil, grpcError(err)
	}
	return resp, nil
}

// Connect is the engine's long-lived config stream.
func (s *Server) Connect(stream controlv1.EngineControl_ConnectServer) error {
	ctx := stream.Context()
	id, serial, err := s.authenticate(ctx)
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
	sub := newSubscriber(id, hello.AppliedVersion)
	defer s.hub.unregisterConnection(sub, func() {
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := s.st.Pool.Exec(dctx, "update engines set connected_instance = null, connection_session = null where id = $1 and connection_session = $2", id, sub.sessionID); err != nil {
			slog.Warn("clear engine connection", "engine", id, "err", err)
		}
	})
	err = s.hub.registerConnection(sub, func() error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return s.st.InTx(ctx, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "insert into instances(id) values ($1) on conflict do nothing", s.instanceID); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `update engines set
			node_name = case when $2 ~ '^[a-z0-9-]{1,63}$' then $2 else node_name end,
			engine_version = case when $3 <> '' then $3 else engine_version end,
			connected_instance = $4, connection_session = $5, last_seen_at = now()
			where id = $1`, id, hello.NodeName, hello.EngineVersion, s.instanceID, sub.sessionID)
			if err != nil {
				return err
			}
			// Authentication may have raced a newer certificate or revocation while
			// this claim waited for the engine lock. Recheck before committing.
			if err := fleet.CheckCertificate(ctx, tx, sub.id, serial); err != nil {
				if errors.Is(err, fleet.ErrUnknownEngine) || errors.Is(err, fleet.ErrCertificateRevoked) {
					return status.Error(codes.PermissionDenied, err.Error())
				}
				return err
			}
			// Serialize certificate supersession with the stream ownership claim.
			return fleet.SupersedeOlderCertificates(ctx, tx, sub.id, serial)
		})
	})
	if err != nil {
		return grpcError(err)
	}
	// Headers now, not with the first snapshot: an engine that is already current would otherwise
	// wait indefinitely for the response to start and never learn that its Hello was accepted.
	if err := stream.SendHeader(metadata.MD{}); err != nil {
		return err
	}

	var tlsCh <-chan *controlv1.TlsMaterial
	err = s.withConnection(ctx, sub, func(tx pgx.Tx) error {
		tlsCh = s.dnsTLS.Register(id, hello.TlsFingerprintSha256)
		return nil
	})
	if err != nil {
		s.dnsTLS.Unregister(id, tlsCh)
		return grpcError(err)
	}
	sub.tlsCh = tlsCh
	defer s.dnsTLS.Unregister(id, tlsCh)
	// A revocation notified between authenticate and register reached no subscriber: check again.
	if err := s.checkCertificate(ctx, id, serial); err != nil {
		return err
	}

	// Registered before loading the target, so a rollout change in between is not missed. Keys go
	// before the snapshot, so a transfer zone's first refresh can already sign its request.
	t, err := fleet.TargetFor(ctx, s.st.Pool, sub.id)
	if errors.Is(err, store.ErrNotFound) {
		return status.Error(codes.PermissionDenied, "unknown or deleted engine")
	}
	if err != nil {
		return grpcError(err)
	}
	ks := s.hub.loadKeySets(ctx)
	offerTarget(sub, t, ks)
	clearKeyMaterial(ks.km)
	if t.Version > 0 && hello.AppliedVersion > t.Version {
		if err := s.writeConnection(ctx, "update engines set version_ahead = true where id = $1 and connection_session = $2", id, sub.sessionID); err != nil {
			return grpcError(err)
		}
		sub.send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_VersionAhead{VersionAhead: &controlv1.VersionAhead{ServerVersion: t.Version}}})
	}
	if t.RotateRequested {
		sub.renew()
	}

	// A failed Send breaks the stream, which also ends Recv in the loop below.
	sendCtx, stopSend := context.WithCancel(ctx)
	sendDone := make(chan struct{})
	go func() {
		defer close(sendDone)
		for {
			// Pending keys go first: a snapshot may name a zone whose key is queued with it.
			select {
			case k := <-sub.keys:
				if err := stream.Send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RpzTsigKeys{RpzTsigKeys: k}}); err != nil {
					return
				}
				continue
			case km := <-sub.keyMaterial:
				if !sendKeyMaterial(stream, km) {
					return
				}
				continue
			default:
			}
			select {
			case <-sendCtx.Done():
				return
			case k := <-sub.keys:
				if err := stream.Send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_RpzTsigKeys{RpzTsigKeys: k}}); err != nil {
					return
				}
			case km := <-sub.keyMaterial:
				if !sendKeyMaterial(stream, km) {
					return
				}
			case msg := <-sub.out:
				if err := stream.Send(msg); err != nil {
					return
				}
			case msg := <-sub.results:
				if err := stream.Send(msg); err != nil {
					return
				}
			case msg := <-sub.control:
				if err := stream.Send(msg); err != nil {
					return
				}
			case r := <-sub.logs:
				if err := stream.Send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_LogRequest{LogRequest: r}}); err != nil {
					return
				}
			case m := <-tlsCh:
				if err := stream.Send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_TlsMaterial{TlsMaterial: m}}); err != nil {
					return
				}
			}
		}
	}()
	received := make(chan error, 1)
	go func() { received <- s.receive(ctx, stream, sub) }()
	select {
	case err = <-received:
	case <-sub.revoked:
		err = status.Error(codes.PermissionDenied, "certificate revoked")
	}
	stopSend()
	<-sendDone
	return err
}

// sendKeyMaterial sends km (Send marshals synchronously) and then clears its secrets.
func sendKeyMaterial(stream controlv1.EngineControl_ConnectServer, km *controlv1.KeyMaterial) bool {
	defer clearKeyMaterial(km)
	return stream.Send(&controlv1.ServerMessage{Msg: &controlv1.ServerMessage_KeyMaterial{KeyMaterial: km}}) == nil
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
			err = s.writeConnection(ctx, `update engines set applied_version = $2, persist_error = $3, version_ahead = false,
				rejected_reason = case when rejected_version <= $2 then '' else rejected_reason end,
				rejected_version = case when rejected_version <= $2 then null else rejected_version end,
				last_seen_at = now()
				where id = $1 and connection_session = $4`, sub.engineID, int64(m.Applied.Version), m.Applied.PersistError, sub.sessionID)
			if err == nil {
				err = s.notifyRollout(ctx, sub)
			}
		case *controlv1.EngineMessage_Rejected:
			sub.observe(m.Rejected.Version)
			err = s.writeConnection(ctx, "update engines set rejected_version = $2, rejected_reason = $3, last_seen_at = now() where id = $1 and connection_session = $4",
				sub.engineID, int64(m.Rejected.Version), m.Rejected.Reason, sub.sessionID)
			if err == nil {
				err = s.notifyRollout(ctx, sub)
			}
		case *controlv1.EngineMessage_Stats:
			// Bound persistence while sharing the ownership transaction with the callback.
			statsCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = s.withConnection(statsCtx, sub, func(tx pgx.Tx) error {
				if _, err := tx.Exec(statsCtx, "update engines set last_seen_at = now() where id = $1", sub.engineID); err != nil {
					return err
				}
				if s.OnStats != nil {
					return s.OnStats(statsCtx, tx, sub.engineID, m.Stats)
				}
				return nil
			})
			cancel()
		case *controlv1.EngineMessage_NotifyReceived:
			if s.OnNotify != nil {
				ev := m.NotifyReceived
				nctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = s.withConnection(nctx, sub, func(tx pgx.Tx) error {
					return s.OnNotify(nctx, tx, sub.engineID, ev)
				})
				cancel()
				if err != nil && status.Code(err) != codes.Aborted {
					slog.Info("notify not acted on", "engine", sub.engineID, "zone", ev.Zone, "source", ev.Source, "err", err)
					err = nil
				}
			}
		case *controlv1.EngineMessage_UpdateRequest:
			s.update(ctx, sub, m.UpdateRequest)
		case *controlv1.EngineMessage_CertRequest:
			err = s.renew(ctx, sub, m.CertRequest)
		case *controlv1.EngineMessage_LogBatch:
			// Only a reply to a request sent on this stream is handed on.
			if s.logs != nil && sub.takeLogRequest(m.LogBatch.RequestId) {
				lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
				err = s.withConnection(lctx, sub, func(tx pgx.Tx) error {
					return s.logs.DeliverTx(lctx, tx, m.LogBatch)
				})
				cancel()
				if err == nil {
					s.logs.Done(m.LogBatch.RequestId)
				}
			}
		case *controlv1.EngineMessage_TlsMaterialResult:
			res := m.TlsMaterialResult
			slog.Info("dns tls result", "engine", sub.engineID, "fingerprint", res.FingerprintSha256, "applied", res.Applied, "error", res.Error)
			err = s.withConnection(ctx, sub, func(tx pgx.Tx) error {
				return store.UpsertEngineTLSState(ctx, tx, store.EngineTLSState{
					EngineID: uuid.MustParse(sub.engineID), Fingerprint: res.FingerprintSha256, Applied: res.Applied, Error: res.Error,
				})
			})
			if err == nil {
				s.dnsTLS.ResultFor(sub.engineID, sub.tlsCh, res)
			}
		default:
			err = status.Error(codes.InvalidArgument, "unexpected message")
		}
		if err != nil {
			return grpcError(err)
		}
	}
}

// notifyRollout wakes the rollout controllers and hubs of the engine's group after an ack or a
// rejection, so the rollout steps at once instead of on the next tick.
func (s *Server) notifyRollout(ctx context.Context, sub *subscriber) error {
	_, err := s.st.Pool.Exec(ctx, "select pg_notify($2, engine_group_id::text) from engines where id = $1", sub.engineID, ChannelRollout)
	return err
}

const (
	// maxUpdateMessage bounds a forwarded UPDATE: one DNS message.
	maxUpdateMessage = 65535
	maxRequestID     = 64
	updateTimeout    = 4 * time.Second
)

// update applies one forwarded dynamic update outside the receive loop, within the engine's rate
// and concurrency bounds, and queues its result on sub.results.
func (s *Server) update(ctx context.Context, sub *subscriber, req *controlv1.UpdateRequest) {
	reply := func(rcode uint32, detail string) {
		sub.result(&controlv1.UpdateResult{RequestId: req.RequestId, Rcode: rcode, Detail: detail})
	}
	switch {
	case len(req.RequestId) > maxRequestID:
		slog.Warn("update request id too long", "engine", sub.engineID)
		return
	case s.OnUpdate == nil:
		reply(dns.RcodeNotImplemented, "dynamic updates are not enabled")
		return
	case len(req.Message) > maxUpdateMessage:
		reply(dns.RcodeFormatError, "update message too large")
		return
	case !sub.allowUpdate(time.Now()):
		reply(dns.RcodeRefused, "update rate limit exceeded")
		return
	}
	select {
	case sub.updateSlots <- struct{}{}:
	default:
		reply(dns.RcodeRefused, "too many concurrent updates")
		return
	}
	go func() {
		defer func() { <-sub.updateSlots }()
		uctx, cancel := context.WithTimeout(ctx, updateTimeout)
		defer cancel()
		res := s.OnUpdate(uctx, sub.engineID, req, s.connectionFence(uctx, sub))
		if res == nil {
			reply(dns.RcodeServerFailure, "")
			return
		}
		res.RequestId = req.RequestId
		sub.result(res)
	}()
}

// GetBlob streams a blob in BlobChunkSize chunks.
func (s *Server) GetBlob(req *controlv1.GetBlobRequest, stream controlv1.EngineControl_GetBlobServer) error {
	ctx := stream.Context()
	if _, _, err := s.authenticate(ctx); err != nil {
		return err
	}
	if !sha256RE.MatchString(req.Sha256) {
		return status.Error(codes.InvalidArgument, "sha256 must be 64 lowercase hex characters")
	}
	// Blobs are content-addressed and never change, so chunks read by separate statements belong to
	// one value; a blob collected mid-stream ends the stream with NotFound (the engine checks size
	// and SHA-256 and retries).
	var size int64
	if err := s.st.Pool.QueryRow(ctx, "select octet_length(data) from blobs where sha256 = $1", req.Sha256).Scan(&size); err != nil {
		if errors.Is(store.MapError(err), store.ErrNotFound) {
			return status.Error(codes.NotFound, "blob not found")
		}
		return grpcError(err)
	}
	for off := int64(0); off < size; off += BlobChunkSize {
		var chunk []byte
		if err := s.st.Pool.QueryRow(ctx, "select substring(data from $2 for $3) from blobs where sha256 = $1",
			req.Sha256, off+1, BlobChunkSize).Scan(&chunk); err != nil {
			if errors.Is(store.MapError(err), store.ErrNotFound) {
				return status.Error(codes.NotFound, "blob removed while streaming")
			}
			return grpcError(err)
		}
		if err := stream.Send(&controlv1.BlobChunk{Data: chunk}); err != nil {
			return err
		}
	}
	return nil
}

// Authenticate returns the id of the engine whose client certificate the caller presents, after
// checking in the database (on every call, no cache) that the engine exists, is neither deleted
// nor revoked, and that the certificate is its own and not revoked or superseded.
func (s *Server) Authenticate(ctx context.Context) (string, error) {
	id, _, err := s.authenticate(ctx)
	return id, err
}

func (s *Server) authenticate(ctx context.Context) (id, serial string, err error) {
	cert, err := PeerCertificate(ctx)
	if err != nil {
		return "", "", err
	}
	id = cert.Subject.CommonName
	if !uuidRE.MatchString(id) {
		return "", "", status.Error(codes.PermissionDenied, "unknown engine")
	}
	serial = cert.SerialNumber.Text(16)
	if err := s.checkCertificate(ctx, id, serial); err != nil {
		return "", "", err
	}
	return id, serial, nil
}

func (s *Server) checkCertificate(ctx context.Context, id, serial string) error {
	err := fleet.CheckCertificate(ctx, s.st.Pool, uuid.MustParse(id), serial)
	switch {
	case errors.Is(err, fleet.ErrUnknownEngine), errors.Is(err, fleet.ErrCertificateRevoked):
		return status.Error(codes.PermissionDenied, err.Error())
	case err != nil:
		return grpcError(err)
	}
	return nil
}

func (s *Server) certTTL() time.Duration {
	if s.EngineCertTTL > 0 {
		return s.EngineCertTTL
	}
	return pki.EngineCertValidity
}

// renewInterval bounds certificate issuance over control streams to one per engine per interval,
// checked against engines.cert_renewed_at under the engine row lock (reconnects share it).
const renewInterval = 10 * time.Second

// renew answers a CertificateRequest: a CSR for this engine's id gets a certificate of certTTL,
// recorded (it becomes valid alongside the current one, which is superseded once the engine
// connects with the new one) and sent back as CertificateIssued. Refused requests get no answer.
func (s *Server) renew(ctx context.Context, sub *subscriber, req *controlv1.CertificateRequest) error {
	csr, err := x509.ParseCertificateRequest(req.CsrDer)
	if err == nil && csr.Subject.CommonName != sub.engineID {
		err = errors.New("CSR common name is not the engine id")
	}
	if err != nil {
		slog.Warn("certificate request refused: "+err.Error(), "engine", sub.engineID)
		return nil
	}
	var der []byte
	var limited bool
	var signErr error
	err = s.withConnection(ctx, sub, func(tx pgx.Tx) error {
		var live, recent bool
		if err := tx.QueryRow(ctx, `select revoked_at is null and deleted_at is null,
			cert_renewed_at is not null and cert_renewed_at > now() - make_interval(secs => $2)
			from engines where id = $1`, sub.id, renewInterval.Seconds()).Scan(&live, &recent); err != nil {
			return err
		}
		if !live {
			return status.Error(codes.PermissionDenied, fleet.ErrCertificateRevoked.Error())
		}
		if limited = recent; limited {
			return nil
		}
		if der, _, signErr = s.ca.SignEngineCSR(req.CsrDer, sub.engineID, s.certTTL()); signErr != nil {
			return signErr
		}
		if err := fleet.RecordCertificate(ctx, tx, sub.id, der); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "update engines set cert_rotate_requested_at = null, cert_renewed_at = now() where id = $1", sub.id)
		return err
	})
	switch {
	case signErr != nil:
		slog.Warn("certificate request refused: "+signErr.Error(), "engine", sub.engineID)
		return nil
	case err != nil:
		return err
	case limited:
		slog.Warn("certificate request ignored: rate limited", "engine", sub.engineID)
		return nil
	}
	slog.Info("engine certificate issued", "engine", sub.engineID, "reason", req.Reason.String())
	select {
	case sub.control <- &controlv1.ServerMessage{Msg: &controlv1.ServerMessage_CertIssued{CertIssued: &controlv1.CertificateIssued{
		CertDer: der, CaDer: s.ca.Cert.Raw}}}:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
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
