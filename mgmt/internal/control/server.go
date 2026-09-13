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

	// OnStats, when set, receives every Stats message.
	OnStats func(ctx context.Context, engineID string, s *controlv1.Stats)
	// OnNotify, when set, receives every NOTIFY an engine accepted for a secondary zone; an error
	// (NOTIFY ignored) is logged and the stream continues.
	OnNotify func(ctx context.Context, engineID string, ev *controlv1.NotifyReceived) error
	// OnUpdate, when set, applies a dynamic update an engine forwarded. It runs outside the receive
	// loop with a 4 s context; without it every update is answered NOTIMP.
	OnUpdate func(ctx context.Context, engineID string, req *controlv1.UpdateRequest) *controlv1.UpdateResult
	// EngineCertTTL is the lifetime of issued engine certificates (0: pki.EngineCertValidity).
	EngineCertTTL time.Duration

	st         *store.Store
	ca         *pki.CA
	hub        *Hub
	instanceID string
	dnsTLS     *DNSTLSFanout
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
	// The engine connected with this certificate: an earlier one is no longer needed (a renewed
	// certificate replaces the old one only once it has proven to work).
	if err := fleet.SupersedeOlderCertificates(ctx, s.st.Pool, uuid.MustParse(id), serial); err != nil {
		return grpcError(err)
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
	tlsCh := s.dnsTLS.Register(id, hello.TlsFingerprintSha256)
	defer s.dnsTLS.Unregister(id)
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
		if _, err := s.st.Pool.Exec(ctx, "update engines set version_ahead = true where id = $1", id); err != nil {
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
			_, err = s.st.Pool.Exec(ctx, `update engines set applied_version = $2, persist_error = $3, version_ahead = false,
				rejected_reason = case when rejected_version <= $2 then '' else rejected_reason end,
				rejected_version = case when rejected_version <= $2 then null else rejected_version end,
				last_seen_at = now()
				where id = $1`, sub.engineID, int64(m.Applied.Version), m.Applied.PersistError)
			if err == nil {
				err = s.notifyRollout(ctx, sub)
			}
		case *controlv1.EngineMessage_Rejected:
			sub.observe(m.Rejected.Version)
			_, err = s.st.Pool.Exec(ctx, "update engines set rejected_version = $2, rejected_reason = $3, last_seen_at = now() where id = $1",
				sub.engineID, int64(m.Rejected.Version), m.Rejected.Reason)
			if err == nil {
				err = s.notifyRollout(ctx, sub)
			}
		case *controlv1.EngineMessage_Stats:
			_, err = s.st.Pool.Exec(ctx, "update engines set last_seen_at = now() where id = $1", sub.engineID)
			if s.OnStats != nil {
				s.OnStats(ctx, sub.engineID, m.Stats)
			}
		case *controlv1.EngineMessage_NotifyReceived:
			if s.OnNotify != nil {
				ev := m.NotifyReceived
				if nerr := s.OnNotify(ctx, sub.engineID, ev); nerr != nil {
					slog.Info("notify not acted on", "engine", sub.engineID, "zone", ev.Zone, "source", ev.Source, "err", nerr)
				}
			}
		case *controlv1.EngineMessage_UpdateRequest:
			s.update(ctx, sub, m.UpdateRequest)
		case *controlv1.EngineMessage_CertRequest:
			err = s.renew(ctx, sub, m.CertRequest)
		case *controlv1.EngineMessage_TlsMaterialResult:
			res := m.TlsMaterialResult
			s.dnsTLS.Result(sub.engineID, res)
			slog.Info("dns tls result", "engine", sub.engineID, "fingerprint", res.FingerprintSha256, "applied", res.Applied, "error", res.Error)
			err = store.UpsertEngineTLSState(ctx, s.st.Pool, store.EngineTLSState{
				EngineID: uuid.MustParse(sub.engineID), Fingerprint: res.FingerprintSha256, Applied: res.Applied, Error: res.Error,
			})
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
		res := s.OnUpdate(uctx, sub.engineID, req)
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

// renewInterval bounds certificate issuance to one per engine stream per interval.
const renewInterval = 10 * time.Second

// renew answers a CertificateRequest: a CSR for this engine's id gets a certificate of certTTL,
// recorded (it becomes valid alongside the current one, which is superseded once the engine
// connects with the new one) and sent back as CertificateIssued. Refused requests get no answer.
func (s *Server) renew(ctx context.Context, sub *subscriber, req *controlv1.CertificateRequest) error {
	now := time.Now()
	sub.mu.Lock()
	limited := !sub.lastIssued.IsZero() && now.Sub(sub.lastIssued) < renewInterval
	sub.mu.Unlock()
	if limited {
		slog.Warn("certificate request ignored: rate limited", "engine", sub.engineID)
		return nil
	}
	csr, err := x509.ParseCertificateRequest(req.CsrDer)
	if err == nil && csr.Subject.CommonName != sub.engineID {
		err = errors.New("CSR common name is not the engine id")
	}
	if err != nil {
		slog.Warn("certificate request refused: "+err.Error(), "engine", sub.engineID)
		return nil
	}
	der, _, err := s.ca.SignEngineCSR(req.CsrDer, sub.engineID, s.certTTL())
	if err != nil {
		slog.Warn("certificate request refused: "+err.Error(), "engine", sub.engineID)
		return nil
	}
	err = s.st.InTx(ctx, func(tx pgx.Tx) error {
		var live bool
		if err := tx.QueryRow(ctx, "select revoked_at is null and deleted_at is null from engines where id = $1 for update",
			sub.id).Scan(&live); err != nil {
			return err
		}
		if !live {
			return status.Error(codes.PermissionDenied, fleet.ErrCertificateRevoked.Error())
		}
		if err := fleet.RecordCertificate(ctx, tx, sub.id, der); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "update engines set cert_rotate_requested_at = null where id = $1", sub.id)
		return err
	})
	if err != nil {
		return err
	}
	sub.mu.Lock()
	sub.lastIssued = now
	sub.mu.Unlock()
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
