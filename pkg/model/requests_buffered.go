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
	"math/rand"
	"time"

	"knative-simulator/pkg/simulator"
)

type RequestsBufferedStock interface {
	simulator.ThroughStock
}

type requestsBufferedStock struct {
	env            simulator.Environment
	delegate       simulator.ThroughStock
	replicas       ReplicasActiveStock
	requestsFailed simulator.SinkStock
}

func (rbs *requestsBufferedStock) Name() simulator.StockName {
	return rbs.delegate.Name()
}

func (rbs *requestsBufferedStock) KindStocked() simulator.EntityKind {
	return rbs.delegate.KindStocked()
}

func (rbs *requestsBufferedStock) Count() uint64 {
	return rbs.delegate.Count()
}

func (rbs *requestsBufferedStock) EntitiesInStock() []simulator.Entity {
	return rbs.delegate.EntitiesInStock()
}

func (rbs *requestsBufferedStock) Remove() simulator.Entity {
	return rbs.delegate.Remove()
}

func (rbs *requestsBufferedStock) Add(entity simulator.Entity) error {
	addResult := rbs.delegate.Add(entity)

	if rbs.replicas.Count() > 0 {
		e := rbs.replicas.EntitiesInStock()[0]
		replica := e.(ReplicaEntity)

		rbs.env.AddToSchedule(simulator.NewMovement(
			"buffer -> replica",
			rbs.env.CurrentMovementTime().Add(1*time.Nanosecond),
			rbs.delegate,
			replica.RequestsProcessing(),
		))
	} else {
		for _, e := range rbs.delegate.EntitiesInStock() {
			rnd := rand.Intn(999999)

			request := e.(RequestEntity)
			backoff, outOfAttempts := request.NextBackoff()

			if outOfAttempts {
				rbs.env.AddToSchedule(simulator.NewMovement(
					"exhausted_attempts",
					rbs.env.CurrentMovementTime().Add(backoff).Add(time.Duration(rnd)),
					rbs.delegate,
					rbs.requestsFailed,
				))
			} else {
				rbs.env.AddToSchedule(simulator.NewMovement(
					"buffer_backoff",
					rbs.env.CurrentMovementTime().Add(backoff).Add(time.Duration(rnd)),
					rbs.delegate,
					rbs.delegate,
				))
			}
		}
	}

	return addResult
}

func NewRequestsBufferedStock(env simulator.Environment, replicas ReplicasActiveStock, requestsFailed simulator.SinkStock) RequestsBufferedStock {
	return &requestsBufferedStock{
		env:            env,
		delegate:       simulator.NewThroughStock("RequestsBuffered", "Request"),
		replicas:       replicas,
		requestsFailed: requestsFailed,
	}
}
