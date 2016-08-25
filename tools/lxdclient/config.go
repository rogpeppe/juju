// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

// +build go1.3

package lxdclient

import (
	"github.com/juju/errors"
)

// Config contains the config values used for a connection to the LXD API.
type Config struct {
	// Remote identifies the remote server to which the client should
	// connect. For the default "remote" use Local.
	Remote Remote
}

// WithDefaults updates a copy of the config with default values
// where needed.
func (cfg Config) WithDefaults() (Config, error) {
	// We leave a blank namespace alone.
	// Also, note that cfg is a value receiver, so it is an implicit copy.

	var err error
	cfg.Remote, err = cfg.Remote.WithDefaults()
	if err != nil {
		return cfg, errors.Trace(err)
	}
	return cfg, nil
}

// Validate checks the client's fields for invalid values.
func (cfg Config) Validate() error {
	if err := cfg.Remote.Validate(); err != nil {
		return errors.Trace(err)
	}

	return nil
}

// UsingTCPRemote converts the config into a "non-local" version. An
// already non-local remote is left alone.
//
// For a "local" remote (see Local), the remote is changed to a one
// with the host set to the IP address of the local lxcbr0 bridge
// interface. The LXD server is also set up for remote access, exposing
// the TCP port and adding a certificate for remote access.
func (cfg Config) UsingTCPRemote() (Config, error) {
	// Note that cfg is a value receiver, so it is an implicit copy.

	if !cfg.Remote.isLocal() {
		return cfg, nil
	}

	client, err := Connect(cfg)
	if err != nil {
		return cfg, errors.Trace(err)
	}

	if _, err := client.ServerStatus(); err != nil {
		return cfg, errors.Trace(err)
	}

	// If the default profile's bridge was never used before, the next call with
	// also activate it and get its address.
	remote, err := cfg.Remote.UsingTCP(client.defaultProfileBridgeName)
	if err != nil {
		return cfg, errors.Trace(err)
	}

	// Update the server config and authorized certs.
	serverCert, err := prepareRemote(client, remote.Cert)
	if err != nil {
		return cfg, errors.Trace(err)
	}
	// Note: jam 2016-02-25 setting ServerPEMCert feels like something
	// that would have been done in UsingTCP. However, we can't know the
	// server's certificate until we've actually connected to it, which
	// happens in prepareRemote
	remote.ServerPEMCert = serverCert

	cfg.Remote = remote
	return cfg, nil
}

func prepareRemote(client *Client, newCert *Cert) (string, error) {
	// Make sure the LXD service is configured to listen to local https
	// requests, rather than only via the Unix socket.
	// TODO: jam 2016-02-25 This tells LXD to listen on all addresses,
	// 	which does expose the LXD to outside requests. It would
	// 	probably be better to only tell LXD to listen for requests on
	// 	the loopback and LXC bridges that we are using.
	if err := client.SetConfig("core.https_address", "[::]"); err != nil {
		return "", errors.Trace(err)
	}

	if newCert != nil {
		// Make sure the LXD service will allow
		// our certificate to authenticate.
		if err := client.AddCert(*newCert); err != nil {
			return "", errors.Trace(err)
		}
	}

	st, err := client.ServerStatus()
	if err != nil {
		return "", errors.Trace(err)
	}

	return st.Environment.Certificate, nil
}