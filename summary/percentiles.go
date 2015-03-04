// Copyright 2015 Google Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Utility methods to calculate percentiles.

package summary

import (
	"fmt"
	"math"
	"sort"

	"github.com/golang/glog"
	info "github.com/google/cadvisor/info/v1"
)

const secondsToMilliSeconds = 1000
const milliSecondsToNanoSeconds = 1000000
const secondsToNanoSeconds = secondsToMilliSeconds * milliSecondsToNanoSeconds

type uint64Slice []uint64

func (a uint64Slice) Len() int           { return len(a) }
func (a uint64Slice) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }
func (a uint64Slice) Less(i, j int) bool { return a[i] < a[j] }

// Get 90th percentile of the provided samples. Round to integer.
func (self uint64Slice) Get90Percentile() uint64 {
	count := self.Len()
	if count == 0 {
		return 0
	}
	sort.Sort(self)
	n := float64(0.9 * (float64(count) + 1))
	idx, frac := math.Modf(n)
	index := int(idx)
	percentile := float64(self[index-1])
	if index > 1 && index < count {
		percentile += frac * float64(self[index]-self[index-1])
	}
	return uint64(percentile)
}

type mean struct {
	// current count.
	count uint64
	// current mean.
	Mean float64
}

func (self *mean) Add(value uint64) {
	self.count++
	if self.count == 1 {
		self.Mean = float64(value)
		return
	}
	c := float64(self.count)
	v := float64(value)
	self.Mean = (self.Mean*(c-1) + v) / c
}

type resource struct {
	// list of samples being tracked.
	samples uint64Slice
	// average from existing samples.
	mean mean
	// maximum value seen so far in the added samples.
	max uint64
}

// Adds a new percentile sample.
func (self *resource) Add(p info.Percentiles) {
	if !p.Present {
		return
	}
	if p.Max > self.max {
		self.max = p.Max
	}
	self.mean.Add(p.Mean)
	// Selecting 90p of 90p :(
	self.samples = append(self.samples, p.Ninety)
}

// Add a single sample. Internally, we convert it to a fake percentile sample.
func (self *resource) AddSample(val uint64) {
	sample := info.Percentiles{
		Present: true,
		Mean:    val,
		Max:     val,
		Ninety:  val,
	}
	self.Add(sample)
}

// Get max, average, and 90p from existing samples.
func (self *resource) GetPercentile() info.Percentiles {
	p := info.Percentiles{}
	p.Mean = uint64(self.mean.Mean)
	p.Max = self.max
	p.Ninety = self.samples.Get90Percentile()
	p.Present = true
	return p
}

func NewResource(size int) *resource {
	return &resource{
		samples: make(uint64Slice, 0, size),
		mean:    mean{count: 0, Mean: 0},
	}
}

// Return aggregated percentiles from the provided percentile samples.
func GetDerivedPercentiles(stats []*info.Usage) info.Usage {
	cpu := NewResource(len(stats))
	memory := NewResource(len(stats))
	for _, stat := range stats {
		cpu.Add(stat.Cpu)
		memory.Add(stat.Memory)
	}
	usage := info.Usage{}
	usage.Cpu = cpu.GetPercentile()
	usage.Memory = memory.GetPercentile()
	return usage
}

// Calculate part of a minute this sample set represent.
func getPercentComplete(stats []*secondSample) (percent int32) {
	numSamples := len(stats)
	if numSamples > 1 {
		percent = 100
		timeRange := stats[numSamples-1].Timestamp.Sub(stats[0].Timestamp).Nanoseconds()
		// allow some slack
		if timeRange < 58*secondsToNanoSeconds {
			percent = int32((timeRange * 100) / 60 * secondsToNanoSeconds)
		}
	}
	return
}

// Calculate cpurate from two consecutive total cpu usage samples.
func getCpuRate(latest, previous secondSample) (uint64, error) {
	var elapsed int64
	elapsed = latest.Timestamp.Sub(previous.Timestamp).Nanoseconds()
	if elapsed < 10*milliSecondsToNanoSeconds {
		return 0, fmt.Errorf("elapsed time too small: %d ns: time now %s last %s", elapsed, latest.Timestamp.String(), previous.Timestamp.String())
	}
	if latest.Cpu < previous.Cpu {
		return 0, fmt.Errorf("bad sample: cumulative cpu usage dropped from %d to %d", latest.Cpu, previous.Cpu)
	}
	// Cpurate is calculated in cpu-milliseconds per second.
	cpuRate := (latest.Cpu - previous.Cpu) * secondsToMilliSeconds / uint64(elapsed)
	return cpuRate, nil
}

// Returns a percentile sample for a minute by aggregating seconds samples.
func GetMinutePercentiles(stats []*secondSample) info.Usage {
	lastSample := secondSample{}
	cpu := NewResource(len(stats))
	memory := NewResource(len(stats))
	for _, stat := range stats {
		if !lastSample.Timestamp.IsZero() {
			cpuRate, err := getCpuRate(*stat, lastSample)
			if err != nil {
				glog.V(2).Infof("Skipping sample, %v", err)
				continue
			}
			glog.V(2).Infof("Adding cpu rate sample : %d", cpuRate)
			cpu.AddSample(cpuRate)
			memory.AddSample(stat.Memory)
		} else {
			memory.AddSample(stat.Memory)
		}
		lastSample = *stat
	}
	percent := getPercentComplete(stats)
	return info.Usage{
		PercentComplete: percent,
		Cpu:             cpu.GetPercentile(),
		Memory:          memory.GetPercentile(),
	}
}
