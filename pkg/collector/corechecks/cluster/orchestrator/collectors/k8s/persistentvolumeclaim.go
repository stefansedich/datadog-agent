// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

//go:build kubeapiserver && orchestrator

package k8s

import (
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/collectors"
	"github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/processors"
	k8sProcessors "github.com/DataDog/datadog-agent/pkg/collector/corechecks/cluster/orchestrator/processors/k8s"
	"github.com/DataDog/datadog-agent/pkg/config/utils"
	"github.com/DataDog/datadog-agent/pkg/orchestrator"
	"github.com/DataDog/datadog-agent/pkg/util/kubernetes"

	"k8s.io/apimachinery/pkg/labels"
	corev1Informers "k8s.io/client-go/informers/core/v1"
	corev1Listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

// NewPersistentVolumeClaimCollectorVersions builds the group of collector versions.
func NewPersistentVolumeClaimCollectorVersions(metadataAsTags utils.MetadataAsTags) collectors.CollectorVersions {
	return collectors.NewCollectorVersions(
		NewPersistentVolumeClaimCollector(metadataAsTags),
	)
}

// PersistentVolumeClaimCollector is a collector for Kubernetes PersistentVolumeClaims.
type PersistentVolumeClaimCollector struct {
	informer  corev1Informers.PersistentVolumeClaimInformer
	lister    corev1Listers.PersistentVolumeClaimLister
	metadata  *collectors.CollectorMetadata
	processor *processors.Processor
}

// NewPersistentVolumeClaimCollector creates a new collector for the Kubernetes
// PersistentVolumeClaim resource.
func NewPersistentVolumeClaimCollector(metadataAsTags utils.MetadataAsTags) *PersistentVolumeClaimCollector {
	resourceType := getResourceType(persistentVolumeClaimName, persistentVolumeClaimVersion)
	labelsAsTags := metadataAsTags.GetResourcesLabelsAsTags()[resourceType]
	annotationsAsTags := metadataAsTags.GetResourcesAnnotationsAsTags()[resourceType]

	return &PersistentVolumeClaimCollector{
		metadata: &collectors.CollectorMetadata{
			IsDefaultVersion:                     true,
			IsStable:                             true,
			IsMetadataProducer:                   true,
			IsManifestProducer:                   true,
			SupportsManifestBuffering:            true,
			Name:                                 persistentVolumeClaimName,
			Kind:                                 kubernetes.PersistentVolumeClaimKind,
			NodeType:                             orchestrator.K8sPersistentVolumeClaim,
			Version:                              persistentVolumeClaimVersion,
			LabelsAsTags:                         labelsAsTags,
			AnnotationsAsTags:                    annotationsAsTags,
			SupportsTerminatedResourceCollection: true,
		},
		processor: processors.NewProcessor(new(k8sProcessors.PersistentVolumeClaimHandlers)),
	}
}

// Informer returns the shared informer.
func (c *PersistentVolumeClaimCollector) Informer() cache.SharedInformer {
	return c.informer.Informer()
}

// Init is used to initialize the collector.
func (c *PersistentVolumeClaimCollector) Init(rcfg *collectors.CollectorRunConfig) {
	c.informer = rcfg.OrchestratorInformerFactory.InformerFactory.Core().V1().PersistentVolumeClaims()
	c.lister = c.informer.Lister()
}

// Metadata is used to access information about the collector.
func (c *PersistentVolumeClaimCollector) Metadata() *collectors.CollectorMetadata {
	return c.metadata
}

// Run triggers the collection process.
func (c *PersistentVolumeClaimCollector) Run(rcfg *collectors.CollectorRunConfig) (*collectors.CollectorRunResult, error) {
	list, err := c.lister.List(labels.Everything())
	if err != nil {
		return nil, collectors.NewListingError(err)
	}

	return c.Process(rcfg, list)
}

// Process is used to process the list of resources and return the result.
func (c *PersistentVolumeClaimCollector) Process(rcfg *collectors.CollectorRunConfig, list interface{}) (*collectors.CollectorRunResult, error) {
	ctx := collectors.NewK8sProcessorContext(rcfg, c.metadata)

	processResult, processed := c.processor.Process(ctx, list)

	if processed == -1 {
		return nil, collectors.ErrProcessingPanic
	}

	result := &collectors.CollectorRunResult{
		Result:             processResult,
		ResourcesListed:    len(c.processor.Handlers().ResourceList(ctx, list)),
		ResourcesProcessed: processed,
	}

	return result, nil
}
