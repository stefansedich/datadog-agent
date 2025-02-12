// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2024-present Datadog, Inc.

// Package npcollectorimpl implements network path collector
package npcollectorimpl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	model "github.com/DataDog/agent-payload/v5/process"
	ddgostatsd "github.com/DataDog/datadog-go/v5/statsd"
	"go.uber.org/atomic"

	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	telemetryComp "github.com/DataDog/datadog-agent/comp/core/telemetry"
	"github.com/DataDog/datadog-agent/comp/forwarder/eventplatform"
	"github.com/DataDog/datadog-agent/comp/networkpath/npcollector/npcollectorimpl/common"
	"github.com/DataDog/datadog-agent/comp/networkpath/npcollector/npcollectorimpl/pathteststore"
	rdnsquerier "github.com/DataDog/datadog-agent/comp/rdnsquerier/def"
	"github.com/DataDog/datadog-agent/pkg/logs/message"
	"github.com/DataDog/datadog-agent/pkg/networkpath/payload"
	"github.com/DataDog/datadog-agent/pkg/networkpath/traceroute"
	"github.com/DataDog/datadog-agent/pkg/networkpath/traceroute/config"
	"github.com/DataDog/datadog-agent/pkg/process/statsd"
)

const (
	networkPathCollectorMetricPrefix    = "datadog.network_path.collector."
	reverseDNSLookupMetricPrefix        = networkPathCollectorMetricPrefix + "reverse_dns_lookup."
	reverseDNSLookupFailuresMetricName  = reverseDNSLookupMetricPrefix + "failures"
	reverseDNSLookupSuccessesMetricName = reverseDNSLookupMetricPrefix + "successes"
)

type npCollectorImpl struct {
	collectorConfigs *collectorConfigs

	// Deps
	epForwarder  eventplatform.Forwarder
	logger       log.Component
	statsdClient ddgostatsd.ClientInterface
	rdnsquerier  rdnsquerier.Component

	// Counters
	receivedPathtestCount    *atomic.Uint64
	processedTracerouteCount *atomic.Uint64

	// Pathtest store
	pathtestStore          *pathteststore.Store
	pathtestInputChan      chan *common.Pathtest
	pathtestProcessingChan chan *pathteststore.PathtestContext

	// Scheduling related
	running       bool
	workers       int
	stopChan      chan struct{}
	flushLoopDone chan struct{}
	runDone       chan struct{}
	flushInterval time.Duration

	// Telemetry component
	telemetrycomp telemetryComp.Component

	// structures needed to ease mocking/testing
	TimeNowFn func() time.Time
	// TODO: instead of mocking traceroute via function replacement like this
	//       we should ideally create a fake/mock traceroute instance that can be passed/injected in NpCollector
	runTraceroute func(cfg config.Config, telemetrycomp telemetryComp.Component) (payload.NetworkPath, error)

	networkDevicesNamespace string
}

func newNoopNpCollectorImpl() *npCollectorImpl {
	return &npCollectorImpl{
		collectorConfigs: &collectorConfigs{},
	}
}

func newNpCollectorImpl(epForwarder eventplatform.Forwarder, collectorConfigs *collectorConfigs, logger log.Component, telemetrycomp telemetryComp.Component, rdnsquerier rdnsquerier.Component) *npCollectorImpl {
	logger.Infof("New NpCollector (workers=%d timeout=%d max_ttl=%d input_chan_size=%d processing_chan_size=%d pathtest_contexts_limit=%d pathtest_ttl=%s pathtest_interval=%s flush_interval=%s reverse_dns_enabled=%t reverse_dns_timeout=%d)",
		collectorConfigs.workers,
		collectorConfigs.timeout,
		collectorConfigs.maxTTL,
		collectorConfigs.pathtestInputChanSize,
		collectorConfigs.pathtestProcessingChanSize,
		collectorConfigs.storeConfig.ContextsLimit,
		collectorConfigs.storeConfig.TTL,
		collectorConfigs.storeConfig.Interval,
		collectorConfigs.storeConfig.MaxPerMinute,
		collectorConfigs.flushInterval,
		collectorConfigs.reverseDNSEnabled,
		collectorConfigs.reverseDNSTimeout,
	)

	return &npCollectorImpl{
		epForwarder:      epForwarder,
		collectorConfigs: collectorConfigs,
		rdnsquerier:      rdnsquerier,
		logger:           logger,

		// pathtestStore is set in start() after statsd.Client is configured
		pathtestStore:          nil,
		pathtestInputChan:      make(chan *common.Pathtest, collectorConfigs.pathtestInputChanSize),
		pathtestProcessingChan: make(chan *pathteststore.PathtestContext, collectorConfigs.pathtestProcessingChanSize),
		flushInterval:          collectorConfigs.flushInterval,
		workers:                collectorConfigs.workers,

		networkDevicesNamespace: collectorConfigs.networkDevicesNamespace,

		receivedPathtestCount:    atomic.NewUint64(0),
		processedTracerouteCount: atomic.NewUint64(0),
		TimeNowFn:                time.Now,

		telemetrycomp: telemetrycomp,

		stopChan:      make(chan struct{}),
		runDone:       make(chan struct{}),
		flushLoopDone: make(chan struct{}),

		runTraceroute: runTraceroute,
	}
}

// makePathtest extracts pathtest information using a single connection and the connection check's reverse dns map
func makePathtest(conn *model.Connection, dns map[string]*model.DNSEntry) common.Pathtest {
	protocol := convertProtocol(conn.GetType())

	rDNSEntry := dns[conn.Raddr.GetIp()]
	var reverseDNSHostname string
	if rDNSEntry != nil && len(rDNSEntry.Names) > 0 {
		reverseDNSHostname = rDNSEntry.Names[0]
	}

	var remotePort uint16
	// UDP traces should not be done to the active port
	if protocol != payload.ProtocolUDP {
		remotePort = uint16(conn.Raddr.GetPort())
	}

	sourceContainer := conn.Laddr.GetContainerId()

	return common.Pathtest{
		Hostname:          conn.Raddr.GetIp(),
		Port:              remotePort,
		Protocol:          protocol,
		SourceContainerID: sourceContainer,
		Metadata: common.PathtestMetadata{
			ReverseDNSHostname: reverseDNSHostname,
		},
	}
}

func (s *npCollectorImpl) ScheduleConns(conns []*model.Connection, dns map[string]*model.DNSEntry) {
	if !s.collectorConfigs.connectionsMonitoringEnabled {
		return
	}
	startTime := s.TimeNowFn()
	s.statsdClient.Count(networkPathCollectorMetricPrefix+"schedule.conns_received", int64(len(conns)), []string{}, 1) //nolint:errcheck
	for _, conn := range conns {
		if !shouldScheduleNetworkPathForConn(conn) {
			protocol := convertProtocol(conn.GetType())
			s.logger.Tracef("Skipped connection: addr=%s, protocol=%s", conn.Raddr, protocol)
			continue
		}
		pathtest := makePathtest(conn, dns)

		err := s.scheduleOne(&pathtest)
		if err != nil {
			s.logger.Errorf("Error scheduling pathtests: %s", err)
		}
	}

	scheduleDuration := s.TimeNowFn().Sub(startTime)
	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"schedule.duration", scheduleDuration.Seconds(), nil, 1) //nolint:errcheck
}

// scheduleOne schedules pathtests.
// It shouldn't block, if the input channel is full, an error is returned.
func (s *npCollectorImpl) scheduleOne(pathtest *common.Pathtest) error {
	if s.pathtestInputChan == nil {
		return errors.New("no input channel, please check that network path is enabled")
	}
	s.logger.Debugf("Schedule traceroute for: hostname=%s port=%d", pathtest.Hostname, pathtest.Port)

	s.statsdClient.Incr(networkPathCollectorMetricPrefix+"schedule.pathtest_count", []string{}, 1) //nolint:errcheck
	select {
	case s.pathtestInputChan <- pathtest:
		s.statsdClient.Incr(networkPathCollectorMetricPrefix+"schedule.pathtest_processed", []string{}, 1) //nolint:errcheck
		return nil
	default:
		s.statsdClient.Incr(networkPathCollectorMetricPrefix+"schedule.pathtest_dropped", []string{"reason:input_chan_full"}, 1) //nolint:errcheck
		return fmt.Errorf("collector input channel is full (channel capacity is %d)", cap(s.pathtestInputChan))
	}
}

func (s *npCollectorImpl) initStatsdClient(statsdClient ddgostatsd.ClientInterface) {
	// Assigning statsd.Client in start() stage since we can't do it in newNpCollectorImpl
	// due to statsd.Client not being configured yet.
	s.statsdClient = statsdClient

	s.pathtestStore = pathteststore.NewPathtestStore(s.collectorConfigs.storeConfig, s.logger, statsdClient, s.TimeNowFn)
}

func (s *npCollectorImpl) start() error {
	if s.running {
		return errors.New("server already started")
	}
	s.running = true

	s.logger.Info("Start NpCollector")

	s.initStatsdClient(statsd.Client)

	go s.listenPathtests()
	go s.flushLoop()
	s.startWorkers()

	return nil
}

func (s *npCollectorImpl) stop() {
	s.logger.Info("Stop NpCollector")
	if !s.running {
		return
	}
	close(s.stopChan)
	<-s.flushLoopDone
	<-s.runDone
	s.running = false
}

func (s *npCollectorImpl) listenPathtests() {
	s.logger.Debug("Starting listening for pathtests")
	for {
		select {
		case <-s.stopChan:
			s.logger.Info("Stopped listening for pathtests")
			s.runDone <- struct{}{}
			return
		case ptest := <-s.pathtestInputChan:
			s.logger.Debugf("Pathtest received: %+v", ptest)
			s.receivedPathtestCount.Inc()
			s.pathtestStore.Add(ptest)
		}
	}
}

func (s *npCollectorImpl) runTracerouteForPath(ptest *pathteststore.PathtestContext) {
	s.logger.Debugf("Run Traceroute for ptest: %+v", ptest)

	cfg := config.Config{
		DestHostname: ptest.Pathtest.Hostname,
		DestPort:     ptest.Pathtest.Port,
		MaxTTL:       uint8(s.collectorConfigs.maxTTL),
		Timeout:      s.collectorConfigs.timeout,
		Protocol:     ptest.Pathtest.Protocol,
	}

	path, err := s.runTraceroute(cfg, s.telemetrycomp)
	if err != nil {
		s.logger.Errorf("%s", err)
		return
	}
	path.Source.ContainerID = ptest.Pathtest.SourceContainerID
	path.Namespace = s.networkDevicesNamespace
	path.Origin = payload.PathOriginNetworkTraffic

	// Perform reverse DNS lookup on destination and hop IPs
	s.enrichPathWithRDNS(&path, ptest.Pathtest.Metadata.ReverseDNSHostname)

	payloadBytes, err := json.Marshal(path)
	if err != nil {
		s.logger.Errorf("json marshall error: %s", err)
	} else {
		s.logger.Debugf("network path event: %s", string(payloadBytes))
		m := message.NewMessage(payloadBytes, nil, "", 0)
		err = s.epForwarder.SendEventPlatformEventBlocking(m, eventplatform.EventTypeNetworkPath)
		if err != nil {
			s.logger.Errorf("failed to send event to epForwarder: %s", err)
		}
	}
}

func runTraceroute(cfg config.Config, telemetry telemetryComp.Component) (payload.NetworkPath, error) {
	tr, err := traceroute.New(cfg, telemetry)
	if err != nil {
		return payload.NetworkPath{}, fmt.Errorf("new traceroute error: %s", err)
	}
	path, err := tr.Run(context.TODO())
	if err != nil {
		return payload.NetworkPath{}, fmt.Errorf("run traceroute error: %s", err)
	}
	return path, nil
}

func (s *npCollectorImpl) flushLoop() {
	s.logger.Debugf("Starting flush loop")

	flushTicker := time.NewTicker(s.flushInterval)

	var lastFlushTime time.Time
	for {
		select {
		// stop sequence
		case <-s.stopChan:
			s.logger.Info("Stopped flush loop")
			s.flushLoopDone <- struct{}{}
			flushTicker.Stop()
			return
		// automatic flush sequence
		case flushTime := <-flushTicker.C:
			s.flushWrapper(flushTime, lastFlushTime)
			lastFlushTime = flushTime
		}
	}
}

func (s *npCollectorImpl) flushWrapper(flushTime time.Time, lastFlushTime time.Time) {
	s.logger.Debugf("Flush loop at %s", flushTime)
	if !lastFlushTime.IsZero() {
		flushInterval := flushTime.Sub(lastFlushTime)
		s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"flush.interval", flushInterval.Seconds(), []string{}, 1) //nolint:errcheck
	}

	s.flush()
	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"flush.duration", s.TimeNowFn().Sub(flushTime).Seconds(), []string{}, 1) //nolint:errcheck
}

func (s *npCollectorImpl) flush() {
	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"workers", float64(s.workers), []string{}, 1) //nolint:errcheck

	flushTime := s.TimeNowFn()
	pathtestsToFlush := s.pathtestStore.Flush()

	flowsContexts := s.pathtestStore.GetContextsCount()
	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"pathtest_store_size", float64(flowsContexts), []string{}, 1) //nolint:errcheck
	s.logger.Debugf("Flushing %d flows to the forwarder (flush_duration=%d, flow_contexts_before_flush=%d)", len(pathtestsToFlush), time.Since(flushTime).Milliseconds(), flowsContexts)

	s.statsdClient.Count(networkPathCollectorMetricPrefix+"flush.pathtest_count", int64(len(pathtestsToFlush)), []string{}, 1) //nolint:errcheck
	for _, ptConf := range pathtestsToFlush {
		s.logger.Tracef("flushed ptConf %s:%d", ptConf.Pathtest.Hostname, ptConf.Pathtest.Port)
		select {
		case s.pathtestProcessingChan <- ptConf:
			s.statsdClient.Incr(networkPathCollectorMetricPrefix+"flush.pathtest_processed", []string{}, 1) //nolint:errcheck
		default:
			s.statsdClient.Incr(networkPathCollectorMetricPrefix+"flush.pathtest_dropped", []string{"reason:processing_chan_full"}, 1) //nolint:errcheck
			s.logger.Tracef("collector processing channel is full (channel capacity is %d)", cap(s.pathtestProcessingChan))
		}
	}

	// keep this metric after the flows are flushed
	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"processing_chan_size", float64(len(s.pathtestProcessingChan)), []string{}, 1) //nolint:errcheck

	s.statsdClient.Gauge(networkPathCollectorMetricPrefix+"input_chan_size", float64(len(s.pathtestInputChan)), []string{}, 1) //nolint:errcheck
}

// enrichPathWithRDNS populates a NetworkPath with reverse-DNS queried hostnames.
func (s *npCollectorImpl) enrichPathWithRDNS(path *payload.NetworkPath, knownDestHostname string) {
	if !s.collectorConfigs.reverseDNSEnabled {
		return
	}

	// collect unique IP addresses from destination and hops
	ipSet := make(map[string]struct{}, len(path.Hops)+1) // +1 for destination

	// only look up the destination hostname if we need to
	if knownDestHostname == "" {
		ipSet[path.Destination.IPAddress] = struct{}{}
	}
	for _, hop := range path.Hops {
		if !hop.Reachable {
			continue
		}
		ipSet[hop.IPAddress] = struct{}{}
	}
	ipAddrs := make([]string, 0, len(ipSet))
	for ip := range ipSet {
		ipAddrs = append(ipAddrs, ip)
	}

	ctx, cancel := context.WithTimeout(context.Background(), s.collectorConfigs.reverseDNSTimeout)
	defer cancel()

	// perform reverse DNS lookup on destination and hops
	results := s.rdnsquerier.GetHostnames(ctx, ipAddrs)
	if len(results) != len(ipAddrs) {
		s.statsdClient.Incr(reverseDNSLookupMetricPrefix+"results_length_mismatch", []string{}, 1) //nolint:errcheck
		s.logger.Debugf("Reverse lookup failed for all hops in path from %s to %s", path.Source.Hostname, path.Destination.Hostname)
	}

	// assign resolved hostnames to destination and hops
	if knownDestHostname != "" {
		path.Destination.ReverseDNSHostname = knownDestHostname
	} else {
		hostname := s.getReverseDNSResult(path.Destination.IPAddress, results)
		// if hostname is blank, use what's given by traceroute
		// TODO: would it be better to move the logic up from the traceroute command?
		// benefit to the current approach is having consistent behavior for all paths
		// both static and dynamic
		if hostname != "" {
			path.Destination.ReverseDNSHostname = hostname
		}
	}

	for i, hop := range path.Hops {
		if !hop.Reachable {
			continue
		}
		hostname := s.getReverseDNSResult(hop.IPAddress, results)
		if hostname != "" {
			path.Hops[i].Hostname = hostname
		}
	}
}

func (s *npCollectorImpl) getReverseDNSResult(ipAddr string, results map[string]rdnsquerier.ReverseDNSResult) string {
	result, ok := results[ipAddr]
	if !ok {
		s.statsdClient.Incr(reverseDNSLookupFailuresMetricName, []string{"reason:absent"}, 1) //nolint:errcheck
		s.logger.Tracef("Reverse DNS lookup failed for IP %s", ipAddr)
		return ""
	}
	if result.Err != nil {
		s.statsdClient.Incr(reverseDNSLookupFailuresMetricName, []string{"reason:error"}, 1) //nolint:errcheck
		s.logger.Tracef("Reverse lookup failed for hop IP %s: %s", ipAddr, result.Err)
		return ""
	}
	if result.Hostname == "" {
		s.statsdClient.Incr(reverseDNSLookupSuccessesMetricName, []string{"status:empty"}, 1) //nolint:errcheck
	} else {
		s.statsdClient.Incr(reverseDNSLookupSuccessesMetricName, []string{"status:found"}, 1) //nolint:errcheck
	}
	return result.Hostname
}

func (s *npCollectorImpl) startWorkers() {
	s.logger.Debugf("Starting workers (%d)", s.workers)
	for w := 0; w < s.workers; w++ {
		s.logger.Debugf("Starting worker #%d", w)
		go s.startWorker(w)
	}
}

func (s *npCollectorImpl) startWorker(workerID int) {
	for {
		select {
		case <-s.stopChan:
			s.logger.Debugf("[worker%d] Stopped worker", workerID)
			return
		case pathtestCtx := <-s.pathtestProcessingChan:
			s.logger.Debugf("[worker%d] Handling pathtest hostname=%s, port=%d", workerID, pathtestCtx.Pathtest.Hostname, pathtestCtx.Pathtest.Port)
			startTime := s.TimeNowFn()

			s.runTracerouteForPath(pathtestCtx)
			s.processedTracerouteCount.Inc()

			checkInterval := pathtestCtx.LastFlushInterval()
			checkDuration := s.TimeNowFn().Sub(startTime)
			s.statsdClient.Histogram(networkPathCollectorMetricPrefix+"worker.task_duration", checkDuration.Seconds(), nil, 1)     //nolint:errcheck
			s.statsdClient.Incr(networkPathCollectorMetricPrefix+"worker.pathtest_processed", []string{}, 1)                       //nolint:errcheck
			s.statsdClient.Histogram(networkPathCollectorMetricPrefix+"worker.pathtest_interval", checkInterval.Seconds(), nil, 1) //nolint:errcheck
		}
	}
}
