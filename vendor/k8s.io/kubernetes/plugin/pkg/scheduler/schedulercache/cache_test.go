/*
Copyright 2015 The Kubernetes Authors.

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

package schedulercache

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/api/core/v1"
	"k8s.io/api/policy/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	priorityutil "k8s.io/kubernetes/plugin/pkg/scheduler/algorithm/priorities/util"
	schedutil "k8s.io/kubernetes/plugin/pkg/scheduler/util"
)

func deepEqualWithoutGeneration(t *testing.T, testcase int, actual, expected *NodeInfo) {
	// Ignore generation field.
	if actual != nil {
		actual.generation = 0
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("#%d: node info get=%s, want=%s", testcase, actual, expected)
	}
}

// TestAssumePodScheduled tests that after a pod is assumed, its information is aggregated
// on node level.
func TestAssumePodScheduled(t *testing.T) {
	nodeName := "node"
	testPods := []*v1.Pod{
		makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-1", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-2", "200m", "1Ki", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-nonzero", "", "", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test", "100m", "500", "example.com/foo:3", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-2", "200m", "1Ki", "example.com/foo:5", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test", "100m", "500", "random-invalid-extended-key:100", []v1.ContainerPort{{}}),
	}

	tests := []struct {
		pods []*v1.Pod

		wNodeInfo *NodeInfo
	}{{
		pods: []*v1.Pod{testPods[0]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[0]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}, {
		pods: []*v1.Pod{testPods[1], testPods[2]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 300,
				Memory:   1524,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 300,
				Memory:   1524,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[1], testPods[2]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true, "TCP/127.0.0.1/8080": true},
		},
	}, { // test non-zero request
		pods: []*v1.Pod{testPods[3]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 0,
				Memory:   0,
			},
			nonzeroRequest: &Resource{
				MilliCPU: priorityutil.DefaultMilliCpuRequest,
				Memory:   priorityutil.DefaultMemoryRequest,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[3]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}, {
		pods: []*v1.Pod{testPods[4]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU:        100,
				Memory:          500,
				ScalarResources: map[v1.ResourceName]int64{"example.com/foo": 3},
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[4]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}, {
		pods: []*v1.Pod{testPods[4], testPods[5]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU:        300,
				Memory:          1524,
				ScalarResources: map[v1.ResourceName]int64{"example.com/foo": 8},
			},
			nonzeroRequest: &Resource{
				MilliCPU: 300,
				Memory:   1524,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[4], testPods[5]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true, "TCP/127.0.0.1/8080": true},
		},
	}, {
		pods: []*v1.Pod{testPods[6]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[6]},
			usedPorts:           map[string]bool{},
		},
	},
	}

	for i, tt := range tests {
		cache := newSchedulerCache(time.Second, time.Second, nil)
		for _, pod := range tt.pods {
			if err := cache.AssumePod(pod); err != nil {
				t.Fatalf("AssumePod failed: %v", err)
			}
		}
		n := cache.nodes[nodeName]
		deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo)

		for _, pod := range tt.pods {
			if err := cache.ForgetPod(pod); err != nil {
				t.Fatalf("ForgetPod failed: %v", err)
			}
		}
		if cache.nodes[nodeName] != nil {
			t.Errorf("NodeInfo should be cleaned for %s", nodeName)
		}
	}
}

type testExpirePodStruct struct {
	pod         *v1.Pod
	assumedTime time.Time
}

func assumeAndFinishBinding(cache *schedulerCache, pod *v1.Pod, assumedTime time.Time) error {
	if err := cache.AssumePod(pod); err != nil {
		return err
	}
	return cache.finishBinding(pod, assumedTime)
}

// TestExpirePod tests that assumed pods will be removed if expired.
// The removal will be reflected in node info.
func TestExpirePod(t *testing.T) {
	nodeName := "node"
	testPods := []*v1.Pod{
		makeBasePod(t, nodeName, "test-1", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-2", "200m", "1Ki", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
	}
	now := time.Now()
	ttl := 10 * time.Second
	tests := []struct {
		pods        []*testExpirePodStruct
		cleanupTime time.Time

		wNodeInfo *NodeInfo
	}{{ // assumed pod would expires
		pods: []*testExpirePodStruct{
			{pod: testPods[0], assumedTime: now},
		},
		cleanupTime: now.Add(2 * ttl),
		wNodeInfo:   nil,
	}, { // first one would expire, second one would not.
		pods: []*testExpirePodStruct{
			{pod: testPods[0], assumedTime: now},
			{pod: testPods[1], assumedTime: now.Add(3 * ttl / 2)},
		},
		cleanupTime: now.Add(2 * ttl),
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[1]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/8080": true},
		},
	}}

	for i, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)

		for _, pod := range tt.pods {
			if err := assumeAndFinishBinding(cache, pod.pod, pod.assumedTime); err != nil {
				t.Fatalf("assumePod failed: %v", err)
			}
		}
		// pods that have assumedTime + ttl < cleanupTime will get expired and removed
		cache.cleanupAssumedPods(tt.cleanupTime)
		n := cache.nodes[nodeName]
		deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo)
	}
}

// TestAddPodWillConfirm tests that a pod being Add()ed will be confirmed if assumed.
// The pod info should still exist after manually expiring unconfirmed pods.
func TestAddPodWillConfirm(t *testing.T) {
	nodeName := "node"
	now := time.Now()
	ttl := 10 * time.Second

	testPods := []*v1.Pod{
		makeBasePod(t, nodeName, "test-1", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test-2", "200m", "1Ki", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
	}
	tests := []struct {
		podsToAssume []*v1.Pod
		podsToAdd    []*v1.Pod

		wNodeInfo *NodeInfo
	}{{ // two pod were assumed at same time. But first one is called Add() and gets confirmed.
		podsToAssume: []*v1.Pod{testPods[0], testPods[1]},
		podsToAdd:    []*v1.Pod{testPods[0]},
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[0]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}}

	for i, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, podToAssume := range tt.podsToAssume {
			if err := assumeAndFinishBinding(cache, podToAssume, now); err != nil {
				t.Fatalf("assumePod failed: %v", err)
			}
		}
		for _, podToAdd := range tt.podsToAdd {
			if err := cache.AddPod(podToAdd); err != nil {
				t.Fatalf("AddPod failed: %v", err)
			}
		}
		cache.cleanupAssumedPods(now.Add(2 * ttl))
		// check after expiration. confirmed pods shouldn't be expired.
		n := cache.nodes[nodeName]
		deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo)
	}
}

// TestAddPodWillReplaceAssumed tests that a pod being Add()ed will replace any assumed pod.
func TestAddPodWillReplaceAssumed(t *testing.T) {
	now := time.Now()
	ttl := 10 * time.Second

	assumedPod := makeBasePod(t, "assumed-node-1", "test-1", "100m", "500", "", []v1.ContainerPort{{HostPort: 80}})
	addedPod := makeBasePod(t, "actual-node", "test-1", "100m", "500", "", []v1.ContainerPort{{HostPort: 80}})
	updatedPod := makeBasePod(t, "actual-node", "test-1", "200m", "500", "", []v1.ContainerPort{{HostPort: 90}})

	tests := []struct {
		podsToAssume []*v1.Pod
		podsToAdd    []*v1.Pod
		podsToUpdate [][]*v1.Pod

		wNodeInfo map[string]*NodeInfo
	}{{
		podsToAssume: []*v1.Pod{assumedPod.DeepCopy()},
		podsToAdd:    []*v1.Pod{addedPod.DeepCopy()},
		podsToUpdate: [][]*v1.Pod{{addedPod.DeepCopy(), updatedPod.DeepCopy()}},
		wNodeInfo: map[string]*NodeInfo{
			"assumed-node": nil,
			"actual-node": {
				requestedResource: &Resource{
					MilliCPU: 200,
					Memory:   500,
				},
				nonzeroRequest: &Resource{
					MilliCPU: 200,
					Memory:   500,
				},
				allocatableResource: &Resource{},
				pods:                []*v1.Pod{updatedPod.DeepCopy()},
				usedPorts:           map[string]bool{"TCP/0.0.0.0/90": true},
			},
		},
	}}

	for i, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, podToAssume := range tt.podsToAssume {
			if err := assumeAndFinishBinding(cache, podToAssume, now); err != nil {
				t.Fatalf("assumePod failed: %v", err)
			}
		}
		for _, podToAdd := range tt.podsToAdd {
			if err := cache.AddPod(podToAdd); err != nil {
				t.Fatalf("AddPod failed: %v", err)
			}
		}
		for _, podToUpdate := range tt.podsToUpdate {
			if err := cache.UpdatePod(podToUpdate[0], podToUpdate[1]); err != nil {
				t.Fatalf("UpdatePod failed: %v", err)
			}
		}
		for nodeName, expected := range tt.wNodeInfo {
			t.Log(nodeName)
			n := cache.nodes[nodeName]
			deepEqualWithoutGeneration(t, i, n, expected)
		}
	}
}

// TestAddPodAfterExpiration tests that a pod being Add()ed will be added back if expired.
func TestAddPodAfterExpiration(t *testing.T) {
	nodeName := "node"
	ttl := 10 * time.Second
	basePod := makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}})
	tests := []struct {
		pod *v1.Pod

		wNodeInfo *NodeInfo
	}{{
		pod: basePod,
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{basePod},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}}

	now := time.Now()
	for i, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		if err := assumeAndFinishBinding(cache, tt.pod, now); err != nil {
			t.Fatalf("assumePod failed: %v", err)
		}
		cache.cleanupAssumedPods(now.Add(2 * ttl))
		// It should be expired and removed.
		n := cache.nodes[nodeName]
		if n != nil {
			t.Errorf("#%d: expecting nil node info, but get=%v", i, n)
		}
		if err := cache.AddPod(tt.pod); err != nil {
			t.Fatalf("AddPod failed: %v", err)
		}
		// check after expiration. confirmed pods shouldn't be expired.
		n = cache.nodes[nodeName]
		deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo)
	}
}

// TestUpdatePod tests that a pod will be updated if added before.
func TestUpdatePod(t *testing.T) {
	nodeName := "node"
	ttl := 10 * time.Second
	testPods := []*v1.Pod{
		makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test", "200m", "1Ki", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
	}
	tests := []struct {
		podsToAssume []*v1.Pod
		podsToAdd    []*v1.Pod
		podsToUpdate []*v1.Pod

		wNodeInfo []*NodeInfo
	}{{ // add a pod and then update it twice
		podsToAdd:    []*v1.Pod{testPods[0]},
		podsToUpdate: []*v1.Pod{testPods[0], testPods[1], testPods[0]},
		wNodeInfo: []*NodeInfo{{
			requestedResource: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[1]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/8080": true},
		}, {
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[0]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		}},
	}}

	for _, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, podToAdd := range tt.podsToAdd {
			if err := cache.AddPod(podToAdd); err != nil {
				t.Fatalf("AddPod failed: %v", err)
			}
		}

		for i := range tt.podsToUpdate {
			if i == 0 {
				continue
			}
			if err := cache.UpdatePod(tt.podsToUpdate[i-1], tt.podsToUpdate[i]); err != nil {
				t.Fatalf("UpdatePod failed: %v", err)
			}
			// check after expiration. confirmed pods shouldn't be expired.
			n := cache.nodes[nodeName]
			deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo[i-1])
		}
	}
}

// TestExpireAddUpdatePod test the sequence that a pod is expired, added, then updated
func TestExpireAddUpdatePod(t *testing.T) {
	nodeName := "node"
	ttl := 10 * time.Second
	testPods := []*v1.Pod{
		makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}}),
		makeBasePod(t, nodeName, "test", "200m", "1Ki", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 8080, Protocol: "TCP"}}),
	}
	tests := []struct {
		podsToAssume []*v1.Pod
		podsToAdd    []*v1.Pod
		podsToUpdate []*v1.Pod

		wNodeInfo []*NodeInfo
	}{{ // Pod is assumed, expired, and added. Then it would be updated twice.
		podsToAssume: []*v1.Pod{testPods[0]},
		podsToAdd:    []*v1.Pod{testPods[0]},
		podsToUpdate: []*v1.Pod{testPods[0], testPods[1], testPods[0]},
		wNodeInfo: []*NodeInfo{{
			requestedResource: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 200,
				Memory:   1024,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[1]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/8080": true},
		}, {
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{testPods[0]},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		}},
	}}

	now := time.Now()
	for _, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, podToAssume := range tt.podsToAssume {
			if err := assumeAndFinishBinding(cache, podToAssume, now); err != nil {
				t.Fatalf("assumePod failed: %v", err)
			}
		}
		cache.cleanupAssumedPods(now.Add(2 * ttl))

		for _, podToAdd := range tt.podsToAdd {
			if err := cache.AddPod(podToAdd); err != nil {
				t.Fatalf("AddPod failed: %v", err)
			}
		}

		for i := range tt.podsToUpdate {
			if i == 0 {
				continue
			}
			if err := cache.UpdatePod(tt.podsToUpdate[i-1], tt.podsToUpdate[i]); err != nil {
				t.Fatalf("UpdatePod failed: %v", err)
			}
			// check after expiration. confirmed pods shouldn't be expired.
			n := cache.nodes[nodeName]
			deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo[i-1])
		}
	}
}

// TestRemovePod tests after added pod is removed, its information should also be subtracted.
func TestRemovePod(t *testing.T) {
	nodeName := "node"
	basePod := makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}})
	tests := []struct {
		pod       *v1.Pod
		wNodeInfo *NodeInfo
	}{{
		pod: basePod,
		wNodeInfo: &NodeInfo{
			requestedResource: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			nonzeroRequest: &Resource{
				MilliCPU: 100,
				Memory:   500,
			},
			allocatableResource: &Resource{},
			pods:                []*v1.Pod{basePod},
			usedPorts:           map[string]bool{"TCP/127.0.0.1/80": true},
		},
	}}

	for i, tt := range tests {
		cache := newSchedulerCache(time.Second, time.Second, nil)
		if err := cache.AddPod(tt.pod); err != nil {
			t.Fatalf("AddPod failed: %v", err)
		}
		n := cache.nodes[nodeName]
		deepEqualWithoutGeneration(t, i, n, tt.wNodeInfo)

		if err := cache.RemovePod(tt.pod); err != nil {
			t.Fatalf("RemovePod failed: %v", err)
		}

		n = cache.nodes[nodeName]
		if n != nil {
			t.Errorf("#%d: expecting pod deleted and nil node info, get=%s", i, n)
		}
	}
}

func TestForgetPod(t *testing.T) {
	nodeName := "node"
	basePod := makeBasePod(t, nodeName, "test", "100m", "500", "", []v1.ContainerPort{{HostIP: "127.0.0.1", HostPort: 80, Protocol: "TCP"}})
	tests := []struct {
		pods []*v1.Pod
	}{{
		pods: []*v1.Pod{basePod},
	}}
	now := time.Now()
	ttl := 10 * time.Second

	for i, tt := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, pod := range tt.pods {
			if err := assumeAndFinishBinding(cache, pod, now); err != nil {
				t.Fatalf("assumePod failed: %v", err)
			}
			isAssumed, err := cache.IsAssumedPod(pod)
			if err != nil {
				t.Fatalf("IsAssumedPod failed: %v.", err)
			}
			if !isAssumed {
				t.Fatalf("Pod is expected to be assumed.")
			}
			assumedPod, err := cache.GetPod(pod)
			if err != nil {
				t.Fatalf("GetPod failed: %v.", err)
			}
			if assumedPod.Namespace != pod.Namespace {
				t.Errorf("assumedPod.Namespace != pod.Namespace (%s != %s)", assumedPod.Namespace, pod.Namespace)
			}
			if assumedPod.Name != pod.Name {
				t.Errorf("assumedPod.Name != pod.Name (%s != %s)", assumedPod.Name, pod.Name)
			}
		}
		for _, pod := range tt.pods {
			if err := cache.ForgetPod(pod); err != nil {
				t.Fatalf("ForgetPod failed: %v", err)
			}
			isAssumed, err := cache.IsAssumedPod(pod)
			if err != nil {
				t.Fatalf("IsAssumedPod failed: %v.", err)
			}
			if isAssumed {
				t.Fatalf("Pod is expected to be unassumed.")
			}
		}
		cache.cleanupAssumedPods(now.Add(2 * ttl))
		if n := cache.nodes[nodeName]; n != nil {
			t.Errorf("#%d: expecting pod deleted and nil node info, get=%s", i, n)
		}
	}
}

// getResourceRequest returns the resource request of all containers in Pods;
// excuding initContainers.
func getResourceRequest(pod *v1.Pod) v1.ResourceList {
	result := &Resource{}
	for _, container := range pod.Spec.Containers {
		result.Add(container.Resources.Requests)
	}

	return result.ResourceList()
}

// buildNodeInfo creates a NodeInfo by simulating node operations in cache.
func buildNodeInfo(node *v1.Node, pods []*v1.Pod) *NodeInfo {
	expected := NewNodeInfo()

	// Simulate SetNode.
	expected.node = node
	expected.allocatableResource = NewResource(node.Status.Allocatable)
	expected.taints = node.Spec.Taints
	expected.generation++

	for _, pod := range pods {
		// Simulate AddPod
		expected.pods = append(expected.pods, pod)
		expected.requestedResource.Add(getResourceRequest(pod))
		expected.nonzeroRequest.Add(getResourceRequest(pod))
		expected.usedPorts = schedutil.GetUsedPorts(pod)
		expected.generation++
	}

	return expected
}

// TestNodeOperators tests node operations of cache, including add, update
// and remove.
func TestNodeOperators(t *testing.T) {
	// Test datas
	nodeName := "test-node"
	cpu_1 := resource.MustParse("1000m")
	mem_100m := resource.MustParse("100m")
	cpu_half := resource.MustParse("500m")
	mem_50m := resource.MustParse("50m")
	resourceFooName := "example.com/foo"
	resourceFoo := resource.MustParse("1")

	tests := []struct {
		node *v1.Node
		pods []*v1.Pod
	}{
		{
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
				Status: v1.NodeStatus{
					Allocatable: v1.ResourceList{
						v1.ResourceCPU:                   cpu_1,
						v1.ResourceMemory:                mem_100m,
						v1.ResourceName(resourceFooName): resourceFoo,
					},
				},
				Spec: v1.NodeSpec{
					Taints: []v1.Taint{
						{
							Key:    "test-key",
							Value:  "test-value",
							Effect: v1.TaintEffectPreferNoSchedule,
						},
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod1",
						UID:  types.UID("pod1"),
					},
					Spec: v1.PodSpec{
						NodeName: nodeName,
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										v1.ResourceCPU:    cpu_half,
										v1.ResourceMemory: mem_50m,
									},
								},
								Ports: []v1.ContainerPort{
									{
										Name:          "http",
										HostPort:      80,
										ContainerPort: 80,
									},
								},
							},
						},
					},
				},
			},
		},
		{
			node: &v1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
				},
				Status: v1.NodeStatus{
					Allocatable: v1.ResourceList{
						v1.ResourceCPU:                   cpu_1,
						v1.ResourceMemory:                mem_100m,
						v1.ResourceName(resourceFooName): resourceFoo,
					},
				},
				Spec: v1.NodeSpec{
					Taints: []v1.Taint{
						{
							Key:    "test-key",
							Value:  "test-value",
							Effect: v1.TaintEffectPreferNoSchedule,
						},
					},
				},
			},
			pods: []*v1.Pod{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod1",
						UID:  types.UID("pod1"),
					},
					Spec: v1.PodSpec{
						NodeName: nodeName,
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										v1.ResourceCPU:    cpu_half,
										v1.ResourceMemory: mem_50m,
									},
								},
							},
						},
					},
				},
				{
					ObjectMeta: metav1.ObjectMeta{
						Name: "pod2",
						UID:  types.UID("pod2"),
					},
					Spec: v1.PodSpec{
						NodeName: nodeName,
						Containers: []v1.Container{
							{
								Resources: v1.ResourceRequirements{
									Requests: v1.ResourceList{
										v1.ResourceCPU:    cpu_half,
										v1.ResourceMemory: mem_50m,
									},
								},
							},
						},
					},
				},
			},
		},
	}

	for _, test := range tests {
		expected := buildNodeInfo(test.node, test.pods)
		node := test.node

		cache := newSchedulerCache(time.Second, time.Second, nil)
		cache.AddNode(node)
		for _, pod := range test.pods {
			cache.AddPod(pod)
		}

		// Case 1: the node was added into cache successfully.
		got, found := cache.nodes[node.Name]
		if !found {
			t.Errorf("Failed to find node %v in schedulercache.", node.Name)
		}

		if !reflect.DeepEqual(got, expected) {
			t.Errorf("Failed to add node into schedulercache:\n got: %+v \nexpected: %+v", got, expected)
		}

		// Case 2: dump cached nodes successfully.
		cachedNodes := map[string]*NodeInfo{}
		cache.UpdateNodeNameToInfoMap(cachedNodes)
		newNode, found := cachedNodes[node.Name]
		if !found || len(cachedNodes) != 1 {
			t.Errorf("failed to dump cached nodes:\n got: %v \nexpected: %v", cachedNodes, cache.nodes)
		}
		if !reflect.DeepEqual(newNode, expected) {
			t.Errorf("Failed to clone node:\n got: %+v, \n expected: %+v", newNode, expected)
		}

		// Case 3: update node attribute successfully.
		node.Status.Allocatable[v1.ResourceMemory] = mem_50m
		expected.allocatableResource.Memory = mem_50m.Value()
		expected.generation++
		cache.UpdateNode(nil, node)
		got, found = cache.nodes[node.Name]
		if !found {
			t.Errorf("Failed to find node %v in schedulercache after UpdateNode.", node.Name)
		}

		if !reflect.DeepEqual(got, expected) {
			t.Errorf("Failed to update node in schedulercache:\n got: %+v \nexpected: %+v", got, expected)
		}

		// Case 4: the node can not be removed if pods is not empty.
		cache.RemoveNode(node)
		if _, found := cache.nodes[node.Name]; !found {
			t.Errorf("The node %v should not be removed if pods is not empty.", node.Name)
		}
	}
}

func BenchmarkList1kNodes30kPods(b *testing.B) {
	cache := setupCacheOf1kNodes30kPods(b)
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		cache.List(labels.Everything())
	}
}

func BenchmarkExpire100Pods(b *testing.B) {
	benchmarkExpire(b, 100)
}

func BenchmarkExpire1kPods(b *testing.B) {
	benchmarkExpire(b, 1000)
}

func BenchmarkExpire10kPods(b *testing.B) {
	benchmarkExpire(b, 10000)
}

func benchmarkExpire(b *testing.B, podNum int) {
	now := time.Now()
	for n := 0; n < b.N; n++ {
		b.StopTimer()
		cache := setupCacheWithAssumedPods(b, podNum, now)
		b.StartTimer()
		cache.cleanupAssumedPods(now.Add(2 * time.Second))
	}
}

type testingMode interface {
	Fatalf(format string, args ...interface{})
}

func makeBasePod(t testingMode, nodeName, objName, cpu, mem, extended string, ports []v1.ContainerPort) *v1.Pod {
	req := v1.ResourceList{}
	if cpu != "" {
		req = v1.ResourceList{
			v1.ResourceCPU:    resource.MustParse(cpu),
			v1.ResourceMemory: resource.MustParse(mem),
		}
		if extended != "" {
			parts := strings.Split(extended, ":")
			if len(parts) != 2 {
				t.Fatalf("Invalid extended resource string: \"%s\"", extended)
			}
			req[v1.ResourceName(parts[0])] = resource.MustParse(parts[1])
		}
	}
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			UID:       types.UID(objName),
			Namespace: "node_info_cache_test",
			Name:      objName,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{{
				Resources: v1.ResourceRequirements{
					Requests: req,
				},
				Ports: ports,
			}},
			NodeName: nodeName,
		},
	}
}

func setupCacheOf1kNodes30kPods(b *testing.B) Cache {
	cache := newSchedulerCache(time.Second, time.Second, nil)
	for i := 0; i < 1000; i++ {
		nodeName := fmt.Sprintf("node-%d", i)
		for j := 0; j < 30; j++ {
			objName := fmt.Sprintf("%s-pod-%d", nodeName, j)
			pod := makeBasePod(b, nodeName, objName, "0", "0", "", nil)

			if err := cache.AddPod(pod); err != nil {
				b.Fatalf("AddPod failed: %v", err)
			}
		}
	}
	return cache
}

func setupCacheWithAssumedPods(b *testing.B, podNum int, assumedTime time.Time) *schedulerCache {
	cache := newSchedulerCache(time.Second, time.Second, nil)
	for i := 0; i < podNum; i++ {
		nodeName := fmt.Sprintf("node-%d", i/10)
		objName := fmt.Sprintf("%s-pod-%d", nodeName, i%10)
		pod := makeBasePod(b, nodeName, objName, "0", "0", "", nil)

		err := assumeAndFinishBinding(cache, pod, assumedTime)
		if err != nil {
			b.Fatalf("assumePod failed: %v", err)
		}
	}
	return cache
}

func makePDB(name, namespace string, labels map[string]string, minAvailable int) *v1beta1.PodDisruptionBudget {
	intstrMin := intstr.FromInt(minAvailable)
	pdb := &v1beta1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    labels,
		},
		Spec: v1beta1.PodDisruptionBudgetSpec{
			MinAvailable: &intstrMin,
			Selector:     &metav1.LabelSelector{MatchLabels: labels},
		},
	}

	return pdb
}

// TestPDBOperations tests that a PDB will be add/updated/deleted correctly.
func TestPDBOperations(t *testing.T) {
	ttl := 10 * time.Second
	testPDBs := []*v1beta1.PodDisruptionBudget{
		makePDB("pdb0", "ns1", map[string]string{"tkey1": "tval1"}, 3),
		makePDB("pdb1", "ns1", map[string]string{"tkey1": "tval1", "tkey2": "tval2"}, 1),
		makePDB("pdb2", "ns3", map[string]string{"tkey3": "tval3", "tkey2": "tval2"}, 10),
	}
	updatedPDBs := []*v1beta1.PodDisruptionBudget{
		makePDB("pdb0", "ns1", map[string]string{"tkey4": "tval4"}, 8),
		makePDB("pdb1", "ns1", map[string]string{"tkey1": "tval1"}, 1),
		makePDB("pdb2", "ns3", map[string]string{"tkey3": "tval3", "tkey1": "tval1", "tkey2": "tval2"}, 10),
	}
	tests := []struct {
		pdbsToAdd    []*v1beta1.PodDisruptionBudget
		pdbsToUpdate []*v1beta1.PodDisruptionBudget
		pdbsToDelete []*v1beta1.PodDisruptionBudget
		expectedPDBs []*v1beta1.PodDisruptionBudget // Expected PDBs after all operations
	}{
		{
			pdbsToAdd:    []*v1beta1.PodDisruptionBudget{testPDBs[0]},
			pdbsToUpdate: []*v1beta1.PodDisruptionBudget{testPDBs[0], testPDBs[1], testPDBs[0]},
			expectedPDBs: []*v1beta1.PodDisruptionBudget{testPDBs[0], testPDBs[1]}, // both will be in the cache as they have different names
		},
		{
			pdbsToAdd:    []*v1beta1.PodDisruptionBudget{testPDBs[0]},
			pdbsToUpdate: []*v1beta1.PodDisruptionBudget{testPDBs[0], updatedPDBs[0]},
			expectedPDBs: []*v1beta1.PodDisruptionBudget{updatedPDBs[0]},
		},
		{
			pdbsToAdd:    []*v1beta1.PodDisruptionBudget{testPDBs[0], testPDBs[2]},
			pdbsToUpdate: []*v1beta1.PodDisruptionBudget{testPDBs[0], updatedPDBs[0]},
			pdbsToDelete: []*v1beta1.PodDisruptionBudget{testPDBs[0]},
			expectedPDBs: []*v1beta1.PodDisruptionBudget{testPDBs[2]},
		},
	}

	for _, test := range tests {
		cache := newSchedulerCache(ttl, time.Second, nil)
		for _, pdbToAdd := range test.pdbsToAdd {
			if err := cache.AddPDB(pdbToAdd); err != nil {
				t.Fatalf("AddPDB failed: %v", err)
			}
		}

		for i := range test.pdbsToUpdate {
			if i == 0 {
				continue
			}
			if err := cache.UpdatePDB(test.pdbsToUpdate[i-1], test.pdbsToUpdate[i]); err != nil {
				t.Fatalf("UpdatePDB failed: %v", err)
			}
		}

		for _, pdb := range test.pdbsToDelete {
			if err := cache.RemovePDB(pdb); err != nil {
				t.Fatalf("RemovePDB failed: %v", err)
			}
		}

		cachedPDBs, err := cache.ListPDBs(labels.Everything())
		if err != nil {
			t.Fatalf("ListPDBs failed: %v", err)
		}
		if len(cachedPDBs) != len(test.expectedPDBs) {
			t.Errorf("Expected %d PDBs, got %d", len(test.expectedPDBs), len(cachedPDBs))
		}
		for _, pdb := range test.expectedPDBs {
			found := false
			// find it among the cached ones
			for _, cpdb := range cachedPDBs {
				if pdb.Name == cpdb.Name {
					found = true
					if !reflect.DeepEqual(pdb, cpdb) {
						t.Errorf("%v is not equal to %v", pdb, cpdb)
					}
					break
				}
			}
			if !found {
				t.Errorf("PDB with name '%v' was not found in the cache.", pdb.Name)
			}

		}
	}
}
