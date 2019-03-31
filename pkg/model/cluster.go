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
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/knative/serving/pkg/autoscaler"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/informers"
	corev1informers "k8s.io/client-go/informers/core/v1"
	"k8s.io/client-go/kubernetes"
	k8sfakes "k8s.io/client-go/kubernetes/fake"

	"knative-simulator/pkg/simulator"
)

type ClusterModel interface {
	Model
	CurrentDesired() int32
	SetDesired(int32)
	CurrentLaunching() uint64
	CurrentActive() uint64
	RecordToAutoscaler(scaler autoscaler.UniScaler, atTime *time.Time, ctx context.Context)
}

type EndpointInformerSource interface {
	EPInformer() corev1informers.EndpointsInformer
}

type IPV4Sequence interface {
	Next() string
}

type clusterModel struct {
	env                simulator.Environment
	currentDesired     int32
	replicasLaunching  simulator.ThroughStock
	replicasActive     simulator.ThroughStock
	replicasTerminated simulator.SinkStock
	kubernetesClient   kubernetes.Interface
	endpointsInformer  corev1informers.EndpointsInformer
	nextIPValue        uint32
}

func (cm *clusterModel) Env() simulator.Environment {
	return cm.env
}

// TODO: can we get rid of this and the variable?
func (cm *clusterModel) CurrentDesired() int32 {
	return cm.currentDesired
}

func (cm *clusterModel) SetDesired(desired int32) {
	launching := int32(cm.replicasLaunching.Count())
	active := int32(cm.replicasActive.Count())

	desireDelta := desired - (launching + active)

	delay := 10 * time.Nanosecond
	if desireDelta > 0 {
		for ; desireDelta > 0; desireDelta-- {
			newReplica := NewReplicaEntity(cm.kubernetesClient, cm.endpointsInformer, cm.Next())
			err := cm.replicasLaunching.Add(newReplica)
			if err != nil {
				panic(fmt.Sprintf("could not scale up in ClusterModel: %s", err.Error()))
			}

			cm.env.AddToSchedule(simulator.NewMovement(
				"launching -> active",
				cm.env.CurrentMovementTime().Add(delay),
				cm.replicasLaunching,
				cm.replicasActive,
			))

			delay += 10
		}
	} else if desireDelta < 0 {
		// for now I assume launching replicas are terminated before active replicas
		desireDelta = desireDelta + launching
		for ; launching > 0; launching-- {
			cm.env.AddToSchedule(simulator.NewMovement(
				"launching -> terminated",
				cm.env.CurrentMovementTime().Add(delay),
				cm.replicasLaunching,
				cm.replicasTerminated,
			))
			delay += 10
		}

		for ; desireDelta < 0; desireDelta++ {
			cm.env.AddToSchedule(simulator.NewMovement(
				"active -> terminated",
				cm.env.CurrentMovementTime().Add(delay),
				cm.replicasActive,
				cm.replicasTerminated,
			))
			delay += 10
		}
	} else {
		// No change.
	}

	cm.currentDesired = desired
}

func (cm *clusterModel) CurrentLaunching() uint64 {
	return cm.replicasLaunching.Count()
}

func (cm *clusterModel) CurrentActive() uint64 {
	return cm.replicasActive.Count()
}

func (cm *clusterModel) RecordToAutoscaler(scaler autoscaler.UniScaler, atTime *time.Time, ctx context.Context) {
	for _, e := range cm.replicasActive.EntitiesInStock() {
		scaler.Record(ctx, autoscaler.Stat{
			Time:                      atTime,
			PodName:                   string(e.Name()),
			AverageConcurrentRequests: 1,
			RequestCount:              1,
		})
	}
}

func (cm *clusterModel) EPInformer() corev1informers.EndpointsInformer {
	return cm.endpointsInformer
}

func (cm *clusterModel) Next() string {
	ip := make(net.IP, 4)
	binary.BigEndian.PutUint32(ip, cm.nextIPValue)

	cm.nextIPValue++

	return ip.String()
}

func NewCluster(env simulator.Environment) ClusterModel {
	fakeClient := k8sfakes.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(fakeClient, 0)
	endpointsInformer := informerFactory.Core().V1().Endpoints()

	newEndpoints := &corev1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Name: "Skenario Revision",
		},
		Subsets: []corev1.EndpointSubset{{
			Addresses: []corev1.EndpointAddress{},
		}},
	}

	fakeClient.CoreV1().Endpoints("skenario").Create(newEndpoints)
	endpointsInformer.Informer().GetIndexer().Add(newEndpoints)

	return &clusterModel{
		env:                env,
		replicasLaunching:  simulator.NewThroughStock("ReplicasLaunching", simulator.EntityKind("Replica")),
		replicasActive:     NewReplicasActiveStock(),
		replicasTerminated: simulator.NewSinkStock("ReplicasTerminated", simulator.EntityKind("Replica")),
		kubernetesClient:   fakeClient,
		endpointsInformer:  endpointsInformer,
		nextIPValue:        1,
	}
}