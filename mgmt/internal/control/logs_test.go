package control_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/piwi3910/nexora/e2e/harness"
	controlv1 "github.com/piwi3910/nexora/gen/go/nexora/control/v1"
	"github.com/piwi3910/nexora/mgmt/internal/control"
	"github.com/piwi3910/nexora/mgmt/internal/store"
)

func TestEngineLogsRoutedAcrossInstances(t *testing.T) {
	brokers := []*control.LogBroker{}
	f := setupServers(t, 2, func(st *store.Store, h *control.Hub) {
		b := control.NewLogBroker(st)
		brokers = append(brokers, b)
		h.SetLogBroker(b)
	}, func(s *control.Server) {
		s.SetLogBroker(brokers[len(brokers)-1])
	})
	client, id := f.enroll(t, f.addr[1]) // the engine's stream lives on instance B
	stream, err := client.Connect(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: id, NodeName: "e1", EngineVersion: "m6"}}})
	recvSnapshot(t, stream)
	go func() {
		for {
			m, err := stream.Recv()
			if err != nil {
				return
			}
			if req := m.GetLogRequest(); req != nil {
				_ = stream.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_LogBatch{LogBatch: &controlv1.LogBatch{
					RequestId: req.RequestId, LastSeq: 7, OldestSeq: 1,
					Lines: []*controlv1.LogLine{{Seq: 7, UnixMs: 1, Level: controlv1.LogLevel_LOG_LEVEL_INFO, Message: "nexora-engine: serving version 1 " + req.Contains}},
				}}})
			}
		}
	}()
	engineID := uuid.MustParse(id)
	var batch *controlv1.LogBatch
	harness.Eventually(t, 10*time.Second, func() error {
		var err error
		batch, err = brokers[0].Read(f.ctx, engineID, &controlv1.LogRequest{Contains: "via-A", Limit: 10})
		return err
	})
	if len(batch.Lines) != 1 || !strings.Contains(batch.Lines[0].Message, "via-A") {
		t.Fatalf("batch through instance A: %+v", batch)
	}
	if _, err := brokers[1].Read(f.ctx, engineID, &controlv1.LogRequest{Limit: 10}); err != nil {
		t.Fatalf("stream holder reads directly: %v", err)
	}
	silent, silentID := f.enroll(t, f.addr[0])
	ss, _ := silent.Connect(f.ctx)
	_ = ss.Send(&controlv1.EngineMessage{Msg: &controlv1.EngineMessage_Hello{Hello: &controlv1.Hello{EngineId: silentID, NodeName: "e2", EngineVersion: "m6"}}})
	recvSnapshot(t, ss)
	start := time.Now()
	if _, err := brokers[1].Read(f.ctx, uuid.MustParse(silentID), &controlv1.LogRequest{Limit: 10}); !errors.Is(err, control.ErrEngineTimeout) || time.Since(start) < 4*time.Second {
		t.Fatalf("silent engine -> %v after %s", err, time.Since(start))
	}
	gone, goneID := f.enroll(t, f.addr[0])
	_ = gone
	if _, err := brokers[0].Read(f.ctx, uuid.MustParse(goneID), &controlv1.LogRequest{Limit: 10}); !errors.Is(err, control.ErrEngineDisconnected) {
		t.Fatalf("never-connected engine -> %v", err)
	}
}
