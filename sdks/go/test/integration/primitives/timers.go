// Licensed to the Apache Software Foundation (ASF) under one or more
// contributor license agreements.  See the NOTICE file distributed with
// this work for additional information regarding copyright ownership.
// The ASF licenses this file to You under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance with
// the License.  You may obtain a copy of the License at
//
//    http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package primitives

import (
	"context"
	"strconv"
	"time"

	"github.com/apache/beam/sdks/v2/go/pkg/beam"
	"github.com/apache/beam/sdks/v2/go/pkg/beam/core/state"
	"github.com/apache/beam/sdks/v2/go/pkg/beam/core/timers"
	"github.com/apache/beam/sdks/v2/go/pkg/beam/register"
	"github.com/apache/beam/sdks/v2/go/pkg/beam/testing/passert"
	"github.com/apache/beam/sdks/v2/go/pkg/beam/transforms/periodic"
)

// Based on https://github.com/apache/beam/blob/master/runners/flink/src/test/java/org/apache/beam/runners/flink/PortableTimersExecutionTest.java

func init() {
	register.DoFn2x0[[]byte, func(string, int)](&inputFn[string, int]{})
	register.DoFn6x0[beam.Window, state.Provider, timers.Provider, string, int, func(kv[string, int])](&eventTimeFn{})
	register.Emitter2[string, int]()
	register.Emitter1[kv[string, int]]()
}

type kv[K, V any] struct {
	Key   K
	Value V
}

func kvfn[K, V any](k K, v V) kv[K, V] {
	return kv[K, V]{k, v}
}

type inputFn[K, V any] struct {
	Inputs []kv[K, V]
}

func (fn *inputFn[K, V]) ProcessElement(_ []byte, emit func(K, V)) {
	for _, in := range fn.Inputs {
		emit(in.Key, in.Value)
	}
}

type eventTimeFn struct {
	Callback timers.EventTime
	MyKey    state.Value[string]

	Offset      int
	TimerOutput int
}

func (fn *eventTimeFn) ProcessElement(w beam.Window, sp state.Provider, tp timers.Provider, key string, value int, emit func(kv[string, int])) {
	fn.Callback.Set(tp, w.MaxTimestamp().ToTime())
	fn.MyKey.Write(sp, key)
	emit(kvfn(key, value+fn.Offset))
}

func (fn *eventTimeFn) OnTimer(ctx context.Context, ts beam.EventTime, sp state.Provider, tp timers.Provider, key string, timer timers.Context, emit func(kv[string, int])) {
	switch timer.Family {
	case fn.Callback.Family:
		switch timer.Tag {
		case "":
			read, ok, err := fn.MyKey.Read(sp)
			if err != nil {
				panic(err)
			}
			if !ok {
				panic("State must be set.")
			}
			emit(kvfn(read, fn.TimerOutput))
		default:
			panic("unexpected timer tag: " + timer.Family + " tag:" + timer.Tag + " want: \"\", for key:" + key)
		}
	default:
		if fn.Callback.Family != timer.Family || timer.Tag != "" {
			panic("unexpected timer family: " + timer.Family + " tag:" + timer.Tag + " want: " + fn.Callback.Family + ", for key:" + key)
		}
	}
}

// TimersEventTime takes in an impulse transform and produces a pipeline construction
// function that validates EventTime timers.
//
// The impulse is provided outside to swap between a bounded impulse, and
// an unbounded one, because the Go SDK uses that to determine if a pipeline
// is "streaming" or not. This matters at least for executions on Dataflow.
//
// The test produces some number of key value elements, with various event time offsets,
// expecting that a single timer event is what fires. The test validates that all the
// input elements, and the timer elements have been emitted.
func timersEventTimePipelineBuilder(makeImp func(s beam.Scope) beam.PCollection) func(s beam.Scope) {
	return func(s beam.Scope) {
		var inputs, wantOutputs []kv[string, int]

		offset := 5000
		timerOutput := 4093

		numKeys := 50
		numDuplicateTimers := 15

		for key := 0; key < numKeys; key++ {
			k := strconv.Itoa(key)
			wantOutputs = append(wantOutputs, kvfn(k, timerOutput))

			for i := 0; i < numDuplicateTimers; i++ {
				inputs = append(inputs, kvfn(k, i))
				wantOutputs = append(wantOutputs, kvfn(k, i+offset))
			}
		}

		imp := makeImp(s)

		keyed := beam.ParDo(s, &inputFn[string, int]{
			Inputs: inputs,
		}, imp)
		times := beam.ParDo(s, &eventTimeFn{
			Offset:      offset,
			TimerOutput: timerOutput,
			Callback:    timers.InEventTime("Callback"),
			MyKey:       state.MakeValueState[string]("MyKey"),
		}, keyed)
		passert.EqualsList(s, times, wantOutputs)
	}
}

// TimersEventTime_Bounded validates event time timers in a bounded pipeline.
func TimersEventTime_Bounded(s beam.Scope) {
	timersEventTimePipelineBuilder(beam.Impulse)(s)
}

// TimersEventTime_Bounded validates event time timers in an unbounded pipeline.
func TimersEventTime_Unbounded(s beam.Scope) {
	timersEventTimePipelineBuilder(func(s beam.Scope) beam.PCollection {
		now := time.Now()
		return periodic.Impulse(s, now, now.Add(10*time.Second), 0, false)
	})(s)
}
