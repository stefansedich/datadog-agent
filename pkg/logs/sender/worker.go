// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

package sender

import (
	"strconv"
	"sync"
	"time"

	pkgconfigmodel "github.com/DataDog/datadog-agent/pkg/config/model"
	"github.com/DataDog/datadog-agent/pkg/logs/auditor"
	"github.com/DataDog/datadog-agent/pkg/logs/client"
	"github.com/DataDog/datadog-agent/pkg/logs/message"
	"github.com/DataDog/datadog-agent/pkg/logs/metrics"
	"github.com/DataDog/datadog-agent/pkg/telemetry"
)

var (
	tlmPayloadsDropped = telemetry.NewCounterWithOpts("logs_sender", "payloads_dropped", []string{"reliable", "destination"}, "Payloads dropped", telemetry.Options{DefaultMetric: true})
	tlmMessagesDropped = telemetry.NewCounterWithOpts("logs_sender", "messages_dropped", []string{"reliable", "destination"}, "Messages dropped", telemetry.Options{DefaultMetric: true})
	tlmSendWaitTime    = telemetry.NewCounter("logs_sender", "send_wait", []string{}, "Time spent waiting for all sends to finish")
)

// worker sends logs to different destinations. Destinations can be either
// reliable or unreliable. The worker ensures that logs are sent to at least
// one reliable destination and will block the pipeline if they are in an
// error state. Unreliable destinations will only send logs when at least
// one reliable destination is also sending logs. However they do not update
// the auditor or block the pipeline if they fail. There will always be at
// least 1 reliable destination (the main destination).
type worker struct {
	auditor        auditor.Auditor
	config         pkgconfigmodel.Reader
	inputChan      chan *message.Payload
	outputChan     chan *message.Payload
	destinations   *client.Destinations
	done           chan struct{}
	finished       chan struct{}
	bufferSize     int
	senderDoneChan chan *sync.WaitGroup
	flushWg        *sync.WaitGroup

	pipelineMonitor metrics.PipelineMonitor
	utilization     metrics.UtilizationMonitor
}

func newWorkerLegacy(
	config pkgconfigmodel.Reader,
	inputChan chan *message.Payload,
	outputChan chan *message.Payload,
	destinations *client.Destinations,
	bufferSize int,
	senderDoneChan chan *sync.WaitGroup,
	flushWg *sync.WaitGroup,
	pipelineMonitor metrics.PipelineMonitor,
) *worker {
	return &worker{
		outputChan:     outputChan,
		config:         config,
		inputChan:      inputChan,
		destinations:   destinations,
		bufferSize:     bufferSize,
		senderDoneChan: senderDoneChan,
		flushWg:        flushWg,
		done:           make(chan struct{}),
		finished:       make(chan struct{}),

		// Telemetry
		pipelineMonitor: pipelineMonitor,
		utilization:     pipelineMonitor.MakeUtilizationMonitor("sender"),
	}
}

func newWorker(
	config pkgconfigmodel.Reader,
	inputChan chan *message.Payload,
	auditor auditor.Auditor,
	destinations *client.Destinations,
	bufferSize int,
	senderDoneChan chan *sync.WaitGroup,
	flushWg *sync.WaitGroup,
	pipelineMonitor metrics.PipelineMonitor,
) *worker {
	return &worker{
		auditor:        auditor,
		config:         config,
		inputChan:      inputChan,
		destinations:   destinations,
		bufferSize:     bufferSize,
		senderDoneChan: senderDoneChan,
		flushWg:        flushWg,
		done:           make(chan struct{}),
		finished:       make(chan struct{}),

		// Telemetry
		pipelineMonitor: pipelineMonitor,
		utilization:     pipelineMonitor.MakeUtilizationMonitor("sender"),
	}
}

// Start starts the worker.
func (s *worker) start() {
	if s.auditor != nil {
		s.outputChan = s.auditor.Channel()
	}

	go s.run()
}

// Stop stops the worker,
// this call blocks until inputChan is flushed
func (s *worker) stop() {
	s.done <- struct{}{}
	<-s.finished
}

func (s *worker) run() {
	reliableDestinations := buildDestinationSenders(s.config, s.destinations.Reliable, s.outputChan, s.bufferSize)

	sink := additionalDestinationsSink(s.bufferSize)
	unreliableDestinations := buildDestinationSenders(s.config, s.destinations.Unreliable, sink, s.bufferSize)
	continueLoop := true
	for continueLoop {
		select {
		case payload := <-s.inputChan:
			s.utilization.Start()
			var startInUse = time.Now()
			senderDoneWg := &sync.WaitGroup{}

			sent := false
			for !sent {
				for _, destSender := range reliableDestinations {
					if destSender.Send(payload) {
						if destSender.destination.Metadata().ReportingEnabled {
							s.pipelineMonitor.ReportComponentIngress(payload, destSender.destination.Metadata().MonitorTag())
						}
						sent = true
						if s.senderDoneChan != nil {
							senderDoneWg.Add(1)
							s.senderDoneChan <- senderDoneWg
						}
					}
				}

				if !sent {
					// Throttle the poll loop while waiting for a send to succeed
					// This will only happen when all reliable destinations
					// are blocked so logs have no where to go.
					time.Sleep(100 * time.Millisecond)
				}
			}

			for i, destSender := range reliableDestinations {
				// If an endpoint is stuck in the previous step, try to buffer the payloads if we have room to mitigate
				// loss on intermittent failures.
				if !destSender.lastSendSucceeded {
					if !destSender.NonBlockingSend(payload) {
						tlmPayloadsDropped.Inc("true", strconv.Itoa(i))
						tlmMessagesDropped.Add(float64(payload.Count()), "true", strconv.Itoa(i))
					}
				}
			}

			// Attempt to send to unreliable destinations
			for i, destSender := range unreliableDestinations {
				if !destSender.NonBlockingSend(payload) {
					tlmPayloadsDropped.Inc("false", strconv.Itoa(i))
					tlmMessagesDropped.Add(float64(payload.Count()), "false", strconv.Itoa(i))
					if s.senderDoneChan != nil {
						senderDoneWg.Add(1)
						s.senderDoneChan <- senderDoneWg
					}
				}
			}

			inUse := float64(time.Since(startInUse) / time.Millisecond)
			tlmSendWaitTime.Add(inUse)
			s.utilization.Stop()

			if s.senderDoneChan != nil && s.flushWg != nil {
				// Wait for all destinations to finish sending the payload
				senderDoneWg.Wait()
				// Decrement the wait group when this payload has been sent
				s.flushWg.Done()
			}
			s.pipelineMonitor.ReportComponentEgress(payload, "sender")
		case <-s.done:
			continueLoop = false
		}
	}

	// Cleanup the destinations
	for _, destSender := range reliableDestinations {
		destSender.Stop()
	}
	for _, destSender := range unreliableDestinations {
		destSender.Stop()
	}
	close(sink)
	s.finished <- struct{}{}
}

// Drains the output channel from destinations that don't update the auditor.
func additionalDestinationsSink(bufferSize int) chan *message.Payload {
	sink := make(chan *message.Payload, bufferSize)
	go func() {
		// drain channel, stop when channel is closed
		//nolint:revive // TODO(AML) Fix revive linter
		for range sink {
		}
	}()
	return sink
}

func buildDestinationSenders(config pkgconfigmodel.Reader, destinations []client.Destination, output chan *message.Payload, bufferSize int) []*DestinationSender {
	destinationSenders := []*DestinationSender{}
	for _, destination := range destinations {
		destinationSenders = append(destinationSenders, NewDestinationSender(config, destination, output, bufferSize))
	}
	return destinationSenders
}
