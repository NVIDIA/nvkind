/*
 * Copyright (c) 2026, NVIDIA CORPORATION.  All rights reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package nvkind

import (
	"strings"
	"testing"
)

func TestSortNodeNames(t *testing.T) {
	names := []string{
		"c-worker10", "c-worker", "c-control-plane", "c-worker2",
		"c-worker11", "c-control-plane2", "c-worker9",
	}
	want := []string{
		"c-control-plane", "c-control-plane2",
		"c-worker", "c-worker2", "c-worker9", "c-worker10", "c-worker11",
	}
	sortNodeNames(names)
	if strings.Join(names, " ") != strings.Join(want, " ") {
		t.Errorf("got %v, want %v", names, want)
	}
}
