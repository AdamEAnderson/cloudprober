// Copyright 2017 The Cloudprober Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
Package probes provides an interface to initialize probes using prober config.
*/
package probes

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudprober/cloudprober/metrics"
	"github.com/cloudprober/cloudprober/probes/dns"
	"github.com/cloudprober/cloudprober/probes/external"
	grpcprobe "github.com/cloudprober/cloudprober/probes/grpc"
	httpprobe "github.com/cloudprober/cloudprober/probes/http"
	"github.com/cloudprober/cloudprober/probes/options"
	"github.com/cloudprober/cloudprober/probes/ping"
	configpb "github.com/cloudprober/cloudprober/probes/proto"
	"github.com/cloudprober/cloudprober/probes/tcp"
	"github.com/cloudprober/cloudprober/probes/udp"
	"github.com/cloudprober/cloudprober/probes/udplistener"
	"github.com/cloudprober/cloudprober/web/formatutils"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var (
	userDefinedProbes   = make(map[string]Probe)
	userDefinedProbesMu sync.RWMutex
	extensionMap        = make(map[int]func() Probe)
	extensionMapMu      sync.RWMutex
)

// Probe interface represents a probe.
//
// A probe is initilized using the Init() method. Init takes the name of the
// probe and probe options.
//
// Start() method starts the probe. Start is not expected to return for the
// lifetime of the prober. It takes a data channel that it writes the probe
// results on. Actual publishing of these results is handled by cloudprober
// itself.
type Probe interface {
	Init(name string, opts *options.Options) error
	Start(ctx context.Context, dataChan chan *metrics.EventMetrics)
}

// ProbeInfo encapsulates the probe and associated information.
type ProbeInfo struct {
	Probe
	ProbeDef      *configpb.ProbeDef // Full probe definition
	Options       *options.Options
	Name          string
	Type          string
	Interval      string
	Timeout       string
	TargetsDesc   string
	LatencyDistLB string
	LatencyUnit   string
	ProbeConf     string
	SourceIP      string
}

func getExtensionProbe(p *configpb.ProbeDef) (Probe, interface{}, error) {
	extensionMapMu.RLock()
	defer extensionMapMu.RUnlock()

	var newProbeFunc func() Probe
	var value interface{}

	proto.RangeExtensions(p, func(xt protoreflect.ExtensionType, val interface{}) bool {
		newProbeFunc = extensionMap[int(xt.TypeDescriptor().Number())]
		if newProbeFunc != nil {
			value = val
			return false
		}
		return true
	})

	if newProbeFunc == nil {
		return nil, nil, fmt.Errorf("no extension probe found in the probe config")
	}

	return newProbeFunc(), value, nil
}

// CreateProbe creates a new probe.
func CreateProbe(p *configpb.ProbeDef, opts *options.Options) (*ProbeInfo, error) {
	probe, probeConf, err := initProbe(p, opts)
	if err != nil {
		return nil, err
	}

	probeInfo := &ProbeInfo{
		Probe:       probe,
		ProbeDef:    p,
		Options:     opts,
		Name:        p.GetName(),
		Type:        p.GetType().String(),
		Interval:    opts.Interval.String(),
		Timeout:     opts.Timeout.String(),
		TargetsDesc: p.Targets.String(),
		LatencyUnit: opts.LatencyUnit.String(),
		ProbeConf:   formatutils.ConfToString(probeConf),
	}

	if opts.LatencyDist != nil {
		probeInfo.LatencyDistLB = fmt.Sprintf("%v", opts.LatencyDist.Data().LowerBounds)
	}
	if opts.SourceIP != nil {
		probeInfo.SourceIP = opts.SourceIP.String()
	}
	return probeInfo, nil
}

func initProbe(p *configpb.ProbeDef, opts *options.Options) (probe Probe, probeConf interface{}, err error) {
	switch p.GetType() {
	case configpb.ProbeDef_PING:
		probe = &ping.Probe{}
		probeConf = p.GetPingProbe()
	case configpb.ProbeDef_HTTP:
		probe = &httpprobe.Probe{}
		probeConf = p.GetHttpProbe()
	case configpb.ProbeDef_DNS:
		probe = &dns.Probe{}
		probeConf = p.GetDnsProbe()
	case configpb.ProbeDef_EXTERNAL:
		probe = &external.Probe{}
		probeConf = p.GetExternalProbe()
	case configpb.ProbeDef_TCP:
		probe = &tcp.Probe{}
		probeConf = p.GetTcpProbe()
	case configpb.ProbeDef_UDP:
		probe = &udp.Probe{}
		probeConf = p.GetUdpProbe()
	case configpb.ProbeDef_UDP_LISTENER:
		probe = &udplistener.Probe{}
		probeConf = p.GetUdpListenerProbe()
	case configpb.ProbeDef_GRPC:
		probe = &grpcprobe.Probe{}
		probeConf = p.GetGrpcProbe()
	case configpb.ProbeDef_EXTENSION:
		probe, probeConf, err = getExtensionProbe(p)
		if err != nil {
			return
		}
	case configpb.ProbeDef_USER_DEFINED:
		userDefinedProbesMu.RLock()
		defer userDefinedProbesMu.RUnlock()
		probe = userDefinedProbes[p.GetName()]
		if probe == nil {
			err = fmt.Errorf("unregistered user defined probe: %s", p.GetName())
			return
		}
		probeConf = p.GetUserDefinedProbe()
	default:
		err = fmt.Errorf("unknown probe type: %s", p.GetType())
		return
	}

	opts.ProbeConf = probeConf
	err = probe.Init(p.GetName(), opts)
	return
}

// RegisterUserDefined allows you to register a user defined probe with
// cloudprober.
// Example usage:
//
//	import (
//		"github.com/cloudprober/cloudprober"
//		"github.com/cloudprober/cloudprober/probes"
//	)
//
//	p := &FancyProbe{}
//	probes.RegisterUserDefined("fancy_probe", p)
//	pr, err := cloudprober.InitFromConfig(*configFile)
//	if err != nil {
//		log.Exitf("Error initializing cloudprober. Err: %v", err)
//	}
func RegisterUserDefined(name string, probe Probe) {
	userDefinedProbesMu.Lock()
	defer userDefinedProbesMu.Unlock()
	userDefinedProbes[name] = probe
}

// RegisterProbeType registers a new probe-type. New probe types are integrated
// with the config subsystem using the protobuf extensions.
//
// TODO(manugarg): Add a full example of using extensions.
func RegisterProbeType(extensionFieldNo int, newProbeFunc func() Probe) {
	extensionMapMu.Lock()
	defer extensionMapMu.Unlock()
	extensionMap[extensionFieldNo] = newProbeFunc
}
