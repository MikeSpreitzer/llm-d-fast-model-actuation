/*
Copyright 2025 The llm-d Authors.

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

package spi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"

	stubapi "github.com/llm-d-incubation/llm-d-fast-model-actuation/pkg/stub/api"
)

func FuzzServer(f *testing.F) {
	f.Add(true)
	f.Add(false)
	gpuIDs := []string{"abc-def", "dead-beef"}
	var ready atomic.Bool
	ctx, cancel := context.WithCancel(f.Context())
	port := "28083"
	go func() {
		err := runTestable(ctx, port, &ready, gpuIDs)
		if err != nil {
			f.Logf("Run failed: %s", err.Error())
		}
	}()
	paths := map[bool]string{false: stubapi.BecomeUnreadyPath, true: stubapi.BecomeReadyPath}
	f.Fuzz(func(t *testing.T, beReady bool) {
		path := paths[beReady]
		resp, err := http.Post("http://localhost:"+port+path, "text/plain", nil)
		if err != nil {
			t.Fatalf("Failed to POST to %s: %s", path, err.Error())
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("POST returned unexpected status %v", resp.StatusCode)
		} else if got := ready.Load(); got != beReady {
			t.Logf("Expected %v, got %v", beReady, got)
		} else {
			t.Logf("Successful test of %v", beReady)
		}

		resp, err = http.Get("http://localhost:" + port + stubapi.AcceleratorQueryPath)
		var gotIDs []string
		if err != nil {
			t.Fatalf("Failed to GET to %s: %s", stubapi.AcceleratorQueryPath, err.Error())
		} else if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned unexpected status %v", stubapi.AcceleratorQueryPath, resp.StatusCode)
		} else if respBytes, err := io.ReadAll(resp.Body); err != nil {
			t.Errorf("Failed to read response body: %s", err.Error())
		} else if err = json.Unmarshal(respBytes, &gotIDs); err != nil {
			t.Errorf("Failed to unmarshal response body %q: %s", string(respBytes), err.Error())
		} else if !slices.Equal(gotIDs, gpuIDs) {
			t.Errorf("GPU ID query returned %#v instead of %#v", gotIDs, gpuIDs)
		} else {
			t.Logf("Successful test of %#v", gpuIDs)
		}

	})
	cancel()
}
