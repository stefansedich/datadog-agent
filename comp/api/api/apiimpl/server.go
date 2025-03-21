// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package apiimpl

import (
	"crypto/tls"
	"fmt"
	stdLog "log"
	"net"
	"net/http"
	"strconv"

	"github.com/DataDog/datadog-agent/comp/api/api/apiimpl/observability"
	"github.com/DataDog/datadog-agent/pkg/util/log"
	pkglogsetup "github.com/DataDog/datadog-agent/pkg/util/log/setup"
)

func startServer(listener net.Listener, srv *http.Server, name string) {
	// Use a stack depth of 4 on top of the default one to get a relevant filename in the stdlib
	logWriter, _ := pkglogsetup.NewLogWriter(5, log.ErrorLvl)

	srv.ErrorLog = stdLog.New(logWriter, fmt.Sprintf("Error from the Agent HTTP server '%s': ", name), 0) // log errors to seelog

	tlsListener := tls.NewListener(listener, srv.TLSConfig)

	go srv.Serve(tlsListener) //nolint:errcheck

	log.Infof("Started HTTP server '%s' on %s", name, listener.Addr().String())
}

func stopServer(listener net.Listener, name string) {
	if listener != nil {
		if err := listener.Close(); err != nil {
			log.Errorf("Error stopping HTTP server '%s': %s", name, err)
		} else {
			log.Infof("Stopped HTTP server '%s'", name)
		}
	}
}

// StartServers creates certificates and starts API + IPC servers
func (server *apiServer) startServers() error {
	apiAddr, err := getIPCAddressPort()
	if err != nil {
		return fmt.Errorf("unable to get IPC address and port: %v", err)
	}

	tmf := observability.NewTelemetryMiddlewareFactory(server.telemetry)

	// start the CMD server
	if err := server.startCMDServer(
		apiAddr,
		tmf,
	); err != nil {
		return fmt.Errorf("unable to start CMD API server: %v", err)
	}

	// start the IPC server
	if ipcServerPort := server.cfg.GetInt("agent_ipc.port"); ipcServerPort > 0 {
		ipcServerHost := server.cfg.GetString("agent_ipc.host")
		ipcServerHostPort := net.JoinHostPort(ipcServerHost, strconv.Itoa(ipcServerPort))

		if err := server.startIPCServer(ipcServerHostPort, tmf); err != nil {
			// if we fail to start the IPC server, we should stop the CMD server
			server.stopServers()
			return fmt.Errorf("unable to start IPC API server: %v", err)
		}
	}

	return nil
}

// StopServers closes the connections and the servers
func (server *apiServer) stopServers() {
	stopServer(server.cmdListener, cmdServerName)
	stopServer(server.ipcListener, ipcServerName)
}
