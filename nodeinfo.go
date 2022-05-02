package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NodeInfo ... information of node
type NodeInfo struct {
	*sync.RWMutex
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`

	RequestCount  int64 `json:"request_count"`
	SentBytes     int64 `json:"sent_bytes"`
	ReceivedBytes int64 `json:"received_bytes"`

	CPU         float64 `json:"cpu"`
	Memory      float64 `json:"memory"`
	ActiveConns int64   `json:"active_conns"`
	TotalConns  int64   `json:"total_conns"`

	ELBs map[string]*NodeInfo `json:"elbs,omitempty"`
}

// NewNodeInfo ... create node info instance
func NewNodeInfo() *NodeInfo {
	now := time.Now().UnixNano()
	_node := &NodeInfo{
		CreatedAt: now,
		UpdatedAt: now,
		ELBs:      make(map[string]*NodeInfo),
	}
	_node.RWMutex = &sync.RWMutex{}
	return _node
}
func (ni *NodeInfo) getUpdatedAt() int64 {
	ni.RLock()
	defer ni.RUnlock()
	return ni.UpdatedAt
}
func (ni *NodeInfo) updateConns() {
	ni.Lock()
	defer ni.Unlock()
	ni.ActiveConns = cw.getActiveConns()
	ni.TotalConns = cw.getTotalConns()
}
func (ni *NodeInfo) updateResources() {
	ni.Lock()
	defer ni.Unlock()
	ni.CPU = store.resource.CPU.getCurrent()
	ni.Memory = store.resource.Memory.getCurrent()
}
func (ni *NodeInfo) reflectRequest(receivedBytes, sentBytes int64) {
	ni.Lock()
	defer ni.Unlock()
	ni.ReceivedBytes += receivedBytes
	ni.SentBytes += sentBytes
	ni.RequestCount++
	ni.UpdatedAt = time.Now().UnixNano()
}
func (ni *NodeInfo) getClone() *NodeInfo {
	ni.RLock()
	defer ni.RUnlock()
	node := *ni
	return &node
}
func (ni *NodeInfo) getCreatedAt() int64 {
	ni.RLock()
	defer ni.RUnlock()
	return ni.CreatedAt
}
func (ni *NodeInfo) setCreatedAt(_time int64) {
	ni.Lock()
	defer ni.Unlock()
	ni.CreatedAt = _time
}
func (ni *NodeInfo) setNow() {
	ni.Lock()
	defer ni.Unlock()
	ni.UpdatedAt = time.Now().UnixNano()
}
func (ni *NodeInfo) clearConns() {
	ni.Lock()
	defer ni.Unlock()
	ni.TotalConns = 0
	ni.ActiveConns = 0
	if ni.ELBs != nil {
		for _, elb := range ni.ELBs {
			elb.TotalConns = 0
			elb.ActiveConns = 0
		}
	}
}
func (ni *NodeInfo) addTotalConnsELB(remoteAddr string, cnt int64) {
	ni.Lock()
	defer ni.Unlock()
	now := time.Now().UnixNano()
	if _, ok := ni.ELBs[remoteAddr]; !ok {
		ni.ELBs[remoteAddr] = &NodeInfo{}
		ni.ELBs[remoteAddr].CreatedAt = now
	}
	ni.ELBs[remoteAddr].UpdatedAt = now
	ni.ELBs[remoteAddr].TotalConns += cnt
}
func (ni *NodeInfo) addActiveConnsELB(remoteAddr string, cnt int64) {
	ni.Lock()
	defer ni.Unlock()
	ni.ELBs[remoteAddr].UpdatedAt = time.Now().UnixNano()
	ni.ELBs[remoteAddr].TotalConns += cnt
}

func elbStatsHandler(w http.ResponseWriter, r *http.Request) {
	var rawFlg bool
	qsMap := r.URL.Query()
	for key := range qsMap {
		if key == "raw" {
			rawFlg = true
			break
		}
	}
	updateNode()
	if rawFlg {
		fmt.Fprintf(w, "\n%s\n", getStoreNodeELBJSON())
	} else {
		fmt.Fprintf(w, "\n%s\n", easeReadJSON(getStoreNodeELBJSON()))
	}
}

func monitorHandler(w http.ResponseWriter, r *http.Request) {
	var rawFlg bool
	qsMap := r.URL.Query()
	for key := range qsMap {
		if key == "raw" {
			rawFlg = true
			break
		}
	}
	updateNode()
	if rawFlg {
		fmt.Fprintf(w, "\n%s\n", getStoreNodeJSON())
	} else {
		fmt.Fprintf(w, "\n%s\n", easeReadJSON(getStoreNodeJSON()))
	}
}

func updateNode() {
	store.node.updateResources()
	store.node.updateConns()
	store.node.setNow()
}

// ELBNode ... temp struct for json.MarshalIndent
type ELBNode struct {
	ELBs map[string]*NodeInfo `json:"elbs"`
}

func getStoreNodeELBJSON() []byte {
	store.RLock()
	defer store.RUnlock()
	elbNodes := map[string]*NodeInfo{}
	for elbIP, elbNode := range store.node.ELBs {
		if _, ok := elbNodes[elbIP]; !ok {
			elbNodes[elbIP] = NewNodeInfo()
		}
		if elbNode.CreatedAt < elbNodes[elbIP].CreatedAt {
			elbNodes[elbIP].CreatedAt = elbNode.CreatedAt
		}
		if elbNode.UpdatedAt > elbNodes[elbIP].UpdatedAt {
			elbNodes[elbIP].UpdatedAt = elbNode.UpdatedAt
		}
		elbNodes[elbIP].RequestCount += elbNode.RequestCount
		elbNodes[elbIP].SentBytes += elbNode.SentBytes
		elbNodes[elbIP].ReceivedBytes += elbNode.ReceivedBytes
		elbNodes[elbIP].ActiveConns += elbNode.ActiveConns
		elbNodes[elbIP].TotalConns += elbNode.TotalConns
	}
	elbsJSON, err := json.MarshalIndent(ELBNode{ELBs: elbNodes}, "", "  ")
	if err != nil {
		fmt.Printf("failed to json.MarshalIndent: %v", err)
		return []byte{}
	}
	return elbsJSON
}

func getStoreNodeJSON() []byte {
	store.RLock()
	defer store.RUnlock()
	storeNodeJSON, err := json.MarshalIndent(store.node, "", "  ")
	if err != nil {
		fmt.Printf("failed to json.MarshalIndent: %v", err)
		return []byte{}
	}
	return storeNodeJSON
}

var utimeRegexp = regexp.MustCompile(`_at": ([0-9]{19}),`)
var bytesRegexp = regexp.MustCompile(`_bytes": ([0-9]+),`)
var usageRegexp = regexp.MustCompile(`(cpu|memory)": ([0-9.]+),?`)

func easeReadJSON(inputJSON []byte) (readableJSON string) {
	// TODO: tuning replace speed
	buffer := bytes.NewBuffer(inputJSON)
	line, err := buffer.ReadString('\n')
	for err == nil {
		// unixtime -> rfc3339 format string
		matches := utimeRegexp.FindStringSubmatch(line)
		if len(matches) > 1 {
			line = strings.Replace(line, matches[1], `"`+easeReadUnixTime(matches[1])+`"`, 1)
		} else {
			// bytes -> human readable string
			matches = bytesRegexp.FindStringSubmatch(line)
			if len(matches) > 1 {
				line = strings.Replace(line, matches[1], `"`+easeReadBytes(matches[1])+`"`, 1)
			} else {
				// usageRate -> round decimal string
				matches = usageRegexp.FindStringSubmatch(line)
				if len(matches) > 1 {
					line = strings.Replace(line, matches[2], easeReadUsageRate(matches[2]), 1)
				}
			}
		}
		readableJSON += line
		line, err = buffer.ReadString('\n')
	}
	readableJSON += line
	return
}

func easeReadBytes(sb string) string {
	b, _ := strconv.Atoi(sb)
	const unit = 1000
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(b)/float64(div), "kMGTPE"[exp])
}

func easeReadUnixTime(st string) string {
	t, _ := strconv.ParseInt(st, 10, 64)
	return time.Unix(0, t).Format(time.RFC3339)
}

func easeReadUsageRate(su string) string {
	u, _ := strconv.ParseFloat(su, 64)
	return fmt.Sprintf("%.1f", math.Round(u*10)/10)
}
