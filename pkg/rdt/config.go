/*
Copyright 2019 Intel Corporation

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

package rdt

import (
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"

	"github.com/intel/goresctrl/pkg/utils"
)

// Config represents the raw RDT configuration data from the configmap
type Config struct {
	Options    Options `json:"options"`
	Partitions map[string]struct {
		L3Allocation interface{} `json:"l3Allocation"`
		MBAllocation interface{} `json:"mbAllocation"`
		Classes      map[string]struct {
			L3Schema interface{} `json:"l3Schema"`
			MBSchema interface{} `json:"mbSchema"`
		} `json:"classes"`
	} `json:"partitions"`
}

// config represents the final (parsed and resolved) runtime configuration of
// RDT Control
type config struct {
	Options    Options
	Partitions partitionSet
	Classes    classSet
}

// partitionSet represents the pool of rdt partitions
type partitionSet map[string]partitionConfig

// classSet represents the pool of rdt classes
type classSet map[string]classConfig

// partitionConfig is the final configuration of one partition
type partitionConfig struct {
	L3 l3Schema
	MB mbSchema
}

// classConfig represents configuration of one class, i.e. one CTRL group in
// the Linux resctrl interface
type classConfig struct {
	Partition string
	L3Schema  l3Schema
	MBSchema  mbSchema
}

// Options contains the common settings for all classes
type Options struct {
	L3 l3Options `json:"l3"`
	MB mbOptions `json:"mb"`
}

// l3Options contains the common settings for L3 cache allocation
type l3Options struct {
	Optional bool
}

// mbOptions contains the common settings for memory bandwidth allocation
type mbOptions struct {
	Optional bool
}

// l3Schema represents the L3 part of the schemata of a class (i.e. resctrl group)
type l3Schema map[uint64]l3Allocation

// mbSchema represents the MB part of the schemata of a class (i.e. resctrl group)
type mbSchema map[uint64]uint64

// l3Allocation describes the L3 allocation configuration for one cache id
type l3Allocation struct {
	Unified cacheAllocation
	Code    cacheAllocation `json:",omitempty"`
	Data    cacheAllocation `json:",omitempty"`
}

// cacheAllocation is the basic interface for handling cache allocations of one
// type (unified, code, data)
type cacheAllocation interface {
	Overlay(Bitmask) (Bitmask, error)
}

// l3AbsoluteAllocation represents an explicitly specified cache allocation
// bitmask
type l3AbsoluteAllocation Bitmask

// l3PctAllocation represents a relative (percentage) share of the available
// bitmask
type l3PctAllocation uint64

// l3PctRangeAllocation represents a percentage range of the available bitmask
type l3PctRangeAllocation struct {
	lowPct  uint64
	highPct uint64
}

// L3SchemaType represents different L3 cache allocation schemes
type l3SchemaType string

const (
	// l3SchemaTypeUnified is the schema type when CDP is not enabled
	l3SchemaTypeUnified l3SchemaType = "unified"
	// l3SchemaTypeCode is the 'code' part of CDP schema
	l3SchemaTypeCode l3SchemaType = "code"
	// l3SchemaTypeData is the 'data' part of CDP schema
	l3SchemaTypeData l3SchemaType = "data"
)

func (t l3SchemaType) ToResctrlStr() string {
	if t == l3SchemaTypeUnified {
		return ""
	}
	return strings.ToUpper(string(t))
}

const (
	mbSuffixPct  = "%"
	mbSuffixMbps = "MBps"
)

// ToStr returns the L3 schema in a format accepted by the Linux kernel
// resctrl (schemata) interface
func (s l3Schema) ToStr(typ l3SchemaType, baseSchema l3Schema) (string, error) {
	schema := "L3" + typ.ToResctrlStr() + ":"
	sep := ""

	// Get a sorted slice of cache ids for deterministic output
	ids := make([]uint64, 0, len(baseSchema))
	for id := range baseSchema {
		ids = append(ids, id)
	}
	utils.SortUint64s(ids)

	for _, id := range ids {
		baseMask, ok := baseSchema[id].getEffective(typ).(l3AbsoluteAllocation)
		if !ok {
			return "", fmt.Errorf("BUG: basemask not of type l3AbsoluteAllocation")
		}
		bitmask := Bitmask(baseMask)

		if s != nil {
			var err error

			masks := s[id]
			overlayMask := masks.getEffective(typ)

			bitmask, err = overlayMask.Overlay(bitmask)
			if err != nil {
				return "", err
			}
		}
		schema += fmt.Sprintf("%s%d=%x", sep, id, bitmask)
		sep = ";"
	}

	return schema + "\n", nil
}

func (a l3Allocation) get(typ l3SchemaType) cacheAllocation {
	switch typ {
	case l3SchemaTypeCode:
		return a.Code
	case l3SchemaTypeData:
		return a.Data
	}
	return a.Unified
}

func (a l3Allocation) set(typ l3SchemaType, v cacheAllocation) l3Allocation {
	switch typ {
	case l3SchemaTypeCode:
		a.Code = v
	case l3SchemaTypeData:
		a.Data = v
	default:
		a.Unified = v
	}

	return a
}

func (a l3Allocation) getEffective(typ l3SchemaType) cacheAllocation {
	switch typ {
	case l3SchemaTypeCode:
		if a.Code != nil {
			return a.Code
		}
	case l3SchemaTypeData:
		if a.Data != nil {
			return a.Data
		}
	}
	// Use Unified as the default/fallback for Code and Data
	return a.Unified
}

// Overlay function of the cacheAllocation interface
func (a l3AbsoluteAllocation) Overlay(baseMask Bitmask) (Bitmask, error) {
	if err := verifyL3BaseMask(baseMask); err != nil {
		return 0, err
	}

	shiftWidth := baseMask.lsbOne()

	// Treat our bitmask relative to the basemask
	bitmask := Bitmask(a) << shiftWidth

	// Do bounds checking that we're "inside" the base mask
	if bitmask|baseMask != baseMask {
		return 0, rdtError("bitmask %#x (%#x << %d) does not fit basemask %#x", bitmask, a, shiftWidth, baseMask)
	}

	return bitmask, nil
}

// MarshalJSON implements the Marshaler interface of "encoding/json"
func (a l3AbsoluteAllocation) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("\"%#x\"", a)), nil
}

// Overlay function of the cacheAllocation interface
func (a l3PctAllocation) Overlay(baseMask Bitmask) (Bitmask, error) {
	return l3PctRangeAllocation{highPct: uint64(a)}.Overlay(baseMask)
}

// Overlay function of the cacheAllocation interface
func (a l3PctRangeAllocation) Overlay(baseMask Bitmask) (Bitmask, error) {
	if err := verifyL3BaseMask(baseMask); err != nil {
		return 0, err
	}

	baseMaskMsb := uint64(baseMask.msbOne())
	baseMaskLsb := uint64(baseMask.lsbOne())
	baseMaskNumBits := baseMaskMsb - baseMaskLsb + 1

	low, high := a.lowPct, a.highPct
	if low == 0 {
		low = 1
	}
	if low > high || low > 100 || high > 100 {
		return 0, rdtError("invalid percentage range in %v", a)
	}

	// Convert percentage limits to bit numbers
	// Our effective range is 1%-100%, use substraction (-1) because of
	// arithmetics, so that we don't overflow on 100%
	lsb := (low - 1) * baseMaskNumBits / 100
	msb := (high - 1) * baseMaskNumBits / 100

	// Make sure the number of bits set satisfies the minimum requirement
	numBits := msb - lsb + 1
	if numBits < info.l3MinCbmBits() {
		gap := info.l3MinCbmBits() - numBits

		// First, widen the mask from the "lsb end"
		if gap <= lsb {
			lsb -= gap
			gap = 0
		} else {
			gap -= lsb
			lsb = 0
		}
		// If needed, widen the mask from the "msb end"
		msbAvailable := baseMaskNumBits - msb - 1
		if gap <= msbAvailable {
			msb += gap
		} else {
			return 0, rdtError("BUG: not enough bits available for cache bitmask (%s applied on basemask %#x)", a, baseMask)
		}
	}

	value := ((1 << (msb - lsb + 1)) - 1) << (lsb + baseMaskLsb)

	return Bitmask(value), nil
}

func verifyL3BaseMask(baseMask Bitmask) error {
	if baseMask == 0 {
		return fmt.Errorf("empty basemask not allowed")
	}

	// Check that the basemask contains one (and only one) contiguous block of
	// (enough) bits set
	baseMaskWidth := baseMask.msbOne() - baseMask.lsbOne() + 1
	if bits.OnesCount64(uint64(baseMask)) != baseMaskWidth {
		return fmt.Errorf("invalid basemask %#x: more than one block of bits set", baseMask)
	}
	if uint64(bits.OnesCount64(uint64(baseMask))) < info.l3MinCbmBits() {
		return fmt.Errorf("invalid basemask %#x: fewer than %d bits set", baseMask, info.l3MinCbmBits())
	}

	return nil
}

// MarshalJSON implements the Marshaler interface of "encoding/json"
func (a l3PctAllocation) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("\"%d%%\"", a)), nil
}

// MarshalJSON implements the Marshaler interface of "encoding/json"
func (a l3PctRangeAllocation) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("\"%d-%d%%\"", a.lowPct, a.highPct)), nil
}

// ToStr returns the MB schema in a format accepted by the Linux kernel
// resctrl (schemata) interface
func (s mbSchema) ToStr(base map[uint64]uint64) string {
	schema := "MB:"
	sep := ""

	// Get a sorted slice of cache ids for deterministic output
	ids := make([]uint64, 0, len(base))
	for id := range base {
		ids = append(ids, id)
	}
	utils.SortUint64s(ids)

	for _, id := range ids {
		baseAllocation := base[id]
		value := uint64(0)
		if info.mb.mbpsEnabled {
			value = math.MaxUint32
			if s != nil {
				value = s[id]
			}
			// Limit to given base value
			if value > baseAllocation {
				value = baseAllocation
			}
		} else {
			allocation := uint64(100)
			if s != nil {
				allocation = s[id]
			}
			value = allocation * baseAllocation / 100
			// Guarantee minimum bw so that writing out the schemata does not fail
			if value < info.mb.minBandwidth {
				value = info.mb.minBandwidth
			}
		}

		schema += fmt.Sprintf("%s%d=%d", sep, id, value)
		sep = ";"
	}

	return schema + "\n"
}

// listStrToArray parses a string containing a human-readable list of numbers
// into an integer array
func listStrToArray(str string) ([]int, error) {
	a := []int{}

	// Empty list
	if len(str) == 0 {
		return a, nil
	}

	ranges := strings.Split(str, ",")
	for _, ran := range ranges {
		split := strings.SplitN(ran, "-", 2)

		// We limit to 8 bits in order to avoid accidental super long slices
		num, err := strconv.ParseInt(split[0], 10, 8)
		if err != nil {
			return a, rdtError("invalid integer %q: %v", str, err)
		}

		if len(split) == 1 {
			a = append(a, int(num))
		} else {
			endNum, err := strconv.ParseInt(split[1], 10, 8)
			if err != nil {
				return a, rdtError("invalid integer in range %q: %v", str, err)
			}
			if endNum <= num {
				return a, rdtError("invalid integer range %q in %q", ran, str)
			}
			for i := num; i <= endNum; i++ {
				a = append(a, int(i))
			}
		}
	}
	sort.Ints(a)
	return a, nil
}

// resolve tries to resolve the requested configuration into a working
// configuration
func (raw Config) resolve() (config, error) {
	var err error
	conf := config{Options: raw.Options}

	log.DebugBlock("", "resolving configuration: |\n%s", utils.DumpJSON(raw))

	conf.Partitions, err = raw.resolvePartitions()
	if err != nil {
		return conf, err
	}

	conf.Classes, err = raw.resolveClasses()
	if err != nil {
		return conf, err
	}

	return conf, nil
}

// resolvePartitions tries to resolve the requested resource allocations of
// partitions
func (raw Config) resolvePartitions() (partitionSet, error) {
	// Initialize empty partition configuration
	conf := make(partitionSet, len(raw.Partitions))
	numCacheIds := len(info.cacheIds)
	for name := range raw.Partitions {
		conf[name] = partitionConfig{L3: make(l3Schema, numCacheIds),
			MB: make(mbSchema, numCacheIds)}
	}

	// Try to resolve L3 partition allocations
	err := raw.resolveL3Partitions(conf)
	if err != nil {
		return nil, err
	}

	// Try to resolve MB partition allocations
	err = raw.resolveMBPartitions(conf)
	if err != nil {
		return nil, err
	}

	return conf, nil
}

// resolveL3Partitions tries to resolve requested L3 allocations between partitions
func (raw Config) resolveL3Partitions(conf partitionSet) error {
	allocationsPerCacheID := make(map[uint64][]l3PartitionAllocation, len(info.cacheIds))
	for _, id := range info.cacheIds {
		allocationsPerCacheID[id] = make([]l3PartitionAllocation, 0, len(raw.Partitions))
	}
	// Helper structure for printing out human-readable info in the end
	requests := map[string]map[uint64]l3Allocation{}

	// Resolve partitions in sorted order for reproducibility
	names := make([]string, 0, len(raw.Partitions))
	for name := range raw.Partitions {
		names = append(names, name)
	}
	sort.Strings(names)

	// Parse requested allocations from raw config and transfer them to our
	// per-cache-id structure
	numNils := 0
	for _, name := range names {
		allocations, err := parseRawL3Allocations(raw.Partitions[name].L3Allocation)
		if err != nil {
			return fmt.Errorf("failed to parse L3 allocation request for partition %q: %v", name, err)
		}

		requests[name] = allocations

		if allocations == nil {
			numNils++
		}

		for id, val := range allocations {
			allocationsPerCacheID[id] = append(allocationsPerCacheID[id], l3PartitionAllocation{name: name, allocation: val})
		}
	}

	if numNils == len(raw.Partitions) {
		log.Debug("L3 allocation disabled for all partitions")
		return nil
	} else if numNils != 0 {
		return fmt.Errorf("L3 allocation only specified for a subset of partitions")
	}

	// Next, try to resolve partition allocations, separately for each cache-id
	fullBitmaskNumBits := uint64(info.l3CbmMask().lsbZero())
	for id := range info.cacheIds {
		err := conf.resolveCacheID(uint64(id), allocationsPerCacheID[uint64(id)])
		if err != nil {
			return err
		}
	}

	log.Info("actual (and requested) L3 allocations per partition and cache id:")
	infoStr := ""
	for name, partition := range requests {
		infoStr += "\n    " + name
		for _, id := range info.cacheIds {
			infoStr += fmt.Sprintf("\n      %2d: ", id)
			allocationReq := partition[id]
			for _, typ := range []l3SchemaType{l3SchemaTypeUnified, l3SchemaTypeCode, l3SchemaTypeData} {
				infoStr += string(typ) + " "
				requested := allocationReq.get(typ)
				switch v := requested.(type) {
				case l3AbsoluteAllocation:
					infoStr += fmt.Sprintf("<absolute %#x>  ", v)
				case l3PctAllocation:
					granted := conf[name].L3[id].get(typ).(l3AbsoluteAllocation)
					requestedPct := fmt.Sprintf("(%d%%)", v)
					truePct := float64(bits.OnesCount64(uint64(granted))) * 100 / float64(fullBitmaskNumBits)
					infoStr += fmt.Sprintf("%5.1f%% %-6s ", truePct, requestedPct)
				case nil:
					infoStr += "<not specified>  "
				}
			}
		}
		infoStr += "\n"
	}
	log.InfoBlock("", "%s", infoStr)

	return nil
}

type l3PartitionAllocation struct {
	name       string
	allocation l3Allocation
}

// resolveCacheID resolves the partition allocations for one cache id
func (s partitionSet) resolveCacheID(id uint64, partitions []l3PartitionAllocation) error {
	for _, typ := range []l3SchemaType{l3SchemaTypeUnified, l3SchemaTypeCode, l3SchemaTypeData} {
		log.Debug("resolving partitions for %q schema for cache id %d", typ, id)
		err := s.resolveCacheIDPerType(id, partitions, typ)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s partitionSet) resolveCacheIDPerType(id uint64, partitions []l3PartitionAllocation, typ l3SchemaType) error {
	// Sanity check: if any partition has l3 allocation of this schema type
	// configured check that all other partitions have it, too
	a := partitions[0].allocation.get(typ)
	isNil := a == nil
	for _, partition := range partitions {
		if (partition.allocation.get(typ) == nil) != isNil {
			return fmt.Errorf("some partition(s) missing l3 %q allocation request for cache id %d", typ, id)
		}
	}

	// Act depending on the type of the first request in the list
	switch a.(type) {
	case l3AbsoluteAllocation:
		return s.resolveCacheIDAbsolute(id, partitions, typ)
	case nil:
	default:
		return s.resolveCacheIDRelative(id, partitions, typ)
	}
	return nil
}

func (s partitionSet) resolveCacheIDRelative(id uint64, partitions []l3PartitionAllocation, typ l3SchemaType) error {
	type reqHelper struct {
		name string
		req  uint64
	}

	// Sanity check:
	// 1. allocation requests are of the same type (relative)
	// 2. total allocation requested for this cache id does not exceed 100 percent
	// Additionally fill a helper structure for sorting partitions
	total := uint64(0)
	reqs := make([]reqHelper, 0, len(partitions))
	for _, partition := range partitions {
		switch a := partition.allocation.get(typ).(type) {
		case l3PctAllocation:
			total += uint64(a)
			reqs = append(reqs, reqHelper{name: partition.name, req: uint64(a)})
		case l3AbsoluteAllocation:
			return fmt.Errorf("error resolving L3 allocation for cache id %d: mixing relative and absolute allocations between partitions not supported", id)
		case l3PctRangeAllocation:
			return fmt.Errorf("percentage ranges in partition allocation not supported")
		default:
			return fmt.Errorf("BUG: unknown cacheAllocation type %T", a)
		}
	}
	if total < 100 {
		log.Info("requested total L3 %q partition allocation for cache id %d <100%% (%d%%)", typ, id, total)
	} else if total > 100 {
		return fmt.Errorf("accumulated L3 %q partition allocation requests for cache id %d exceeds 100%% (%d%%)", typ, id, total)
	}

	// Sort partition allocations. We want to resolve smallest allocations
	// first in order to try to ensure that all allocations can be satisfied
	// because small percentages might need to be rounded up
	sort.Slice(reqs, func(i, j int) bool {
		return reqs[i].req < reqs[j].req
	})

	// Calculate number of bits granted each partition.
	grants := make(map[string]uint64, len(partitions))
	minCbmBits := info.l3MinCbmBits()
	bitsTotal := uint64(info.l3CbmMask().lsbZero())
	bitsAvailable := bitsTotal
	for _, req := range reqs {
		percentageAvailable := bitsAvailable * 100 / bitsTotal

		// This might happen e.g. if number of partitions would be greater
		// than the total number of bits
		if bitsAvailable < minCbmBits {
			return fmt.Errorf("unable to resolve L3 allocation for cache id %d, not enough exlusive bits available", id)
		}

		// Use integer arithmetics, effectively always rounding down
		// fractional allocations i.e. trying to avoid over-allocation
		numBits := req.req * bitsAvailable / percentageAvailable

		// Guarantee a non-zero allocation
		if numBits < minCbmBits {
			numBits = minCbmBits
		}
		// Don't overflow
		if numBits > bitsAvailable {
			numBits = bitsAvailable
		}

		grants[req.name] = numBits
		bitsAvailable -= numBits
	}

	// Construct the actual bitmasks for each partition
	lsbID := uint64(0)
	for _, partition := range partitions {
		// Compose the actual bitmask
		v := s[partition.name].L3[id].set(typ, l3AbsoluteAllocation(Bitmask(((1<<grants[partition.name])-1)<<lsbID)))
		s[partition.name].L3[id] = v

		lsbID += grants[partition.name]
	}

	return nil
}

func (s partitionSet) resolveCacheIDAbsolute(id uint64, partitions []l3PartitionAllocation, typ l3SchemaType) error {
	// Just sanity check:
	// 1. allocation requests of the correct type (absolute)
	// 2. allocations do not overlap
	mask := Bitmask(0)
	for _, partition := range partitions {
		a, ok := partition.allocation.get(typ).(l3AbsoluteAllocation)
		if !ok {
			return fmt.Errorf("error resolving L3 allocation for cache id %d: mixing absolute and relative allocations between partitions not supported", id)
		}
		if Bitmask(a)&mask > 0 {
			return fmt.Errorf("overlapping L3 partition allocation requests for cache id %d", id)
		}
		mask |= Bitmask(a)

		s[partition.name].L3[id] = s[partition.name].L3[id].set(typ, a)
	}

	return nil
}

// resolveMBPartitions tries to resolve requested MB allocations between partitions
func (raw Config) resolveMBPartitions(conf partitionSet) error {
	// We use percentage values directly from the raw conf
	for name, partition := range raw.Partitions {
		allocations, err := parseRawMBAllocations(partition.MBAllocation)
		if err != nil {
			return fmt.Errorf("failed to resolve MB allocation for partition %q: %v", name, err)
		}
		for id, allocation := range allocations {
			conf[name].MB[id] = allocation
			// Check that we don't go under the minimum allowed bandwidth setting
			if !info.mb.mbpsEnabled && allocation < info.mb.minBandwidth {
				conf[name].MB[id] = info.mb.minBandwidth
			}
		}
	}

	return nil
}

// resolveClasses tries to resolve class allocations of all partitions
func (raw Config) resolveClasses() (classSet, error) {
	classes := make(classSet)

	for bname, partition := range raw.Partitions {
		for gname, class := range partition.Classes {
			if _, ok := classes[gname]; ok {
				return classes, fmt.Errorf("class names must be unique, %q defined multiple times", gname)
			}

			var err error
			gc := classConfig{Partition: bname}

			gc.L3Schema, err = parseRawL3Allocations(class.L3Schema)
			if err != nil {
				return classes, fmt.Errorf("failed to resolve L3 allocation for class %q: %v", gname, err)
			}
			if gc.L3Schema != nil && partition.L3Allocation == nil {
				return classes, fmt.Errorf("L3 allocation missing from partition %q but class %q specifies L3 schema", bname, gname)
			}

			gc.MBSchema, err = parseRawMBAllocations(class.MBSchema)
			if err != nil {
				return classes, fmt.Errorf("failed to resolve MB allocation for class %q: %v", gname, err)
			}
			if gc.MBSchema != nil && partition.MBAllocation == nil {
				return classes, fmt.Errorf("MB allocation missing from partition %q but class %q specifies MB schema", bname, gname)
			}

			classes[gname] = gc
		}
	}

	return classes, nil
}

// parseRawL3Allocations parses a raw L3 cache allocation
func parseRawL3Allocations(raw interface{}) (l3Schema, error) {
	rawValues, err := preparseRawAllocations(raw, "100%", false)
	if err != nil || rawValues == nil {
		return nil, err
	}

	allocations := make(l3Schema, len(rawValues))
	for id, rawVal := range rawValues {
		allocations[id], err = parseL3Allocation(rawVal)
		if err != nil {
			return nil, err
		}
	}

	return allocations, nil
}

// parseRawMBAllocations parses a raw MB allocation
func parseRawMBAllocations(raw interface{}) (mbSchema, error) {
	rawValues, err := preparseRawAllocations(raw, []interface{}{}, false)
	if err != nil || rawValues == nil {
		return nil, err
	}

	allocations := make(mbSchema, len(rawValues))
	for id, rawVal := range rawValues {
		strList, ok := rawVal.([]interface{})
		if !ok {
			return nil, fmt.Errorf("not a list value %q", rawVal)
		}
		allocations[id], err = parseMBAllocation(strList)
		if err != nil {
			return nil, err
		}
	}

	return allocations, nil
}

// preparseRawAllocations "pre-parses" the rawAllocations per each cache id. I.e. it assigns
// a raw (string) allocation for each cache id
func preparseRawAllocations(raw interface{}, defaultVal interface{}, initEmpty bool) (map[uint64]interface{}, error) {
	if raw == nil && !initEmpty {
		return nil, nil
	}

	var rawPerCacheId map[string]interface{}
	allocations := make(map[uint64]interface{}, len(info.cacheIds))

	switch value := raw.(type) {
	case string:
		defaultVal = value
	case []interface{}:
		defaultVal = value
	case map[string]interface{}:
		if all, ok := value["all"]; ok {
			defaultVal = all
		} else if defaultVal == nil {
			return nil, fmt.Errorf("'all' is missing")
		}
		rawPerCacheId = value
	default:
		return allocations, fmt.Errorf("invalid structure of allocation schema '%v' (%T)", raw, raw)
	}

	for _, i := range info.cacheIds {
		allocations[i] = defaultVal
	}

	for key, val := range rawPerCacheId {
		if key == "all" {
			continue
		}
		ids, err := listStrToArray(key)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, ok := allocations[uint64(id)]; ok {
				allocations[uint64(id)] = val
			}
		}
	}

	return allocations, nil
}

// parseL3Allocation parses a generic string map into l3Allocation struct
func parseL3Allocation(raw interface{}) (l3Allocation, error) {
	var err error
	allocation := l3Allocation{}

	switch value := raw.(type) {
	case string:
		allocation.Unified, err = parseCacheAllocation(value)
		if err != nil {
			return allocation, err
		}
	case map[string]interface{}:
		for k, v := range value {
			s, ok := v.(string)
			if !ok {
				return allocation, fmt.Errorf("not a string value %q", v)
			}
			switch strings.ToLower(k) {
			case string(l3SchemaTypeUnified):
				allocation.Unified, err = parseCacheAllocation(s)
			case string(l3SchemaTypeCode):
				allocation.Code, err = parseCacheAllocation(s)
			case string(l3SchemaTypeData):
				allocation.Data, err = parseCacheAllocation(s)
			}
			if err != nil {
				return allocation, err
			}
		}
	default:
		return allocation, fmt.Errorf("invalid structure of l3Schema %q", raw)
	}

	// Sanity check for the configuration
	if allocation.Unified == nil {
		return allocation, fmt.Errorf("'unified' not specified in l3Schema %s", raw)
	}
	if allocation.Code != nil && allocation.Data == nil {
		return allocation, fmt.Errorf("'code' specified but missing 'data' from l3Schema %s", raw)
	}
	if allocation.Code == nil && allocation.Data != nil {
		return allocation, fmt.Errorf("'data' specified but missing 'code' from l3Schema %s", raw)
	}

	return allocation, nil
}

// parseCacheAllocation parses a string value into cacheAllocation type
func parseCacheAllocation(data string) (cacheAllocation, error) {
	if data[len(data)-1] == '%' {
		// Percentages of the max number of bits
		split := strings.SplitN(data[0:len(data)-1], "-", 2)
		var allocation cacheAllocation

		if len(split) == 1 {
			pct, err := strconv.ParseUint(split[0], 10, 7)
			if err != nil {
				return allocation, err
			}
			if pct > 100 {
				return allocation, fmt.Errorf("invalid percentage value %q", data)
			}
			allocation = l3PctAllocation(pct)
		} else {
			low, err := strconv.ParseUint(split[0], 10, 7)
			if err != nil {
				return allocation, err
			}
			high, err := strconv.ParseUint(split[1], 10, 7)
			if err != nil {
				return allocation, err
			}
			if low > high || low > 100 || high > 100 {
				return allocation, fmt.Errorf("invalid percentage range %q", data)
			}
			allocation = l3PctRangeAllocation{lowPct: low, highPct: high}
		}

		return allocation, nil
	}

	// Absolute allocation
	var value uint64
	var err error
	if strings.HasPrefix(data, "0x") {
		// Hex value
		value, err = strconv.ParseUint(data[2:], 16, 64)
		if err != nil {
			return nil, err
		}
	} else {
		// Last, try "list" format (i.e. smthg like 0,2,5-9,...)
		tmp, err := ListStrToBitmask(data)
		value = uint64(tmp)
		if err != nil {
			return nil, err
		}
	}

	// Sanity check of absolute allocation: bitmask must (only) contain one
	// contiguous block of ones wide enough
	numOnes := bits.OnesCount64(value)
	if numOnes != 64-bits.LeadingZeros64(value)-bits.TrailingZeros64(value) {
		return nil, fmt.Errorf("invalid cache bitmask %q: more than one continuous block of ones", data)
	}
	if uint64(numOnes) < info.l3MinCbmBits() {
		return nil, fmt.Errorf("invalid cache bitmask %q: number of bits less than %d", data, info.l3MinCbmBits())
	}

	return l3AbsoluteAllocation(value), nil
}

// parseMBAllocation parses a generic string map into MB allocation value
func parseMBAllocation(raw []interface{}) (uint64, error) {
	for _, v := range raw {
		strVal, ok := v.(string)
		if !ok {
			log.Warn("ignoring non-string (%T) MBA allocation %v", v, v)
			continue
		}
		if strings.HasSuffix(strVal, mbSuffixPct) {
			if !info.mb.mbpsEnabled {
				value, err := strconv.ParseUint(strings.TrimSuffix(strVal, mbSuffixPct), 10, 7)
				if err != nil {
					return 0, err
				}
				return value, nil
			}
		} else if strings.HasSuffix(strVal, mbSuffixMbps) {
			if info.mb.mbpsEnabled {
				value, err := strconv.ParseUint(strings.TrimSuffix(strVal, mbSuffixMbps), 10, 32)
				if err != nil {
					return 0, err
				}
				return value, nil
			}
		} else {
			log.Warn("unrecognized MBA allocation unit in %q", strVal)
		}
	}

	// No value for the active mode was specified
	if info.mb.mbpsEnabled {
		return 0, fmt.Errorf("missing 'MBps' value from mbSchema; required because 'mba_MBps' is enabled in the system")
	}
	return 0, fmt.Errorf("missing '%%' value from mbSchema; required because percentage-based MBA allocation is enabled in the system")
}
