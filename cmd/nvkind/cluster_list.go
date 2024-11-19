/*
 * Copyright (c) 2024, NVIDIA CORPORATION.  All rights reserved.
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

package main

import (
	"fmt"
	"sort"

	"github.com/urfave/cli/v2"

	"github.com/NVIDIA/nvkind/pkg/nvkind"
)

func BuildClusterListCommand() *cli.Command {
	cmd := cli.Command{}
	cmd.Name = "list"
	cmd.Usage = "list all kind clusters (whether they have GPUs on them or not)"
	cmd.Action = runClusterList
	return &cmd
}

func runClusterList(c *cli.Context) error {
	clusters, err := nvkind.GetClusterNames()
	if err != nil {
		return fmt.Errorf("getting cluster names: %w", err)
	}

	clusterList := clusters.UnsortedList()
	sort.Strings(clusterList)

	if len(clusterList) == 0 {
		fmt.Println("No kind clusters found.")
	}

	for _, cluster := range clusterList {
		fmt.Println(cluster)
	}

	return nil
}
