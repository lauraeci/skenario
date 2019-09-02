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

package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/bvinc/go-sqlite-lite/sqlite3"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"knative.dev/pkg/logging"
	"knative.dev/serving/pkg/autoscaler"

	"skenario/pkg/data"
	"skenario/pkg/model"
	"skenario/pkg/model/trafficpatterns"
	"skenario/pkg/simulator"
)

var startAt = time.Unix(0, 0)

type TallyLine struct {
	OccursAt    int64  `json:"occurs_at"`
	StockName   string `json:"stock_name"`
	KindStocked string `json:"kind_stocked"`
	Tally       int64  `json:"tally"`
}

type ResponseTime struct {
	ArrivedAt    int64 `json:"arrived_at"`
	CompletedAt  int64 `json:"completed_at"`
	ResponseTime int64 `json:"response_time"`
}

type RPS struct {
	Second   int64 `json:"second"`
	Requests int64 `json:"requests"`
}

type SkenarioRunResponse struct {
	RanFor            time.Duration  `json:"ran_for"`
	TrafficPattern    string         `json:"traffic_pattern"`
	TallyLines        []TallyLine    `json:"tally_lines"`
	ResponseTimes     []ResponseTime `json:"response_times"`
	RequestsPerSecond []RPS          `json:"requests_per_second"`
}

type SkenarioRunRequest struct {
	RunFor           time.Duration `json:"run_for"`
	TrafficPattern   string        `json:"traffic_pattern"`
	InMemoryDatabase bool          `json:"in_memory_database,omitempty"`

	LaunchDelay              time.Duration `json:"launch_delay"`
	TerminateDelay           time.Duration `json:"terminate_delay"`
	TickInterval             time.Duration `json:"tick_interval"`
	StableWindow             time.Duration `json:"stable_window"`
	PanicWindowPercentage    float64       `json:"panic_window_percentage"`
	PanicThresholdPercentage float64       `json:"panic_threshold_percentage"`
	ScaleToZeroGracePeriod   time.Duration `json:"scale_to_zero_grace_period"`
	TargetConcurrency        float64       `json:"target_concurrency"`
	TotalConcurrency         float64       `json:"total_concurrency"`
	MaxScaleUpRate           float64       `json:"max_scale_up_rate"`

	UniformConfig    trafficpatterns.UniformConfig    `json:"uniform_config,omitempty"`
	RampConfig       trafficpatterns.RampConfig       `json:"ramp_config,omitempty"`
	StepConfig       trafficpatterns.StepConfig       `json:"step_config,omitempty"`
	SinusoidalConfig trafficpatterns.SinusoidalConfig `json:"sinusoidal_config,omitempty"`
}

func RunHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	runReq := &SkenarioRunRequest{}
	err := json.NewDecoder(r.Body).Decode(runReq)
	if err != nil {
		panic(err.Error())
	}

	logger := newLogger(os.Stdout)
	ctx := logging.WithLogger(r.Context(), logger)
	env := simulator.NewEnvironment(ctx, startAt, runReq.RunFor)

	clusterConf := buildClusterConfig(runReq)
	kpaConf := buildKpaConfig(runReq)
	replicasConfig := model.ReplicasConfig{
		LaunchDelay:    runReq.LaunchDelay,
		TerminateDelay: runReq.TerminateDelay,
		MaxRPS:         int64(runReq.TotalConcurrency),
	}

	cluster := model.NewCluster(env, clusterConf, replicasConfig)
	model.NewKnativeAutoscaler(env, startAt, cluster, kpaConf)
	trafficSource := model.NewTrafficSource(env, cluster.BufferStock())

	var traffic trafficpatterns.Pattern
	switch runReq.TrafficPattern {
	case "golang_rand_uniform":
		traffic = trafficpatterns.NewUniformRandom(env, trafficSource, cluster.BufferStock(), runReq.UniformConfig)
	case "step":
		traffic = trafficpatterns.NewStep(env, trafficSource, cluster.BufferStock(), runReq.StepConfig)
	case "ramp":
		traffic = trafficpatterns.NewRamp(env, trafficSource, cluster.BufferStock(), runReq.RampConfig)
	case "sinusoidal":
		traffic = trafficpatterns.NewSinusoidal(env, trafficSource, cluster.BufferStock(), runReq.SinusoidalConfig)
	}

	traffic.Generate()

	completed, ignored, err := env.Run()
	if err != nil {
		panic(err.Error())
	}

	var dbFileName string
	if runReq.InMemoryDatabase {
		dbFileName = "file::memory:?cache=shared"
	} else {
		dbFileName = "skenario.db"
	}

	conn, err := sqlite3.Open(dbFileName)
	if err != nil {
		panic(fmt.Errorf("could not open database file '%s': %s", dbFileName, err.Error()))
	}
	defer conn.Close()

	store := data.NewRunStore(conn)
	scenarioRunId, err := store.Store(completed, ignored, clusterConf, kpaConf, "skenario_web", traffic.Name(), runReq.RunFor)
	if err != nil {
		fmt.Printf("there was an error saving data: %s", err.Error())
	}

	var vds = SkenarioRunResponse{
		RanFor:            env.HaltTime().Sub(startAt),
		TrafficPattern:    traffic.Name(),
		TallyLines:        tallyLines(dbFileName, scenarioRunId),
		ResponseTimes:     responseTimes(dbFileName, scenarioRunId),
		RequestsPerSecond: requestsPerSecond(dbFileName, scenarioRunId),
	}

	err = json.NewEncoder(w).Encode(vds)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
}

func tallyLines(dbFileName string, scenarioRunId int64) []TallyLine {
	totalConn, err := sqlite3.Open(dbFileName, sqlite3.OPEN_READONLY)
	if err != nil {
		panic(fmt.Errorf("could not open database file '%s': %s", dbFileName, err.Error()))
	}
	defer totalConn.Close()

	totalStmt, err := totalConn.Prepare(data.RunningTallyQuery, scenarioRunId, scenarioRunId)
	if err != nil {
		panic(fmt.Errorf("could not prepare query: %s", err.Error()))
	}

	var occursAt, tally int64
	var stockName, kindStocked string
	tallyLines := make([]TallyLine, 0)
	for {
		hasRow, err := totalStmt.Step()
		if err != nil {
			panic(fmt.Errorf("could not step: %s", err.Error()))
		}

		if !hasRow {
			break
		}

		err = totalStmt.Scan(&occursAt, &stockName, &kindStocked, &tally)
		if err != nil {
			panic(fmt.Errorf("could not scan: %s", err.Error()))
		}

		line := TallyLine{
			OccursAt:    occursAt,
			StockName:   stockName,
			KindStocked: kindStocked,
			Tally:       tally,
		}
		tallyLines = append(tallyLines, line)
	}

	return tallyLines
}

func responseTimes(dbFileName string, scenarioRunId int64) []ResponseTime {
	responseConn, err := sqlite3.Open(dbFileName, sqlite3.OPEN_READONLY)
	if err != nil {
		panic(fmt.Errorf("could not open database file '%s': %s", dbFileName, err.Error()))
	}
	defer responseConn.Close()

	responseStmt, err := responseConn.Prepare(data.ResponseTimesQuery, scenarioRunId)
	if err != nil {
		panic(fmt.Errorf("could not prepare query: %s", err.Error()))
	}

	var arrivedAt, completedAt, rTime int64
	responseTimes := make([]ResponseTime, 0)
	for {
		hasRow, err := responseStmt.Step()
		if err != nil {
			panic(fmt.Errorf("could not step: %s", err.Error()))
		}

		if !hasRow {
			break
		}

		err = responseStmt.Scan(&arrivedAt, &completedAt, &rTime)
		if err != nil {
			panic(fmt.Errorf("could not scan: %s", err.Error()))
		}

		var rt = ResponseTime{
			ArrivedAt:    arrivedAt,
			CompletedAt:  completedAt,
			ResponseTime: rTime,
		}
		responseTimes = append(responseTimes, rt)
	}

	return responseTimes
}

func requestsPerSecond(dbFileName string, scenarioRunId int64) []RPS {
	rpsConn, err := sqlite3.Open(dbFileName, sqlite3.OPEN_READONLY)
	if err != nil {
		panic(fmt.Errorf("could not open database file '%s': %s", dbFileName, err.Error()))
	}
	defer rpsConn.Close()

	requestsPerSecondStmt, err := rpsConn.Prepare(data.RequestsPerSecondQuery, scenarioRunId)
	if err != nil {
		panic(fmt.Errorf("could not prepare query: %s", err.Error()))
	}

	var second, requests int64
	requestsPerSecond := make([]RPS, 0)
	for {
		hasRow, err := requestsPerSecondStmt.Step()
		if err != nil {
			panic(fmt.Errorf("could not step: %s", err.Error()))
		}

		if !hasRow {
			break
		}

		err = requestsPerSecondStmt.Scan(&second, &requests)
		if err != nil {
			panic(fmt.Errorf("could not scan: %s", err.Error()))
		}

		var rps = RPS{
			Second:   second,
			Requests: requests,
		}
		requestsPerSecond = append(requestsPerSecond, rps)
	}

	return requestsPerSecond
}

func buildClusterConfig(srr *SkenarioRunRequest) model.ClusterConfig {
	return model.ClusterConfig{
		LaunchDelay:      srr.LaunchDelay,
		TerminateDelay:   srr.TerminateDelay,
		NumberOfRequests: uint(srr.UniformConfig.NumberOfRequests),
		KnativeAutoscalerSpecific: model.KnativeAutoscalerSpecific{
			ScaleToZeroGracePeriod: srr.ScaleToZeroGracePeriod,
			PanicWindowPercentage:  srr.PanicWindowPercentage,
		},
	}
}

func buildKpaConfig(srr *SkenarioRunRequest) model.KnativeAutoscalerConfig {
	panicThreshold := srr.TargetConcurrency * srr.PanicThresholdPercentage / 100.0

	return model.KnativeAutoscalerConfig{
		DeciderSpec: autoscaler.DeciderSpec{
			ScalingMetric: "concurrency",
			TickInterval:   srr.TickInterval,
			MaxScaleUpRate: srr.MaxScaleUpRate,
			TargetValue:    srr.TargetConcurrency,
			TotalValue:     srr.TotalConcurrency,
			PanicThreshold: panicThreshold,
			StableWindow:   srr.StableWindow,
		},
		KnativeAutoscalerSpecific: model.KnativeAutoscalerSpecific{
			ScaleToZeroGracePeriod: srr.ScaleToZeroGracePeriod,
			PanicWindowPercentage:  srr.PanicWindowPercentage,
		},
	}
}

func newLogger(buf io.Writer) *zap.SugaredLogger {
	sink := zapcore.AddSync(buf)

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig()),
		sink,
		zap.DebugLevel,
	)

	unsugaredLogger := zap.New(core)

	return unsugaredLogger.Named("skenario").Sugar()
}
