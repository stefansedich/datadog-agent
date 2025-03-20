// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package remoteimpl implements a remote Tagger.
package remoteimpl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/google/uuid"
	"github.com/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/grpclog"
	"google.golang.org/grpc/metadata"

	api "github.com/DataDog/datadog-agent/comp/api/api/def"

	"github.com/DataDog/datadog-agent/comp/core/config"
	log "github.com/DataDog/datadog-agent/comp/core/log/def"
	tagger "github.com/DataDog/datadog-agent/comp/core/tagger/def"
	"github.com/DataDog/datadog-agent/comp/core/tagger/origindetection"
	"github.com/DataDog/datadog-agent/comp/core/tagger/telemetry"
	"github.com/DataDog/datadog-agent/comp/core/tagger/types"
	"github.com/DataDog/datadog-agent/comp/core/tagger/utils"
	coretelemetry "github.com/DataDog/datadog-agent/comp/core/telemetry"
	compdef "github.com/DataDog/datadog-agent/comp/def"
	pb "github.com/DataDog/datadog-agent/pkg/proto/pbgo/core"
	taggertypes "github.com/DataDog/datadog-agent/pkg/tagger/types"
	"github.com/DataDog/datadog-agent/pkg/tagset"
	"github.com/DataDog/datadog-agent/pkg/util/cache"
	"github.com/DataDog/datadog-agent/pkg/util/common"
	grpcutil "github.com/DataDog/datadog-agent/pkg/util/grpc"
	httputils "github.com/DataDog/datadog-agent/pkg/util/http"
)

const (
	noTimeout         = 0 * time.Minute
	streamRecvTimeout = 10 * time.Minute
	cacheExpiration   = 1 * time.Minute
)

var (
	errTaggerStreamNotStarted = errors.New("tagger stream not started")
	errFetchAuthToken         = errors.New("failed to fetch auth token")
)

// Requires defines the dependencies for the remote tagger.
type Requires struct {
	compdef.In

	Lc        compdef.Lifecycle
	Config    config.Component
	Log       log.Component
	Params    tagger.RemoteParams
	Telemetry coretelemetry.Component
}

// Provides contains the fields provided by the remote tagger constructor.
type Provides struct {
	compdef.Out

	Comp     tagger.Component
	Endpoint api.AgentEndpointProvider
}

type remoteTagger struct {
	store   *tagStore
	ready   bool
	options Options

	cfg config.Component
	log log.Component

	conn   *grpc.ClientConn
	token  string
	client pb.AgentSecureClient
	stream pb.AgentSecure_TaggerStreamEntitiesClient

	streamCtx    context.Context
	streamCancel context.CancelFunc
	filter       *types.Filter

	ctx    context.Context
	cancel context.CancelFunc

	telemetryTicker *time.Ticker
	telemetryStore  *telemetry.Store

	checksCardinality    types.TagCardinality
	dogstatsdCardinality types.TagCardinality
}

// Options contains the options needed to configure the remote tagger.
type Options struct {
	Target       string
	TokenFetcher func() (string, error)
	Disabled     bool
}

// NewComponent returns a remote tagger
func NewComponent(req Requires) (Provides, error) {
	remoteTaggerInstance, err := newRemoteTagger(req.Params, req.Config, req.Log, req.Telemetry)

	if err != nil {
		return Provides{}, err
	}

	// Creates the connection to the remote tagger and starts watching for events.
	req.Lc.Append(compdef.Hook{OnStart: func(_ context.Context) error {
		return start(remoteTaggerInstance)
	}})
	req.Lc.Append(compdef.Hook{OnStop: func(context.Context) error {
		return stop(remoteTaggerInstance)
	}})

	return Provides{
		Comp:     remoteTaggerInstance,
		Endpoint: api.NewAgentEndpointProvider(remoteTaggerInstance.writeList, "/tagger-list", "GET"),
	}, nil
}

func newRemoteTagger(params tagger.RemoteParams, cfg config.Component, log log.Component, telemetryComp coretelemetry.Component) (*remoteTagger, error) {
	telemetryStore := telemetry.NewStore(telemetryComp)

	target, err := params.RemoteTarget(cfg)
	if err != nil {
		return nil, err
	}

	remotetagger := &remoteTagger{
		options: Options{
			Target:       target,
			TokenFetcher: params.RemoteTokenFetcher(cfg),
		},
		cfg:            cfg,
		store:          newTagStore(telemetryStore),
		telemetryStore: telemetryStore,
		filter:         params.RemoteFilter,
		log:            log,
	}

	checkCard := cfg.GetString("checks_tag_cardinality")
	dsdCard := cfg.GetString("dogstatsd_tag_cardinality")
	remotetagger.checksCardinality, err = types.StringToTagCardinality(checkCard)
	if err != nil {
		log.Warnf("failed to parse check tag cardinality, defaulting to low. Error: %s", err)
		remotetagger.checksCardinality = types.LowCardinality
	}

	remotetagger.dogstatsdCardinality, err = types.StringToTagCardinality(dsdCard)
	if err != nil {
		log.Warnf("failed to parse dogstatsd tag cardinality, defaulting to low. Error: %s", err)
		remotetagger.dogstatsdCardinality = types.LowCardinality
	}

	return remotetagger, nil
}

func start(remoteTagger *remoteTagger) error {
	remoteTagger.telemetryTicker = time.NewTicker(1 * time.Minute)

	// Main context passed to components, consistent with the one used in the WorkloadMeta component.
	mainCtx, _ := common.GetMainCtxCancel()
	remoteTagger.ctx, remoteTagger.cancel = context.WithCancel(mainCtx)

	// NOTE: we're using InsecureSkipVerify because the gRPC server only
	// persists its TLS certs in memory, and we currently have no
	// infrastructure to make them available to clients. This is NOT
	// equivalent to grpc.WithInsecure(), since that assumes a non-TLS
	// connection.
	creds := credentials.NewTLS(&tls.Config{
		InsecureSkipVerify: true,
	})

	var onStartErr error
	remoteTagger.conn, onStartErr = grpc.DialContext( //nolint:staticcheck // TODO (ASC) fix grpc.DialContext is deprecated
		remoteTagger.ctx,
		remoteTagger.options.Target,
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(_ context.Context, url string) (net.Conn, error) {
			return net.Dial("tcp", url)
		}),
	)
	if onStartErr != nil {
		return onStartErr
	}

	// Fetch the auth token
	remoteTagger.log.Debug("fetching auth token")
	retries := 0
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 50 * time.Millisecond
	expBackoff.MaxInterval = 500 * time.Millisecond
	expBackoff.MaxElapsedTime = 30 * time.Second
	onStartErr = backoff.Retry(func() error {
		select {
		case <-remoteTagger.ctx.Done():
			return &backoff.PermanentError{Err: errFetchAuthToken}
		default:
		}

		remoteTagger.token, onStartErr = remoteTagger.options.TokenFetcher()
		if onStartErr != nil {
			retries++
			remoteTagger.log.Warnf("unable to fetch auth token, will possibly retry: %s", onStartErr)
			return onStartErr
		}
		return nil
	}, expBackoff)
	if onStartErr != nil {
		remoteTagger.log.Errorf("unable to fetch auth token after %d retries: %s", retries, onStartErr)
		return onStartErr
	}
	remoteTagger.log.Debugf("auth token fetched after %d retries", retries)

	// Initialize the gRPC client.
	remoteTagger.client = pb.NewAgentSecureClient(remoteTagger.conn)

	remoteTagger.log.Info("remote tagger initialized successfully")

	// Start the tagger stream.
	go remoteTagger.run()
	return nil
}

func stop(remoteTagger *remoteTagger) error {
	remoteTagger.cancel()

	onStopErr := remoteTagger.conn.Close()
	if onStopErr != nil {
		return onStopErr
	}

	remoteTagger.telemetryTicker.Stop()

	remoteTagger.log.Info("remote tagger stopped successfully")

	return nil
}

// GetTaggerTelemetryStore returns tagger telemetry store
func (t *remoteTagger) GetTaggerTelemetryStore() *telemetry.Store {
	return t.telemetryStore
}

// Tag returns tags for a given entity at the desired cardinality.
func (t *remoteTagger) Tag(entityID types.EntityID, cardinality types.TagCardinality) ([]string, error) {
	entity := t.store.getEntity(entityID)
	if entity != nil {
		t.telemetryStore.QueriesByCardinality(cardinality).Success.Inc()
		return entity.GetTags(cardinality), nil
	}

	t.telemetryStore.QueriesByCardinality(cardinality).EmptyTags.Inc()

	return []string{}, nil
}

// LegacyTag has the same behaviour as the Tag method, but it receives the entity id as a string and parses it.
// If possible, avoid using this function, and use the Tag method instead.
// This function exists in order not to break backward compatibility with rtloader and python
// integrations using the tagger
func (t *remoteTagger) LegacyTag(entity string, cardinality types.TagCardinality) ([]string, error) {
	prefix, id, err := types.ExtractPrefixAndID(entity)
	if err != nil {
		return nil, err
	}

	entityID := types.NewEntityID(prefix, id)
	return t.Tag(entityID, cardinality)
}

// GenerateContainerIDFromOriginInfo returns a container ID for the given Origin Info.
func (t *remoteTagger) GenerateContainerIDFromOriginInfo(originInfo origindetection.OriginInfo) (string, error) {
	fail := true
	defer func() {
		if fail {
			t.telemetryStore.OriginInfoRequests.Inc("failed")
		} else {
			t.telemetryStore.OriginInfoRequests.Inc("success")
		}
	}()

	key := cache.BuildAgentKey(
		"remoteTagger",
		"originInfo",
		origindetection.OriginInfoString(originInfo),
	)

	cachedContainerID, err := cache.GetWithExpiration(key, func() (containerID string, err error) {
		containerID, err = t.queryContainerIDFromOriginInfo(originInfo)
		return containerID, err
	}, cacheExpiration)

	if err != nil {
		return "", err
	}
	fail = false
	return cachedContainerID, nil
}

// queryContainerIDFromOriginInfo calls the local tagger to get the container ID from the Origin Info.
func (t *remoteTagger) queryContainerIDFromOriginInfo(originInfo origindetection.OriginInfo) (string, error) {
	// Check the auth token
	if t.token == "" {
		return "", errors.New("RemoteTagger initialization failed: auth token is unset")
	}

	// Create the context with the auth token
	queryCtx, queryCancel := context.WithTimeout(
		metadata.NewOutgoingContext(t.ctx, metadata.MD{
			"authorization": []string{fmt.Sprintf("Bearer %s", t.token)},
		}),
		1*time.Second,
	)
	defer queryCancel()

	// Call the gRPC method to get the container ID from the OriginInfo.
	containerIDResponse, err := t.client.TaggerGenerateContainerIDFromOriginInfo(queryCtx, &pb.GenerateContainerIDFromOriginInfoRequest{
		LocalData: &pb.GenerateContainerIDFromOriginInfoRequest_LocalData{
			ProcessID:   &originInfo.LocalData.ProcessID,
			ContainerID: &originInfo.LocalData.ContainerID,
			Inode:       &originInfo.LocalData.Inode,
			PodUID:      &originInfo.LocalData.PodUID,
		},
		ExternalData: &pb.GenerateContainerIDFromOriginInfoRequest_ExternalData{
			Init:          &originInfo.ExternalData.Init,
			ContainerName: &originInfo.ExternalData.ContainerName,
			PodUID:        &originInfo.ExternalData.PodUID,
		},
	})
	if err != nil {
		t.log.Debugf("unable to generate container ID from origin info: %s", err)
		return "", err
	}

	if containerIDResponse == nil {
		t.log.Debugf("unable to generate container ID from origin info: %s", err)
		return "", errors.New("containerIDResponse is nil")
	}
	containerID := containerIDResponse.ContainerID

	t.log.Debugf("Container ID generated successfully from origin info %+v: %s", originInfo, containerID)

	return containerID, err
}

// AccumulateTagsFor returns tags for a given entity at the desired cardinality.
func (t *remoteTagger) AccumulateTagsFor(entityID types.EntityID, cardinality types.TagCardinality, tb tagset.TagsAccumulator) error {
	tags, err := t.Tag(entityID, cardinality)
	if err != nil {
		return err
	}
	tb.Append(tags...)
	return nil
}

// Standard returns the standard tags for a given entity.
func (t *remoteTagger) Standard(entityID types.EntityID) ([]string, error) {
	entity := t.store.getEntity(entityID)
	if entity == nil {
		return []string{}, nil
	}

	return entity.StandardTags, nil
}

// GetEntity returns the entity corresponding to the specified id and an error
func (t *remoteTagger) GetEntity(entityID types.EntityID) (*types.Entity, error) {
	entity := t.store.getEntity(entityID)
	if entity == nil {
		return nil, fmt.Errorf("Entity not found for entityID")
	}

	return entity, nil
}

// List returns all the entities currently stored by the tagger.
func (t *remoteTagger) List() types.TaggerListResponse {
	entities := t.store.listEntities()
	resp := types.TaggerListResponse{
		Entities: make(map[string]types.TaggerListEntity),
	}

	for _, e := range entities {
		resp.Entities[e.ID.String()] = types.TaggerListEntity{
			Tags: map[string][]string{
				remoteSource: e.GetTags(types.HighCardinality),
			},
		}
	}

	return resp
}

// GetEntityHash returns the hash for the tags associated with the given entity
// Returns an empty string if the tags lookup fails
func (t *remoteTagger) GetEntityHash(entityID types.EntityID, cardinality types.TagCardinality) string {
	tags, err := t.Tag(entityID, cardinality)
	if err != nil {
		return ""
	}
	return utils.ComputeTagsHash(tags)
}

// AgentTags is a no-op in the remote tagger.
// Agents using the remote tagger are not supposed to rely on this function,
// because to get the container ID where the agent is running we'd need to
// introduce some dependencies that we don't want to have in the remote
// tagger.
// The only user of this function that uses the remote tagger is the cluster
// check runner, but it gets its tags from the cluster-agent which doesn't
// store tags for containers. So this function is a no-op.
func (t *remoteTagger) AgentTags(_ types.TagCardinality) ([]string, error) {
	return nil, nil
}

func (t *remoteTagger) GlobalTags(cardinality types.TagCardinality) ([]string, error) {
	return t.Tag(types.GetGlobalEntityID(), cardinality)
}

// EnrichTags enriches the tags with the global tags.
// Agents running the remote tagger don't have the ability to enrich tags based
// on the origin info. Only the core agent or dogstatsd can have origin info,
// and they always use the local tagger.
// This function can only add the global tags.
func (t *remoteTagger) EnrichTags(tb tagset.TagsAccumulator, _ taggertypes.OriginInfo) {
	if err := t.AccumulateTagsFor(types.GetGlobalEntityID(), t.dogstatsdCardinality, tb); err != nil {
		t.log.Error(err.Error())
	}
}

func (t *remoteTagger) ChecksCardinality() types.TagCardinality {
	return t.checksCardinality
}

// Subscribe currently returns a non-nil error indicating that the method is not supported
// for remote tagger. Currently, there are no use cases for client subscribing to remote tagger events
func (t *remoteTagger) Subscribe(string, *types.Filter) (types.Subscription, error) {
	return nil, errors.New("subscription to the remote tagger is not currently supported")
}

func (t *remoteTagger) run() {
	for {
		select {
		case <-t.telemetryTicker.C:
			t.store.collectTelemetry()
			continue
		case <-t.ctx.Done():
			return
		default:
		}

		if t.stream == nil {
			if err := t.startTaggerStream(noTimeout); err != nil {
				t.log.Warnf("error received trying to start stream with target %q: %s", t.options.Target, err)
				continue
			}
		}

		var response *pb.StreamTagsResponse
		err := grpcutil.DoWithTimeout(func() error {
			var err error
			response, err = t.stream.Recv()
			return err
		}, streamRecvTimeout)
		if err != nil {
			t.streamCancel()

			t.telemetryStore.ClientStreamErrors.Inc()

			// when Recv() returns an error, the stream is aborted
			// and the contents of our store are considered out of
			// sync and therefore no longer valid, so the tagger
			// can no longer be considered ready, and the stream
			// must be re-established.
			t.ready = false
			t.stream = nil

			t.log.Warnf("error received from remote tagger: %s", err)

			continue
		}

		t.log.Info("tagger stream successfully initialized")

		t.telemetryStore.Receives.Inc()

		err = t.processResponse(response)
		if err != nil {
			t.log.Warnf("error processing event received from remote tagger: %s", err)
			continue
		}
	}
}

func (t *remoteTagger) processResponse(response *pb.StreamTagsResponse) error {
	// returning early when there are no events prevents a keep-alive sent
	// from the core agent from wiping the store clean in case the remote
	// tagger was previously in an unready (but filled) state.
	if len(response.Events) == 0 {
		return nil
	}

	events := make([]types.EntityEvent, 0, len(response.Events))
	for _, ev := range response.Events {
		eventType, err := convertEventType(ev.Type)
		if err != nil {
			t.log.Warnf("error processing event received from remote tagger: %s", err)
			continue
		}

		entity := ev.Entity
		events = append(events, types.EntityEvent{
			EventType: eventType,
			Entity: types.Entity{
				ID:                          types.NewEntityID(types.EntityIDPrefix(entity.Id.Prefix), entity.Id.Uid),
				HighCardinalityTags:         entity.HighCardinalityTags,
				OrchestratorCardinalityTags: entity.OrchestratorCardinalityTags,
				LowCardinalityTags:          entity.LowCardinalityTags,
				StandardTags:                entity.StandardTags,
			},
		})
	}

	// if the tagger was not ready by this point, it means an error
	// occurred and the contents of the store are no longer valid and need
	// to be replaced by the batch coming from the current response
	replaceStoreContents := !t.ready

	err := t.store.processEvents(events, replaceStoreContents)
	if err != nil {
		return err
	}

	t.ready = true

	return nil
}

// startTaggerStream tries to establish a stream with the remote gRPC endpoint.
// Since the entire remote tagger really depends on this working, it'll keep on
// retrying with an exponential backoff until maxElapsed (or forever if
// maxElapsed == 0) or the tagger is stopped.
func (t *remoteTagger) startTaggerStream(maxElapsed time.Duration) error {
	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 500 * time.Millisecond
	expBackoff.MaxInterval = 5 * time.Minute
	expBackoff.MaxElapsedTime = maxElapsed

	return backoff.Retry(func() error {
		select {
		case <-t.ctx.Done():
			return &backoff.PermanentError{Err: errTaggerStreamNotStarted}
		default:
		}

		var err error

		// Check the auth token
		if t.token == "" {
			return errors.New("RemoteTagger initialization failed: auth token is unset")
		}

		t.streamCtx, t.streamCancel = context.WithCancel(
			metadata.NewOutgoingContext(t.ctx, metadata.MD{
				"authorization": []string{fmt.Sprintf("Bearer %s", t.token)},
			}),
		)

		prefixes := make([]string, 0)
		for prefix := range t.filter.GetPrefixes() {
			prefixes = append(prefixes, string(prefix))
		}

		t.stream, err = t.client.TaggerStreamEntities(t.streamCtx, &pb.StreamTagsRequest{
			Cardinality: pb.TagCardinality(t.filter.GetCardinality()),
			StreamingID: uuid.New().String(),
			Prefixes:    prefixes,
		})
		if err != nil {
			t.log.Debug("unable to establish stream, will possibly retry: %s", err)
			return err
		}

		return nil
	}, expBackoff)
}

func (t *remoteTagger) writeList(w http.ResponseWriter, _ *http.Request) {
	response := t.List()

	jsonTags, err := json.Marshal(response)
	if err != nil {
		httputils.SetJSONError(w, t.log.Errorf("Unable to marshal tagger list response: %s", err), 500)
		return
	}
	w.Write(jsonTags)
}

func convertEventType(t pb.EventType) (types.EventType, error) {
	switch t {
	case pb.EventType_ADDED:
		return types.EventTypeAdded, nil
	case pb.EventType_MODIFIED:
		return types.EventTypeModified, nil
	case pb.EventType_DELETED:
		return types.EventTypeDeleted, nil
	}

	return types.EventTypeAdded, fmt.Errorf("unknown event type: %q", t)
}

// TODO(components): verify the grpclog is initialized elsewhere and cleanup
func init() {
	grpclog.SetLoggerV2(grpcutil.NewLogger())
}
