/*
 * Copyright (C) 2019-Present Pivotal Software, Inc. All rights reserved.
 *
 * This program and the accompanying materials are made available under the terms
 * of the Apache License, Version 2.0 (the "License”); you may not use this file
 * except in compliance with the License. You may obtain a copy of the License at:
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed
 * under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR
 * CONDITIONS OF ANY KIND, either express or implied. See the License for the
 * specific language governing permissions and limitations under the License.
 */

package model

import (
	"context"
	"fmt"
	"knative.dev/serving/pkg/apis/serving"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"time"

	"knative.dev/pkg/logging"
	"knative.dev/serving/pkg/resources"
	"go.uber.org/zap"

	"skenario/pkg/simulator"

	"knative.dev/serving/pkg/autoscaler"
)

const (
	testNamespace = "simulator-namespace"
	testName      = "revisionService"
)

type KnativeAutoscalerConfig struct {
	TickInterval           time.Duration
	StableWindow           time.Duration
	PanicWindow            time.Duration
	PanicThreshold         float64
	ScaleToZeroGracePeriod time.Duration
	TargetConcurrency      float64
	MaxScaleUpRate         float64
}

type KnativeAutoscalerModel interface {
	Model
}

type knativeAutoscaler struct {
	env      simulator.Environment
	tickTock AutoscalerTicktockStock
}

func (kas *knativeAutoscaler) Env() simulator.Environment {
	return kas.env
}

func NewKnativeAutoscaler(env simulator.Environment, startAt time.Time, cluster ClusterModel, config KnativeAutoscalerConfig) KnativeAutoscalerModel {
	logger := logging.FromContext(env.Context())

	readyPodCounter := NewClusterReadyCounter(cluster.ActiveStock())
	kpa := newKpa(logger, config, readyPodCounter, cluster.Collector())

	autoscalerEntity := simulator.NewEntity("Autoscaler", "Autoscaler")

	kas := &knativeAutoscaler{
		env:      env,
		tickTock: NewAutoscalerTicktockStock(env, autoscalerEntity, kpa, cluster),
	}

	for theTime := startAt.Add(config.TickInterval).Add(1 * time.Nanosecond); theTime.Before(env.HaltTime()); theTime = theTime.Add(config.TickInterval) {
		kas.env.AddToSchedule(simulator.NewMovement(
			"autoscaler_tick",
			theTime,
			kas.tickTock,
			kas.tickTock,
		))
	}

	scraperTickTock := NewScraperTicktockStock(cluster.Collector(), NewClusterServiceScraper(cluster.ActiveStock()))
	for theTime := startAt.Add(config.TickInterval).Add(1 * time.Nanosecond); theTime.Before(env.HaltTime()); theTime = theTime.Add(config.TickInterval) {
		kas.env.AddToSchedule(simulator.NewMovement(
			"scraper_tick",
			theTime,
			scraperTickTock,
			scraperTickTock,
		))
	}

	return kas
}

func newKpa(logger *zap.SugaredLogger, kconfig KnativeAutoscalerConfig, readyCounter resources.ReadyPodCounter, collector *autoscaler.MetricCollector) *autoscaler.Autoscaler {
	deciderSpec := autoscaler.DeciderSpec{
		ServiceName:       testName,
		TickInterval:      kconfig.TickInterval,
		MaxScaleUpRate:    kconfig.MaxScaleUpRate,
		TargetConcurrency: kconfig.TargetConcurrency,
		PanicThreshold:    kconfig.PanicThreshold,
		StableWindow:      kconfig.StableWindow,
	}

	statsReporter, err := autoscaler.NewStatsReporter(testNamespace, testName, "config-1", "revision-1")
	if err != nil {
		logger.Fatalf("could not create stats reporter: %s", err.Error())
	}

	as, err := autoscaler.New(
		testNamespace,
		testName,
		collector,
		readyCounter,
		deciderSpec,
		statsReporter,
	)
	if err != nil {
		panic(err.Error())
	}

	return as
}

func NewMetricCollector(logger *zap.SugaredLogger, kconfig KnativeAutoscalerConfig, activeStock ReplicasActiveStock) *autoscaler.MetricCollector {
	scraper := NewClusterServiceScraper(activeStock)

	metric := &autoscaler.Metric{
		ObjectMeta: v1.ObjectMeta{
			Namespace: testNamespace,
			Name:      testName,
			Labels:    map[string]string{serving.RevisionLabelKey: testName},
		},
		Spec: autoscaler.MetricSpec{
			ScrapeTarget: testName,
			StableWindow: kconfig.StableWindow,
			PanicWindow:  kconfig.PanicWindow,
		},
	}

	clusterStatScraper := func(metric *autoscaler.Metric) (autoscaler.StatsScraper, error) {
		return scraper, nil
	}

	collector := autoscaler.NewMetricCollector(clusterStatScraper, logger)
	_, err := collector.Create(context.Background(), metric)
	if err != nil {
		panic(fmt.Errorf("could not create metric collector: %s", err.Error()))
	}

	return collector
}
