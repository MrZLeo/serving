/*
Copyright 2020 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package queue

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"go.opencensus.io/trace"
	netheader "knative.dev/networking/pkg/http/header"
	netstats "knative.dev/networking/pkg/http/stats"
	"knative.dev/serving/pkg/activator"
)

var instances int32 = 1

type HandlerEntry struct {
	breaker *Breaker
	handler http.Handler
}

type HandlerVec struct {
	entrys []HandlerEntry
}

func (h HandlerVec) GetEntry() (*Breaker, http.Handler) {
	// use random number to decide which instance to go
	// SAFETY: len < len(h.entrys)
	len := atomic.LoadInt32(&instances)
	i := rand.Int31n(len)

	entry := h.entrys[i]
	return entry.breaker, entry.handler
}

// ProxyHandler sends requests to the `next` handler at a rate controlled by
// the passed `breaker`, while recording stats to `stats`.
func ProxyHandler(breakers []*Breaker, stats *netstats.RequestStats, tracingEnabled bool, next []http.Handler) http.HandlerFunc {
	// SAFETY: breakers.len() == next.len()
	handlerEntrys := make([]HandlerEntry, len(breakers))
	for i := 0; i < len(breakers); i++ {
		handlerEntrys[i] = HandlerEntry{
			breaker: breakers[i],
			handler: next[i],
		}
	}
	handlerVec := HandlerVec{entrys: handlerEntrys}

	// timer to check size of instances
	const TIME_INTERVAL = 500 * time.Millisecond
	ticker := time.NewTicker(TIME_INTERVAL)

	go func() {
		for range ticker.C {
			// adjust the size of instance
			newSize := InstanceAvailable(len(breakers))
			atomic.StoreInt32(&instances, newSize)
			fmt.Printf("set new size: %d\n", newSize)
		}
	}()

	return func(w http.ResponseWriter, r *http.Request) {
		// choose a instance to serve request
		breaker, next := handlerVec.GetEntry()

		if netheader.IsKubeletProbe(r) {
			next.ServeHTTP(w, r)
			return
		}

		if tracingEnabled {
			proxyCtx, proxySpan := trace.StartSpan(r.Context(), "queue_proxy")
			r = r.WithContext(proxyCtx)
			defer proxySpan.End()
		}

		// Metrics for autoscaling.
		in, out := netstats.ReqIn, netstats.ReqOut
		if activator.Name == netheader.GetKnativeProxyValue(r) {
			in, out = netstats.ProxiedIn, netstats.ProxiedOut
		}
		stats.HandleEvent(netstats.ReqEvent{Time: time.Now(), Type: in})
		defer func() {
			stats.HandleEvent(netstats.ReqEvent{Time: time.Now(), Type: out})
		}()
		netheader.RewriteHostOut(r)

		// Enforce queuing and concurrency limits.
		if breaker != nil {
			var waitSpan *trace.Span
			if tracingEnabled {
				_, waitSpan = trace.StartSpan(r.Context(), "queue_wait")
			}
			if err := breaker.Maybe(r.Context(), func() {
				waitSpan.End()
				next.ServeHTTP(w, r)
			}); err != nil {
				waitSpan.End()
				if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrRequestQueueFull) {
					http.Error(w, err.Error(), http.StatusServiceUnavailable)
				} else {
					// This line is most likely untestable :-).
					w.WriteHeader(http.StatusInternalServerError)
				}
			}
		} else {
			next.ServeHTTP(w, r)
		}
	}
}

// WARN: use ps to find base process, DON'T USE IT IN YOUR PRODUCTION
func InstanceAvailable(size int) int32 {

	const START_PORT int = 8080
	END_PORT := START_PORT + size
	cnt := 0
	for port := START_PORT; port <= END_PORT; port++ {
		addr := fmt.Sprintf(":%d", port)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			cnt++
			continue
		}
		ln.Close()
	}
	return int32(cnt)
}
