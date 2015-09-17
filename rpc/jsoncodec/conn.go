package jsoncodec

import (
	"encoding/json"
	"net"

	"github.com/gorilla/websocket"
)

// NewWebsocket returns an rpc codec that uses the given websocket
// connection to send and receive messages.
func NewWebsocket(conn *websocket.Conn) *Codec {
	return New(wsJSONConn{conn})
}

type wsJSONConn struct {
	conn *websocket.Conn
}

func (conn wsJSONConn) Send(msg interface{}) error {
	return conn.conn.WriteJSON(msg)
}

func (conn wsJSONConn) Receive(msg interface{}) error {
	return conn.conn.ReadJSON(msg)
}

func (conn wsJSONConn) Close() error {
	return conn.conn.Close()
}

// NewNet returns an rpc codec that uses the given net
// connection to send and receive messages.
func NewNet(conn net.Conn) *Codec {
	return New(&netConn{
		enc:  json.NewEncoder(conn),
		dec:  json.NewDecoder(conn),
		conn: conn,
	})
}

type netConn struct {
	enc  *json.Encoder
	dec  *json.Decoder
	conn net.Conn
}

func (conn *netConn) Send(msg interface{}) error {
	return conn.enc.Encode(msg)
}

func (conn *netConn) Receive(msg interface{}) error {
	return conn.dec.Decode(msg)
}

func (conn *netConn) Close() error {
	return conn.conn.Close()
}
