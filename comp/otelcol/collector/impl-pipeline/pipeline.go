// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build otlp

// Package pipelineimpl implements the collector component
package pipelineimpl

import (
	"context"
	"net/http"
	"time"

	"github.com/DataDog/datadog-agent/comp/api/authtoken"
	"github.com/DataDog/datadog-agent/comp/core/config"
	flaretypes "github.com/DataDog/datadog-agent/comp/core/flare/types"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	"github.com/DataDog/datadog-agent/comp/core/status"
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	compdef "github.com/DataDog/datadog-agent/comp/def"
	"github.com/DataDog/datadog-agent/comp/metadata/inventoryagent"
	collector "github.com/DataDog/datadog-agent/comp/otelcol/collector/def"
	"github.com/DataDog/datadog-agent/comp/otelcol/logsagentpipeline"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/configcheck"
	"github.com/DataDog/datadog-agent/comp/otelcol/otlp/datatype"
	apiutil "github.com/DataDog/datadog-agent/pkg/api/util"
	"github.com/DataDog/datadog-agent/pkg/logs/message"
	"github.com/DataDog/datadog-agent/pkg/serializer"
	"github.com/DataDog/datadog-agent/pkg/util/option"
)

const (
	otlpEnabled = "feature_otlp_enabled"
)

// Requires specifies a list of dependencies required for the collector
// to be instantiated.
type Requires struct {
	// Lc specifies the lifecycle settings, used for appending startup
	// and shutdown hooks.
	Lc compdef.Lifecycle

	// Config specifies the Datadog Agent's configuration component.
	Config config.Component

	// Log specifies the logging component.
	Log log.Component

	// Authtoken specifies the authentication token component.
	Authtoken authtoken.Component

	// Serializer specifies the metrics serializer that is used to export metrics
	// to Datadog.
	Serializer serializer.MetricSerializer

	// LogsAgent specifies a logs agent
	LogsAgent option.Option[logsagentpipeline.Component]

	// InventoryAgent require the inventory metadata payload, allowing otelcol to add data to it.
	InventoryAgent inventoryagent.Component

	Tagger tagger.Component
}

// Provides specifics the types returned by the constructor
type Provides struct {
	compdef.Out

	Comp           collector.Component
	FlareProvider  flaretypes.Provider
	StatusProvider status.InformationProvider
}

type collectorImpl struct {
	authToken      authtoken.Component
	col            *otlp.Pipeline
	config         config.Component
	log            log.Component
	serializer     serializer.MetricSerializer
	logsAgent      option.Option[logsagentpipeline.Component]
	inventoryAgent inventoryagent.Component
	tagger         tagger.Component
	client         *http.Client
	ctx            context.Context
}

func (c *collectorImpl) start(context.Context) error {
	on := configcheck.IsEnabled(c.config)
	c.inventoryAgent.Set(otlpEnabled, on)
	if !on {
		return nil
	}
	var logch chan *message.Message
	if v, ok := c.logsAgent.Get(); ok {
		if provider := v.GetPipelineProvider(); provider != nil {
			logch = provider.NextPipelineChan()
		}
	}
	var err error
	col, err := otlp.NewPipelineFromAgentConfig(c.config, c.serializer, logch, c.tagger)
	if err != nil {
		// failure to start the OTLP component shouldn't fail startup
		c.log.Errorf("Error creating the OTLP ingest pipeline: %v", err)
		return nil
	}
	c.col = col
	go func() {
		if err := col.Run(c.ctx); err != nil {
			c.log.Errorf("Error running the OTLP ingest pipeline: %v", err)
		}
	}()
	return nil
}

func (c *collectorImpl) stop(context.Context) error {
	if c.col != nil {
		c.col.Stop()
	}
	return nil
}

// Status returns the status of the collector.
func (c *collectorImpl) Status() datatype.CollectorStatus {
	return c.col.GetCollectorStatus()
}

// NewComponent creates a new Component for this module and returns any errors on failure.
func NewComponent(reqs Requires) (Provides, error) {

	timeoutSeconds := reqs.Config.GetInt("otelcollector.extension_timeout")
	if timeoutSeconds == 0 {
		timeoutSeconds = defaultExtensionTimeout
	}
	client := apiutil.GetClientWithTimeout(time.Duration(timeoutSeconds) * time.Second)

	collector := &collectorImpl{
		authToken:      reqs.Authtoken,
		config:         reqs.Config,
		log:            reqs.Log,
		serializer:     reqs.Serializer,
		logsAgent:      reqs.LogsAgent,
		inventoryAgent: reqs.InventoryAgent,
		tagger:         reqs.Tagger,
		client:         client,
		ctx:            context.Background(),
	}

	reqs.Lc.Append(compdef.Hook{
		OnStart: collector.start,
		OnStop:  collector.stop,
	})
	timeoutCallback := func(flaretypes.FlareBuilder) time.Duration {
		return time.Second * time.Duration(reqs.Config.GetInt("otelcollector.flare.timeout"))
	}
	return Provides{
		Comp:           collector,
		FlareProvider:  flaretypes.NewProviderWithTimeout(collector.fillFlare, timeoutCallback),
		StatusProvider: status.NewInformationProvider(collector),
	}, nil
}
