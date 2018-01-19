// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package pubsub_test

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"time"

	"github.com/juju/loggo"
	"github.com/juju/pubsub"
	jc "github.com/juju/testing/checkers"
	"github.com/juju/utils/clock"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/names.v2"

	"github.com/juju/juju/api"
	"github.com/juju/juju/api/base"
	apipubsub "github.com/juju/juju/api/pubsub"
	"github.com/juju/juju/apiserver"
	"github.com/juju/juju/apiserver/observer"
	"github.com/juju/juju/apiserver/observer/fakeobserver"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/state"
	statetesting "github.com/juju/juju/state/testing"
	coretesting "github.com/juju/juju/testing"
	"github.com/juju/juju/testing/factory"
)

type PubSubSuite struct {
	coretesting.BaseSuite
}

var _ = gc.Suite(&PubSubSuite{})

func (s *PubSubSuite) TestNewAPI(c *gc.C) {
	conn := &mockConnector{
		c: c,
	}
	a := apipubsub.NewAPI(conn)
	w, err := a.OpenMessageWriter()
	c.Assert(err, gc.IsNil)

	msg := new(params.PubSubMessage)
	err = w.ForwardMessage(msg)
	c.Assert(err, gc.IsNil)

	c.Assert(conn.written, gc.HasLen, 1)
	c.Assert(conn.written[0], gc.Equals, msg)

	err = w.Close()
	c.Assert(err, gc.IsNil)
	c.Assert(conn.closeCount, gc.Equals, 1)
}

func (s *PubSubSuite) TestNewAPIWriteLogError(c *gc.C) {
	conn := &mockConnector{
		c:            c,
		connectError: errors.New("foo"),
	}
	a := apipubsub.NewAPI(conn)
	w, err := a.OpenMessageWriter()
	c.Assert(err, gc.ErrorMatches, "cannot connect to /pubsub: foo")
	c.Assert(w, gc.Equals, nil)
}

func (s *PubSubSuite) TestNewAPIWriteError(c *gc.C) {
	conn := &mockConnector{
		c:          c,
		writeError: errors.New("foo"),
	}
	a := apipubsub.NewAPI(conn)
	w, err := a.OpenMessageWriter()
	c.Assert(err, gc.IsNil)
	defer w.Close()

	err = w.ForwardMessage(new(params.PubSubMessage))
	c.Assert(err, gc.ErrorMatches, "cannot send pubsub message: foo")
	c.Assert(conn.written, gc.HasLen, 0)
}

type mockConnector struct {
	c *gc.C

	connectError error
	writeError   error
	written      []interface{}

	closeCount int
}

func (c *mockConnector) ConnectStream(path string, values url.Values) (base.Stream, error) {
	c.c.Assert(path, gc.Equals, "/pubsub")
	c.c.Assert(values, gc.HasLen, 0)
	if c.connectError != nil {
		return nil, c.connectError
	}
	return mockStream{c}, nil
}

type mockStream struct {
	conn *mockConnector
}

func (s mockStream) WriteJSON(v interface{}) error {
	if s.conn.writeError != nil {
		return s.conn.writeError
	}
	s.conn.written = append(s.conn.written, v)
	return nil
}

func (s mockStream) ReadJSON(v interface{}) error {
	s.conn.c.Errorf("ReadJSON called unexpectedly")
	return nil
}

func (s mockStream) NextReader() (messageType int, r io.Reader, err error) {
	// NextReader is now called by the read loop thread.
	// So just wait a bit and return so it doesn't sit in a very tight loop.
	time.Sleep(time.Millisecond)
	return 0, nil, nil
}

func (s mockStream) Close() error {
	s.conn.closeCount++
	return nil
}

type PubSubIntegrationSuite struct {
	statetesting.StateSuite
	machineTag names.Tag
	password   string
	nonce      string
	address    string
	hub        *pubsub.StructuredHub
	server     *apiserver.Server
}

var _ = gc.Suite(&PubSubIntegrationSuite{})

func (s *PubSubIntegrationSuite) SetUpTest(c *gc.C) {
	s.StateSuite.SetUpTest(c)
	loggo.GetLogger("juju.apiserver").SetLogLevel(loggo.TRACE)
	s.nonce = "nonce"
	m, password := s.Factory.MakeMachineReturningPassword(c, &factory.MachineParams{
		Nonce: s.nonce,
		Jobs:  []state.MachineJob{state.JobManageModel},
	})
	s.machineTag = m.Tag()
	s.password = password
	s.hub = pubsub.NewStructuredHub(nil)

	statePool := state.NewStatePool(s.State)
	s.AddCleanup(func(*gc.C) { statePool.Close() })
	s.server, s.address = newServerWithHub(c, statePool, s.hub)
	s.AddCleanup(func(*gc.C) { s.server.Stop() })
}

func (s *PubSubIntegrationSuite) connect(c *gc.C) apipubsub.MessageWriter {
	info := &api.Info{
		Addrs:    []string{s.address},
		CACert:   coretesting.CACert,
		ModelTag: s.IAASModel.ModelTag(),
		Tag:      s.machineTag,
		Password: s.password,
		Nonce:    s.nonce,
	}
	conn, err := api.Open(info, api.DialOpts{})
	c.Assert(err, jc.ErrorIsNil)
	s.AddCleanup(func(_ *gc.C) { conn.Close() })

	a := apipubsub.NewAPI(conn)
	w, err := a.OpenMessageWriter()
	c.Assert(err, jc.ErrorIsNil)
	s.AddCleanup(func(_ *gc.C) { w.Close() })
	return w
}

func (s *PubSubIntegrationSuite) TestMessages(c *gc.C) {
	writer := s.connect(c)
	topic := "test.message"
	messages := []map[string]interface{}{}
	done := make(chan struct{})
	_, err := s.hub.SubscribeMatch(pubsub.MatchAll, func(t string, payload map[string]interface{}) {
		c.Check(t, gc.Equals, topic)
		messages = append(messages, payload)
		if len(messages) == 2 {
			close(done)
		}
	})

	first := map[string]interface{}{
		"key": "value",
	}
	err = writer.ForwardMessage(&params.PubSubMessage{
		Topic: string(topic),
		Data:  first,
	})
	c.Assert(err, jc.ErrorIsNil)

	second := map[string]interface{}{
		"key": "other",
	}
	err = writer.ForwardMessage(&params.PubSubMessage{
		Topic: string(topic),
		Data:  second,
	})
	c.Assert(err, jc.ErrorIsNil)

	select {
	case <-done:
		// messages received
	case <-time.After(coretesting.LongWait):
		c.Fatal("messages not received")
	}
	c.Assert(messages, jc.DeepEquals, []map[string]interface{}{first, second})
}

func newServerWithHub(c *gc.C, statePool *state.StatePool, hub *pubsub.StructuredHub) (*apiserver.Server, string) {
	listener, err := net.Listen("tcp", ":0")
	c.Assert(err, jc.ErrorIsNil)
	srv, err := apiserver.NewServer(statePool, listener, apiserver.ServerConfig{
		Clock:               clock.WallClock,
		GetCertificate:      func() *tls.Certificate { return coretesting.ServerTLSCert },
		Tag:                 names.NewMachineTag("0"),
		LogDir:              c.MkDir(),
		Hub:                 hub,
		NewObserver:         func() observer.Observer { return &fakeobserver.Instance{} },
		RateLimitConfig:     apiserver.DefaultRateLimitConfig(),
		UpgradeComplete:     func() bool { return true },
		RestoreStatus:       func() state.RestoreStatus { return state.RestoreNotActive },
		ProviderRegistry:    environs.GlobalProviderRegistry(),
		ImageSourceRegistry: environs.GlobalImageSourceRegistry(),
	})
	c.Assert(err, jc.ErrorIsNil)
	port := listener.Addr().(*net.TCPAddr).Port
	address := fmt.Sprintf("localhost:%d", port)
	return srv, address
}
