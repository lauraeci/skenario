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
 *
 */

package serve

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/sclevine/spec"
	"github.com/sclevine/spec/report"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunHandler(t *testing.T) {
	spec.Run(t, "RunHandler", testRunHandler, spec.Report(report.Terminal{}))
}

func testRunHandler(t *testing.T, describe spec.G, it spec.S) {
	var req *http.Request
	var recorder *httptest.ResponseRecorder
	var err error
	var mux *http.ServeMux
	var skenarioRunRequest *SkenarioRunRequest

	describe("RunHandler", func() {
		describe("common behaviour", func() {
			it.Before(func() {
				skenarioRunRequest = &SkenarioRunRequest{
					RunFor:           20 * time.Second,
					TrafficPattern:   "golang_rand_uniform",
					InMemoryDatabase: true,
				}
				var reqBody = new(bytes.Buffer)
				err = json.NewEncoder(reqBody).Encode(skenarioRunRequest)
				assert.NoError(t, err)

				req, err = http.NewRequest("POST", "/run", reqBody)
				assert.NoError(t, err)

				mux = http.NewServeMux()
				mux.HandleFunc("/run", RunHandler)

				recorder = httptest.NewRecorder()
				mux.ServeHTTP(recorder, req)
			})

			describe("headers", func() {
				it("has status 200 OK", func() {
					assert.Equal(t, http.StatusOK, recorder.Code)
				})

				it("sets the content-type to JSON", func() {
					assert.Equal(t, "application/json", recorder.Header().Get("Content-Type"))
				})
			})

			describe("response", func() {
				var skenarioResponse *SkenarioRunResponse

				it.Before(func() {
					skenarioResponse = &SkenarioRunResponse{}
					err := json.NewDecoder(recorder.Result().Body).Decode(skenarioResponse)
					assert.NoError(t, err)
				})

				it("gives the ran-for time", func() {
					assert.Equal(t, skenarioResponse.RanFor, 20*time.Second)
				})

				it("only runs for the expected amount of time", func() {
					maxTime := skenarioResponse.TotalLines[len(skenarioResponse.TotalLines)-1].OccursAt
					assert.InDelta(t, int64(20*time.Second), maxTime, float64(time.Second))
				})

				it("contains total_line entries", func() {
					assert.NotEmpty(t, skenarioResponse.TotalLines)
				})

				it("contains response_time entries", func() {
					assert.NotEmpty(t, skenarioResponse.ResponseTimes)
				})
			})
		})

		describe("configuring traffic patterns", func() {
			var skenarioResponse *SkenarioRunResponse

			patterns := []string{"goland_rand_uniform", "step", "ramp", "sinusoidal"}

			for _, p := range patterns {
				describe(fmt.Sprintf("with '%s' pattern", p), func() {
					it.Before(func() {
						skenarioResponse = trafficPatternBefore(t, p)
					})

					it(fmt.Sprintf("gives its kind as '%s'", p), func() {
						assert.Equal(t, skenarioResponse.TrafficPattern, p)
					})
				})
			}
		})
	})
}

func trafficPatternBefore(t *testing.T, pattern string) *SkenarioRunResponse {
	skenarioRunRequest := &SkenarioRunRequest{
		RunFor:           20 * time.Second,
		TrafficPattern:   pattern,
		InMemoryDatabase: true,
	}
	var reqBody = new(bytes.Buffer)
	err := json.NewEncoder(reqBody).Encode(skenarioRunRequest)
	assert.NoError(t, err)

	req, err := http.NewRequest("POST", "/run", reqBody)
	assert.NoError(t, err)

	mux := http.NewServeMux()
	mux.HandleFunc("/run", RunHandler)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, req)

	skenarioResponse := &SkenarioRunResponse{}
	err = json.NewDecoder(recorder.Result().Body).Decode(skenarioResponse)
	assert.NoError(t, err)

	return skenarioResponse
}
